package vm

import (
	"context"
	"fmt"
	"io"
	"net"
	"time"

	"go.uber.org/zap"
)

// ipResolver returns the guest's current LAN address. It is called per
// accepted connection, not once at proxy start, so a DHCP renewal that hands
// the guest a new lease keeps working without a proxy restart — and so the
// proxy can come up before the guest has finished booting and obtained an
// address at all.
type ipResolver func(ctx context.Context) (string, error)

// sshLoopbackProxy is a daemon-hosted listener on 127.0.0.1 that forwards to
// port 22 on a bridge-mode guest's LAN address. It restores the loopback
// convenience of user-mode hostfwds, which do not exist on a bridge (#110):
// with it, `vee ssh` and third-party tools (IDE remote-SSH targets, scripts)
// get a stable 127.0.0.1:<port> endpoint regardless of what address DHCP
// hands the guest.
type sshLoopbackProxy struct {
	vmName     string
	port       int
	targetPort int
	ln         net.Listener
	cancel     context.CancelFunc
	done       chan struct{}
}

// startSSHLoopbackProxy listens on 127.0.0.1:port (an ephemeral port when
// port is 0) and forwards to targetPort on the resolved guest address until
// Close. The actual bound port is in Port().
func startSSHLoopbackProxy(ctx context.Context, vmName string, port, targetPort int, resolve ipResolver, log *zap.Logger) (*sshLoopbackProxy, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, fmt.Errorf("ssh loopback proxy for %q: %w", vmName, err)
	}
	pctx, cancel := context.WithCancel(ctx)
	p := &sshLoopbackProxy{
		vmName:     vmName,
		port:       ln.Addr().(*net.TCPAddr).Port,
		targetPort: targetPort,
		ln:         ln,
		cancel:     cancel,
		done:       make(chan struct{}),
	}
	go p.serve(pctx, resolve, log)
	return p, nil
}

func (p *sshLoopbackProxy) Port() int { return p.port }

// Close stops the listener and waits for the accept loop to exit. In-flight
// connections are severed via the proxy context.
func (p *sshLoopbackProxy) Close() {
	p.cancel()
	_ = p.ln.Close()
	<-p.done
}

// reconcileSSHProxies aligns the daemon's loopback proxies with the set of
// running bridge-mode VMs that configured an ssh_port. Called from the
// daemon loop on every poll tick; CLI-only setups (no daemon) simply never
// get loopback proxies and `vee ssh` falls back to the guest's LAN address.
func (m *Manager) reconcileSSHProxies(ctx context.Context) {
	log := m.provider.Logger()
	entries, err := m.List()
	if err != nil {
		log.Debug("ssh proxy reconcile: list failed", zap.Error(err))
		return
	}

	wanted := map[string]*VMConfig{}
	for _, e := range entries {
		cfg, st := e.Config, e.State
		if cfg == nil || st == nil || !st.Running || !isAlive(st.PID) {
			continue
		}
		if !effectiveBridge(cfg) || cfg.SSHPort <= 0 {
			continue
		}
		wanted[cfg.Name] = cfg
	}

	m.sshProxyMu.Lock()
	defer m.sshProxyMu.Unlock()
	if m.sshProxies == nil {
		m.sshProxies = map[string]*sshLoopbackProxy{}
	}

	for name, p := range m.sshProxies {
		if _, ok := wanted[name]; ok {
			continue
		}
		p.Close()
		delete(m.sshProxies, name)
		m.clearStateSSHPort(name, p.Port())
		log.Info("ssh loopback proxy stopped", zap.String("vm", name), zap.Int("port", p.Port()))
	}

	for name, cfg := range wanted {
		if _, ok := m.sshProxies[name]; ok {
			continue
		}
		port := availablePort(cfg.SSHPort, 2200, 2299)
		p, perr := startSSHLoopbackProxy(ctx, name, port, 22, m.guestIPResolver(name), log)
		if perr != nil {
			log.Warn("ssh loopback proxy failed to start", zap.String("vm", name), zap.Error(perr))
			continue
		}
		m.sshProxies[name] = p
		m.setStateSSHPort(name, p.Port())
		log.Info("ssh loopback proxy started", zap.String("vm", name), zap.Int("port", p.Port()))
	}
}

// stopAllSSHProxies tears down every proxy and clears the recorded ports so
// state stays truthful when the daemon exits: `vee ssh` probes the loopback
// port before using it, but other readers of state should not see a port
// nothing serves.
func (m *Manager) stopAllSSHProxies() {
	m.sshProxyMu.Lock()
	defer m.sshProxyMu.Unlock()
	for name, p := range m.sshProxies {
		p.Close()
		delete(m.sshProxies, name)
		m.clearStateSSHPort(name, p.Port())
	}
}

// guestIPResolver loads config and state fresh on every call: the MAC is
// stable, but the QGA socket path only exists once the VM is up, and the
// lease can change across guest reboots.
func (m *Manager) guestIPResolver(name string) ipResolver {
	return func(ctx context.Context) (string, error) {
		cfg, err := m.loadConfig(name)
		if err != nil {
			return "", err
		}
		var resolveErr error
		if cfg.NIC.MAC != "" {
			var ip string
			if ip, resolveErr = ResolveIPFromMAC(cfg.NIC.MAC); resolveErr == nil {
				return ip, nil
			}
		} else {
			resolveErr = fmt.Errorf("VM %q has no MAC address recorded", name)
		}
		if st, serr := m.loadState(name); serr == nil && st.QGASocket != "" {
			return ResolveIPFromQGA(ctx, st.QGASocket)
		}
		return "", resolveErr
	}
}

// setStateSSHPort / clearStateSSHPort do a load-modify-save on the VM's
// state file. clear only removes the port this proxy recorded, so it cannot
// clobber a hostfwd port written by a Start that raced the reconcile.
func (m *Manager) setStateSSHPort(name string, port int) {
	st, err := m.loadState(name)
	if err != nil || st.SSHPort == port {
		return
	}
	st.SSHPort = port
	if err := m.saveState(name, st); err != nil {
		m.provider.Logger().Debug("ssh proxy: state save failed", zap.String("vm", name), zap.Error(err))
	}
}

func (m *Manager) clearStateSSHPort(name string, port int) {
	st, err := m.loadState(name)
	if err != nil || st.SSHPort != port {
		return
	}
	st.SSHPort = 0
	if err := m.saveState(name, st); err != nil {
		m.provider.Logger().Debug("ssh proxy: state save failed", zap.String("vm", name), zap.Error(err))
	}
}

func (p *sshLoopbackProxy) serve(ctx context.Context, resolve ipResolver, log *zap.Logger) {
	defer close(p.done)
	for {
		conn, err := p.ln.Accept()
		if err != nil {
			// Accept fails when the listener is closed; that is the
			// shutdown path, not an error.
			return
		}
		go p.forward(ctx, conn, resolve, log)
	}
}

func (p *sshLoopbackProxy) forward(ctx context.Context, local net.Conn, resolve ipResolver, log *zap.Logger) {
	defer func() { _ = local.Close() }()

	rctx, rcancel := context.WithTimeout(ctx, 10*time.Second)
	ip, err := resolve(rctx)
	rcancel()
	if err != nil {
		log.Debug("ssh loopback proxy could not resolve guest IP",
			zap.String("vm", p.vmName), zap.Error(err))
		return
	}

	var d net.Dialer
	guest, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip, fmt.Sprintf("%d", p.targetPort)))
	if err != nil {
		log.Debug("ssh loopback proxy could not reach guest sshd",
			zap.String("vm", p.vmName), zap.String("guest", ip), zap.Error(err))
		return
	}
	defer func() { _ = guest.Close() }()

	// Same shape as cmd/tunnel.go's proxyConn: first direction to finish
	// tears the pair down; ssh handles the abrupt close.
	pair := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(guest, local); pair <- struct{}{} }()
	go func() { _, _ = io.Copy(local, guest); pair <- struct{}{} }()
	select {
	case <-pair:
	case <-ctx.Done():
	}
}
