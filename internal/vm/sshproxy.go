package vm

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
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
	bindAddr   string
	port       int
	targetPort int
	ln         net.Listener
	cancel     context.CancelFunc
	done       chan struct{}
	// fallback, when set, is tried after a direct dial to the guest address
	// fails. Nil for the SSH loopback proxies themselves, which target port
	// 22 and so have nothing to fall back through.
	fallback guestDialer

	// useFallback latches once the direct dial has been shown not to work,
	// so subsequent connections skip straight to the fallback instead of
	// paying the dial timeout again. Whether a service is bound to guest
	// loopback is a property of the guest's configuration, not of any one
	// connection, so the answer does not change under the proxy. A guest
	// reconfigured to bind its LAN address is picked up on the next daemon
	// reconcile, which builds a fresh proxy.
	stickyMu    sync.Mutex
	useFallback bool
}

// startSSHLoopbackProxy listens on 127.0.0.1:port (an ephemeral port when
// port is 0) and forwards to targetPort on the resolved guest address until
// Close. The actual bound port is in Port().
func startSSHLoopbackProxy(ctx context.Context, vmName string, port, targetPort int, resolve ipResolver, log *zap.Logger) (*sshLoopbackProxy, error) {
	return startGuestProxy(ctx, vmName, BindLoopback, port, targetPort, resolve, nil, log)
}

// startGuestProxy is the general form behind both the daemon's SSH loopback
// proxies and its background service tunnels: it listens on bindAddr:port and
// forwards every connection to targetPort on the guest address resolved at
// connect time.
//
// bindAddr is a parameter rather than a constant because background tunnels
// may be published to the LAN on 0.0.0.0 (`vee tunnel --host`), while the SSH
// proxies stay on loopback — exposing a guest's sshd to the LAN is never
// implied by wanting a stable local port.
func startGuestProxy(ctx context.Context, vmName, bindAddr string, port, targetPort int, resolve ipResolver, fallback guestDialer, log *zap.Logger) (*sshLoopbackProxy, error) {
	if bindAddr == "" {
		bindAddr = BindLoopback
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", net.JoinHostPort(bindAddr, fmt.Sprintf("%d", port)))
	if err != nil {
		return nil, fmt.Errorf("guest proxy for %q: %w", vmName, err)
	}
	pctx, cancel := context.WithCancel(ctx)
	p := &sshLoopbackProxy{
		vmName:     vmName,
		bindAddr:   bindAddr,
		port:       ln.Addr().(*net.TCPAddr).Port,
		targetPort: targetPort,
		ln:         ln,
		cancel:     cancel,
		done:       make(chan struct{}),
		fallback:   fallback,
	}
	go p.serve(pctx, resolve, log)
	return p, nil
}

// sticky reports whether the direct dial has already been ruled out.
func (p *sshLoopbackProxy) sticky() bool {
	p.stickyMu.Lock()
	defer p.stickyMu.Unlock()
	return p.useFallback && p.fallback != nil
}

// markSticky records that the fallback is the working path for this service,
// so later connections skip the direct dial.
func (p *sshLoopbackProxy) markSticky(log *zap.Logger) {
	p.stickyMu.Lock()
	first := !p.useFallback
	p.useFallback = true
	p.stickyMu.Unlock()
	if first {
		log.Info("guest service not reachable at guest address; routing this tunnel over ssh",
			zap.String("vm", p.vmName), zap.Int("port", p.targetPort))
	}
}

func (p *sshLoopbackProxy) Port() int { return p.port }

// BindAddr reports the address the proxy is listening on.
func (p *sshLoopbackProxy) BindAddr() string { return p.bindAddr }

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
		log.Debug("guest proxy could not resolve guest IP",
			zap.String("vm", p.vmName), zap.Error(err))
		return
	}

	var guest net.Conn
	if p.sticky() {
		// Already established that the direct dial does not reach this
		// service; skip it rather than paying the timeout on every request.
		guest, err = p.fallback(ctx, ip, p.targetPort)
		if err != nil {
			log.Warn("guest proxy ssh fallback failed",
				zap.String("vm", p.vmName), zap.String("guest", ip),
				zap.Int("port", p.targetPort), zap.Error(err))
			return
		}
	} else {
		// Dial the guest's own address first: it is the cheap path and the
		// one that works for a service bound to a routable interface. The
		// timeout is short because a guest that is up either answers or
		// refuses promptly; the budget here is for the fallback, not for
		// waiting out a silent drop.
		d := net.Dialer{Timeout: 2 * time.Second}
		guest, err = d.DialContext(ctx, "tcp", net.JoinHostPort(ip, fmt.Sprintf("%d", p.targetPort)))
		if err != nil {
			if p.fallback == nil {
				log.Warn("guest proxy could not reach guest port",
					zap.String("vm", p.vmName), zap.String("guest", ip),
					zap.Int("port", p.targetPort), zap.Error(err))
				return
			}
			// Unreachable at the guest's address does not mean unreachable:
			// the service may be bound to guest loopback, or the guest may be
			// on user-mode NAT. Both are reachable over SSH.
			log.Debug("guest proxy direct dial failed, trying ssh",
				zap.String("vm", p.vmName), zap.String("guest", ip),
				zap.Int("port", p.targetPort), zap.Error(err))
			guest, err = p.fallback(ctx, ip, p.targetPort)
			if err != nil {
				log.Warn("guest proxy could not reach guest port, ssh fallback failed",
					zap.String("vm", p.vmName), zap.String("guest", ip),
					zap.Int("port", p.targetPort), zap.Error(err))
				return
			}
			p.markSticky(log)
		}
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

// guestDialer opens a connection to targetPort on a guest. The direct
// implementation dials the guest's own address; the SSH implementation
// tunnels to the guest's loopback instead.
type guestDialer func(ctx context.Context, ip string, targetPort int) (net.Conn, error)

// sshFallbackDialer connects to targetPort on the guest's *loopback* through
// an SSH session, for services that are unreachable at the guest's own
// address. Two shapes of guest need this:
//
//   - A service deliberately bound to guest loopback. qBittorrent's web UI
//     does exactly this (WebUI\Address=127.0.0.1), so a kill-switched guest
//     cannot leak it to the LAN. Nothing listens on the guest's LAN address,
//     and a direct dial is refused.
//   - A user-mode NAT guest, whose 10.0.2.x address is meaningful only inside
//     QEMU's stack. A direct dial leaves the host toward the LAN gateway and
//     is never answered.
//
// In both cases sshd is reachable — over the daemon's own loopback proxy on a
// bridge, or a hostfwd under user-mode — so SSH is the one path that always
// lands inside the guest. This mirrors the fallback cmd/tunnel.go already
// performs for foreground tunnels; without it here, a background tunnel to
// such a service accepts connections and then hangs until the client times
// out, which is how #162 presented.
func (m *Manager) sshFallbackDialer(vmName string) guestDialer {
	return func(ctx context.Context, ip string, targetPort int) (net.Conn, error) {
		cfg, err := m.LoadConfig(vmName)
		if err != nil {
			return nil, fmt.Errorf("load config: %w", err)
		}
		user := cfg.SSHUsername()
		if user == "" {
			return nil, fmt.Errorf("no ssh user configured for %q", vmName)
		}

		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("home dir: %w", err)
		}
		// Fixed path under the user's own home, not attacker-influenced.
		keyPath := filepath.Join(home, ".vee", "ssh", "id_ed25519")
		key, err := os.ReadFile(keyPath) //nolint:gosec // G304: fixed path under the user's home dir.
		if err != nil {
			return nil, fmt.Errorf("read ssh key: %w", err)
		}

		host, port := m.tunnelSSHEndpoint(ctx, vmName, ip)
		if port == 0 {
			return nil, fmt.Errorf("no ssh endpoint for %q", vmName)
		}

		addr := net.JoinHostPort(host, strconv.Itoa(port))
		target := net.JoinHostPort(BindLoopback, strconv.Itoa(targetPort))

		// One SSH connection carries every forwarded channel for this VM. A
		// browser opens several connections per page, and a fresh handshake
		// on each one would add seconds to every request.
		cl, err := m.sshTunnelClient(ctx, vmName, addr, user, key)
		if err != nil {
			return nil, err
		}
		guest, err := cl.client.DialContext(ctx, "tcp", target)
		if err != nil {
			// The cached connection may have died with the guest; drop it and
			// retry once so a rebooted VM recovers without a daemon restart.
			m.dropSSHTunnelClient(vmName, cl)
			cl, err = m.sshTunnelClient(ctx, vmName, addr, user, key)
			if err != nil {
				return nil, err
			}
			guest, err = cl.client.DialContext(ctx, "tcp", target)
			if err != nil {
				m.dropSSHTunnelClient(vmName, cl)
				return nil, fmt.Errorf("ssh forward to loopback:%d: %w", targetPort, err)
			}
		}
		return guest, nil
	}
}

// tunnelSSHEndpoint picks the host:port to reach the guest's sshd, mirroring
// cmd/tunnel.go's dispatch of the same name: prefer the daemon's loopback
// proxy when one is serving this VM, and fall back to port 22 at the guest's
// own address otherwise.
func (m *Manager) tunnelSSHEndpoint(ctx context.Context, vmName, ip string) (string, int) {
	m.sshProxyMu.Lock()
	p := m.sshProxies[vmName]
	m.sshProxyMu.Unlock()
	if p != nil {
		return BindLoopback, p.Port()
	}
	if st, err := m.LoadState(vmName); err == nil && st != nil && st.SSHPort > 0 {
		return BindLoopback, st.SSHPort
	}
	if ip != "" {
		return ip, 22
	}
	return "", 0
}

// sshTunnelClient returns a shared SSH connection to the guest, dialling one
// if the cache is empty. Connections are keyed by VM and live until the guest
// stops answering on them.
func (m *Manager) sshTunnelClient(ctx context.Context, vmName, addr, user string, key []byte) (*sshExecClient, error) {
	m.sshTunnelMu.Lock()
	if c, ok := m.sshTunnelClients[vmName]; ok && c != nil {
		m.sshTunnelMu.Unlock()
		return c, nil
	}
	m.sshTunnelMu.Unlock()

	// Dial outside the lock so a slow handshake does not stall other VMs.
	cl, err := dialSSH(ctx, addr, user, key, 15*time.Second)
	if err != nil {
		return nil, err
	}

	m.sshTunnelMu.Lock()
	defer m.sshTunnelMu.Unlock()
	if m.sshTunnelClients == nil {
		m.sshTunnelClients = map[string]*sshExecClient{}
	}
	// Another connection may have raced us here; keep the first and discard
	// the duplicate rather than leaking it.
	if c, ok := m.sshTunnelClients[vmName]; ok && c != nil {
		_ = cl.Close()
		return c, nil
	}
	m.sshTunnelClients[vmName] = cl
	return cl, nil
}

// dropSSHTunnelClient evicts a dead connection, but only if it is still the
// cached one — a concurrent caller may already have replaced it.
func (m *Manager) dropSSHTunnelClient(vmName string, cl *sshExecClient) {
	m.sshTunnelMu.Lock()
	if c, ok := m.sshTunnelClients[vmName]; ok && c == cl {
		delete(m.sshTunnelClients, vmName)
	}
	m.sshTunnelMu.Unlock()
	_ = cl.Close()
}
