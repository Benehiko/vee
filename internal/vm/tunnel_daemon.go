package vm

import (
	"context"
	"fmt"
	"sync"

	"go.uber.org/zap"
)

// tunnelPortMin and tunnelPortMax bound the port range background tunnels are
// allocated from when a spec has no recorded port yet. It sits above the SSH
// proxy range (2200-2299) so the two never contend for the same port.
const (
	tunnelPortMin = 2300
	tunnelPortMax = 2399
)

// activeTunnel is a running background tunnel: the proxy plus the spec it was
// started from, so reconcile can detect a spec change (a new bind address, a
// new port) and restart the proxy rather than leaving a stale listener up.
type activeTunnel struct {
	spec  TunnelSpec
	proxy *sshLoopbackProxy
	// guestPort is the port inside the guest, resolved from the VM's service
	// list at start. Recorded for the router and for `--list` output.
	guestPort int
	protocol  ServiceProtocol
}

// TunnelStatus is the reported state of one background tunnel, as returned
// over the control socket to `vee tunnel --list`.
type TunnelStatus struct {
	VM        string          `json:"vm"`
	Service   string          `json:"service"`
	Hostname  string          `json:"hostname"`
	Bind      string          `json:"bind"`
	Port      int             `json:"port"`
	GuestPort int             `json:"guest_port,omitempty"`
	Protocol  ServiceProtocol `json:"protocol,omitempty"`
	// Active is true when the daemon currently holds a listener for this
	// tunnel. A registered tunnel whose VM is stopped is inactive but still
	// listed, so the user can see what will come back on next boot.
	Active bool `json:"active"`
	// Reason explains why a registered tunnel is not active (VM stopped,
	// service no longer declared, listener failed to bind).
	Reason string `json:"reason,omitempty"`
}

// tunnelState holds the daemon's live tunnels. Like sshProxies, only the
// daemon's long-lived Manager populates it.
type tunnelState struct {
	mu     sync.Mutex
	active map[string]*activeTunnel
	// inactive records why a registered tunnel has no listener, so --list can
	// explain the gap instead of silently showing nothing.
	inactive map[string]string
}

// reconcileTunnels aligns the daemon's background tunnels with the registry,
// starting proxies for registered tunnels whose VM is running and tearing down
// those whose VM has stopped or whose registration was removed.
//
// This is what makes background tunnels survive a host reboot: the registry is
// on disk, the daemon starts at boot, and each poll tick re-establishes any
// tunnel whose guest has come up. No tunnel is started for a stopped VM — the
// proxy would accept connections it could never fulfil.
func (m *Manager) reconcileTunnels(ctx context.Context) {
	log := m.provider.Logger()

	specs, err := m.Tunnels().List()
	if err != nil {
		log.Warn("tunnel reconcile: registry unreadable", zap.Error(err))
		return
	}

	entries, err := m.List()
	if err != nil {
		log.Debug("tunnel reconcile: list failed", zap.Error(err))
		return
	}
	running := map[string]*ListEntry{}
	for _, e := range entries {
		if e.Config != nil && e.State != nil && e.State.Running && isAlive(e.State.PID) {
			running[e.Config.Name] = e
		}
	}

	// Resolve each spec against the VM's current service list. A spec whose
	// VM is stopped, or whose service was removed from the config, is kept
	// registered but not started — the user's intent outlives a stopped guest.
	type wantEntry struct {
		spec      TunnelSpec
		guestPort int
		protocol  ServiceProtocol
	}
	wanted := map[string]wantEntry{}
	reasons := map[string]string{}
	for _, spec := range specs {
		entry, ok := running[spec.VM]
		if !ok {
			reasons[spec.Key()] = "VM not running"
			continue
		}
		var found *ResolvedService
		for _, s := range ResolvedServices(entry.Config, entry.State) {
			if s.Name == spec.Service {
				svc := s
				found = &svc
				break
			}
		}
		if found == nil {
			reasons[spec.Key()] = fmt.Sprintf("VM %q no longer declares service %q", spec.VM, spec.Service)
			continue
		}
		if found.Port <= 0 {
			reasons[spec.Key()] = "service port not yet assigned"
			continue
		}
		wanted[spec.Key()] = wantEntry{spec: spec, guestPort: found.Port, protocol: found.Protocol}
	}

	m.tunnels.mu.Lock()
	defer m.tunnels.mu.Unlock()
	if m.tunnels.active == nil {
		m.tunnels.active = map[string]*activeTunnel{}
	}
	m.tunnels.inactive = reasons

	// Stop tunnels that are no longer wanted, or whose spec changed in a way
	// the running listener cannot honour.
	for key, t := range m.tunnels.active {
		want, ok := wanted[key]
		if ok && want.spec.BindAddr() == t.spec.BindAddr() &&
			want.spec.Port == t.spec.Port && want.guestPort == t.guestPort {
			continue
		}
		t.proxy.Close()
		delete(m.tunnels.active, key)
		log.Info("background tunnel stopped",
			zap.String("vm", t.spec.VM), zap.String("service", t.spec.Service),
			zap.Int("port", t.proxy.Port()))
	}

	for key, want := range wanted {
		if _, ok := m.tunnels.active[key]; ok {
			continue
		}
		port := want.spec.Port
		if port <= 0 {
			port = availablePort(0, tunnelPortMin, tunnelPortMax)
		}
		proxy, perr := startGuestProxy(ctx, want.spec.VM, want.spec.BindAddr(), port,
			want.guestPort, m.guestIPResolver(want.spec.VM), log)
		if perr != nil {
			m.tunnels.inactive[key] = perr.Error()
			log.Warn("background tunnel failed to start",
				zap.String("vm", want.spec.VM), zap.String("service", want.spec.Service),
				zap.Error(perr))
			continue
		}
		m.tunnels.active[key] = &activeTunnel{
			spec:      want.spec,
			proxy:     proxy,
			guestPort: want.guestPort,
			protocol:  want.protocol,
		}
		log.Info("background tunnel started",
			zap.String("vm", want.spec.VM), zap.String("service", want.spec.Service),
			zap.String("bind", proxy.BindAddr()), zap.Int("port", proxy.Port()),
			zap.Int("guest_port", want.guestPort))

		// Record the port actually bound so the next daemon start reuses it,
		// keeping bookmarks and DNS records valid across reboots.
		if proxy.Port() != want.spec.Port || want.spec.Protocol != want.protocol {
			updated := want.spec
			updated.Port = proxy.Port()
			updated.Protocol = want.protocol
			if err := m.Tunnels().Add(updated); err != nil {
				log.Debug("tunnel reconcile: could not persist allocated port",
					zap.String("vm", updated.VM), zap.Error(err))
			}
		}
	}
}

// stopAllTunnels tears down every background tunnel. Called when the daemon
// exits so no listener outlives the process that reconciles it.
func (m *Manager) stopAllTunnels() {
	m.tunnels.mu.Lock()
	defer m.tunnels.mu.Unlock()
	for key, t := range m.tunnels.active {
		t.proxy.Close()
		delete(m.tunnels.active, key)
	}
}

// TunnelStatuses reports every registered tunnel with its live state. Used by
// the control socket to answer `vee tunnel --list` from the daemon, which is
// the only process that knows which listeners are actually up.
func (m *Manager) TunnelStatuses() ([]TunnelStatus, error) {
	specs, err := m.Tunnels().List()
	if err != nil {
		return nil, err
	}

	m.tunnels.mu.Lock()
	defer m.tunnels.mu.Unlock()

	out := make([]TunnelStatus, 0, len(specs))
	for _, spec := range specs {
		st := TunnelStatus{
			VM:       spec.VM,
			Service:  spec.Service,
			Hostname: spec.VHost(),
			Bind:     spec.BindAddr(),
			Port:     spec.Port,
			Protocol: spec.Protocol,
		}
		if t, ok := m.tunnels.active[spec.Key()]; ok {
			st.Active = true
			st.Port = t.proxy.Port()
			st.Bind = t.proxy.BindAddr()
			st.GuestPort = t.guestPort
			st.Protocol = t.protocol
		} else if reason, ok := m.tunnels.inactive[spec.Key()]; ok {
			st.Reason = reason
		} else {
			st.Reason = "not started"
		}
		out = append(out, st)
	}
	return out, nil
}

// activeRoutes returns the live tunnels the vhost router should serve, keyed
// by hostname label. Only HTTP and HTTPS services are routable — a SPICE or
// raw TCP tunnel has no meaningful HTTP representation, and is reachable on
// its port directly.
func (m *Manager) activeRoutes() map[string]*activeTunnel {
	m.tunnels.mu.Lock()
	defer m.tunnels.mu.Unlock()
	out := map[string]*activeTunnel{}
	for _, t := range m.tunnels.active {
		if t.protocol != ServiceHTTP && t.protocol != ServiceHTTPS {
			continue
		}
		label := SanitizeHostname(t.spec.VHost())
		if label == "" {
			continue
		}
		// A duplicate label means two VMs export the same service name with
		// no explicit hostname override. Keep the first by sorted key so the
		// winner is stable rather than map-iteration dependent, and let the
		// user disambiguate with --hostname.
		if existing, ok := out[label]; ok && existing.spec.Key() < t.spec.Key() {
			continue
		}
		out[label] = t
	}
	return out
}
