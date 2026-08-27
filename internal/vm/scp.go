package vm

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// CopySpec describes one host↔guest file or directory copy.
type CopySpec struct {
	VMName    string
	GuestPath string // path inside the guest; "" means the login user's home
	HostPath  string
	ToGuest   bool // direction: true = host→guest, false = guest→host
	Recursive bool
	User      string // login account override; default VMConfig.SSHUsername()
}

// CopyPath copies a file or directory between the host and a running guest by
// shelling out to scp, the same way backup shells out to rsync: recursion,
// permissions, and Windows guests (whose sshd serves SFTP but has no POSIX
// shell) all come for free with the system binary. stdout/stderr receive
// scp's own output so an interactive caller keeps the progress meter.
func (m *Manager) CopyPath(ctx context.Context, spec CopySpec, stdout, stderr io.Writer) error {
	cfg, err := m.LoadConfig(spec.VMName)
	if err != nil {
		return fmt.Errorf("VM %q not found: %w", spec.VMName, err)
	}
	state, err := m.LoadState(spec.VMName)
	if err != nil {
		return err
	}
	if state == nil || !state.Running {
		return fmt.Errorf("VM %q is not running", spec.VMName)
	}

	user := spec.User
	if user == "" {
		user = cfg.SSHUsername()
	}
	if user == "" && cfg.Template == "truenas" {
		user = cfg.TrueNASUser
	}

	host, port, err := m.guestSSHEndpoint(ctx, cfg, state)
	if err != nil {
		return err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	identity := filepath.Join(home, ".vee", "ssh", "id_ed25519")
	if _, statErr := os.Stat(identity); statErr != nil {
		identity = "" // fall back to ssh-agent / default keys
	}
	knownHosts := filepath.Join(home, ".vee", "ssh", "known_hosts")
	ScrubKnownHost(knownHosts, host, port)

	args := buildScpArgs(user, host, port, identity, knownHosts, spec, cfg.WindowsGuest())

	scpBin, err := exec.LookPath("scp")
	if err != nil {
		return fmt.Errorf("scp not found in PATH: %w", err)
	}
	cmd := exec.CommandContext(ctx, scpBin, args...) //nolint:gosec // G204: scpBin comes from LookPath; args are built from vee-managed config, not untrusted input.
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("scp: %w", err)
	}
	return nil
}

// guestSSHEndpoint resolves the host:port of the guest's sshd, in the same order
// backup resolves it: persisted ssh_host override, forwarded loopback port,
// then the guest's own address by MAC (with a QGA fallback, since freshly
// booted guests may not be in the lease/neighbour tables yet).
func (m *Manager) guestSSHEndpoint(ctx context.Context, cfg *VMConfig, state *VMState) (string, int, error) {
	switch {
	case cfg.SSHHost != "":
		return splitSSHHostPort(cfg.SSHHost)
	case state.SSHPort > 0:
		return "127.0.0.1", state.SSHPort, nil
	case cfg.NIC.MAC != "":
		ip, err := ResolveIPFromMAC(cfg.NIC.MAC)
		if err != nil && state.QGASocket != "" {
			ip, err = ResolveIPFromQGA(ctx, state.QGASocket)
		}
		if err != nil {
			return "", 0, fmt.Errorf("could not resolve IP for VM %q (MAC %s): %w", cfg.Name, cfg.NIC.MAC, err)
		}
		return ip, 22, nil
	default:
		return "", 0, fmt.Errorf("VM %q has no SSH port and no recorded MAC address; check --ssh-port or --nic-mode", cfg.Name)
	}
}

// splitSSHHostPort splits "host", "host:port", or "[host]:port"; the port
// defaults to 22.
func splitSSHHostPort(s string) (string, int, error) {
	h, p, err := net.SplitHostPort(s)
	if err != nil {
		return s, 22, nil //nolint:nilerr // missing port is not an error; default to 22.
	}
	port, err := strconv.Atoi(p)
	if err != nil || port <= 0 {
		return "", 0, fmt.Errorf("invalid port %q in ssh_host %q", p, s)
	}
	return h, port, nil
}

// buildScpArgs renders the scp argv for a copy. scp is exec'd directly (no
// host shell), so only scp's own remote-vs-local parsing needs care: a local
// path containing a colon is prefixed with ./ so scp does not read it as
// host:path. Windows guest paths are normalized to forward slashes — the
// guest's SFTP server rejects backslashed paths, and backslash is not a legal
// filename character on Windows.
func buildScpArgs(user, host string, port int, identity, knownHosts string, spec CopySpec, windowsGuest bool) []string {
	args := []string{"-o", "BatchMode=yes"}
	if knownHosts != "" {
		args = append(args,
			"-o", "UserKnownHostsFile="+knownHosts,
			"-o", "StrictHostKeyChecking=accept-new",
		)
	}
	if port != 22 {
		args = append(args, "-P", strconv.Itoa(port)) // scp's port flag is -P, unlike ssh's -p
	}
	if identity != "" {
		args = append(args, "-i", identity)
	}
	if spec.Recursive {
		args = append(args, "-r")
	}

	guestPath := spec.GuestPath
	if windowsGuest {
		guestPath = strings.ReplaceAll(guestPath, `\`, "/")
	}
	remote := host
	if user != "" {
		remote = user + "@" + host
	}
	remote += ":" + guestPath

	local := spec.HostPath
	if !filepath.IsAbs(local) && strings.Contains(local, ":") {
		local = "./" + local
	}

	if spec.ToGuest {
		return append(args, local, remote)
	}
	return append(args, remote, local)
}

// ScrubKnownHost removes all lines matching host (and [host]:port for non-22
// ports) from the vee-managed known_hosts file. Called before every connect so
// a reinstalled VM with the same IP never blocks with a host-key-changed error
// — vee tracks VM identity, so a changed host key always means reinstall,
// never MITM.
func ScrubKnownHost(knownHostsPath, host string, port int) {
	data, err := os.ReadFile(knownHostsPath) //nolint:gosec // knownHostsPath is the fixed vee-managed ~/.vee/ssh/known_hosts.
	if err != nil {
		return
	}

	// The key pattern to drop: bare host for port 22, bracketed [host]:port
	// for any other port.
	drop := host
	if port != 22 {
		drop = fmt.Sprintf("[%s]:%d", host, port)
	}

	var kept []string
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := scanner.Text()
		// Each known_hosts line starts with the host pattern (possibly
		// comma-separated), then a space, then the key type and key.
		fields := strings.Fields(line)
		if len(fields) == 0 {
			kept = append(kept, line)
			continue
		}
		if !slices.Contains(strings.Split(fields[0], ","), drop) {
			kept = append(kept, line)
		}
	}

	out := strings.Join(kept, "\n")
	if len(kept) > 0 {
		out += "\n"
	}
	//nolint:gosec // knownHostsPath is the fixed vee-managed ~/.vee/ssh/known_hosts.
	_ = os.WriteFile(knownHostsPath, []byte(out), 0o600)
}
