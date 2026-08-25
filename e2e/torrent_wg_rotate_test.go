//go:build e2e

package e2e_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Benehiko/vee/internal/templates"
	"github.com/Benehiko/vee/internal/vm"
)

// TestVMTorrentWireGuardEndpointRotation covers the case a literal-IP endpoint
// cannot reach: a config naming a *hostname* whose address changes.
//
// The kill-switch pins the handshake hole to the endpoint's resolved addresses,
// because once the deny policy is up there is no DNS left to resolve a hostname
// with. Those addresses are written to /etc/wireguard/endpoint-addrs at build
// time. If nothing ever recomputes them, a provider that re-addresses its
// server leaves wg-quick chasing the new IP while the firewall still permits
// only the old one: the handshake is dropped, the tunnel never comes up, and it
// stays that way across reboots. It fails closed, so nothing leaks, but it is a
// silent permanent outage.
//
// The rotation is simulated with /etc/hosts rather than real DNS, which is what
// makes this runnable at all: the endpoint hostname is repointed from a
// blackhole address to the address the tunnel actually works on. A guest that
// re-resolves recovers; a guest that pins once and never looks again does not.
//
// Requires VEE_E2E=1 and KVM access.
func TestVMTorrentWireGuardEndpointRotation(t *testing.T) {
	if os.Getenv("VEE_E2E") == "" {
		t.Skip("set VEE_E2E=1 to run e2e tests (requires KVM)")
	}

	// Both bases pin the handshake hole, and both had the same stale-pin bug;
	// they just express the firewall differently (ufw vs iptables) and recover
	// on different schedules (a systemd timer vs a boot hook plus crond).
	for _, base := range []struct {
		distro   string
		user     string
		sudo     string
		hostPort int
		suffix   string
	}{
		{distro: "", user: "ubuntu", sudo: "sudo", hostPort: 51901, suffix: "ubuntu"},
		{distro: "alpine", user: "alpine", sudo: "doas", hostPort: 51902, suffix: "alpine"},
	} {
		t.Run(base.suffix, func(t *testing.T) {
			testEndpointRotation(t, base.distro, base.user, base.sudo, base.hostPort, base.suffix)
		})
	}
}

func testEndpointRotation(t *testing.T, distro, guestUser, sudo string, wgHostPortArg int, suffix string) {
	// t.TempDir() paths are long, and QEMU's QMP socket lives under the home:
	// a UNIX socket path over 108 bytes is rejected outright ("Path must be
	// less than 108 bytes") and the VM never starts. VEE_E2E_HOME lets a run
	// point at a short path like /tmp/vee-e2e when the default is too deep.
	home := t.TempDir()
	if h := os.Getenv("VEE_E2E_HOME"); h != "" {
		home = h
	}
	privKeyPath := veePrivKeyPath(t, home)
	pubKeyBytes, err := os.ReadFile(privKeyPath + ".pub")
	if err != nil {
		t.Fatalf("read public key: %v", err)
	}
	sshPubKey := strings.TrimSpace(string(pubKeyBytes))

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()

	prov, err := providerWithHome(t, home)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	mgr := vm.NewManager(prov)

	wgServerName := "e2e-wgrot-srv-" + suffix
	torrentVMName := "e2e-wgrot-tor-" + suffix
	wgHostPort := wgHostPortArg

	const (
		// The endpoint the guest's wg0.conf names. Resolved through
		// /etc/hosts inside the guest, so the test controls what it maps to.
		endpointHost = "vpn.e2e.invalid"
		// Where the hostname points at build time: a reserved TEST-NET-1
		// address that nothing answers on, standing in for the provider's
		// old IP. The handshake hole is pinned here.
		staleAddr = "192.0.2.77"
	)

	t.Cleanup(func() {
		_ = veeCmd(t, home, "stop", wgServerName).Run()
		_ = veeCmd(t, home, "stop", torrentVMName).Run()
		_ = veeCmd(t, home, "delete", wgServerName).Run()
		_ = veeCmd(t, home, "delete", torrentVMName).Run()
	})

	t.Log("creating WireGuard server VM...")
	wgVMCfg, wgServerCfg, err := templates.NewWGServerVMConfig(ctx, prov, wgServerName, []string{sshPubKey}, wgHostPort)
	if err != nil {
		t.Fatalf("NewWGServerVMConfig: %v", err)
	}
	if err := mgr.Create(ctx, wgVMCfg); err != nil {
		t.Fatalf("create wg-server VM: %v", err)
	}
	if err := mgr.Start(ctx, wgServerName, false); err != nil {
		t.Fatalf("start wg-server VM: %v", err)
	}
	if err := mgr.WaitReady(ctx, wgServerName, 20*time.Minute); err != nil {
		t.Fatalf("wg-server VM not ready: %v", err)
	}
	t.Log("WireGuard server VM ready")

	// The client config names the hostname, not 127.0.0.1 — that is the whole
	// point. The guest reaches the server over the host-forwarded UDP port, so
	// the address the hostname must eventually resolve to is 10.0.2.2, QEMU's
	// alias for the host as seen from a user-mode guest.
	clientWGConf := templates.ClientWireGuardConfig(wgServerCfg)
	clientWGConf.Endpoint = fmt.Sprintf("%s:%d", endpointHost, wgHostPort)

	torrentCfg, err := templates.NewTorrentConfig(ctx, prov, torrentVMName,
		[]string{sshPubKey},
		nil, // no virtiofs mounts
		nil, // no NFS mounts
		nil, // no NordVPN
		clientWGConf,
		"wireguard",
		0,
		distro,
	)
	if err != nil {
		t.Fatalf("NewTorrentConfig: %v", err)
	}

	// Seed /etc/hosts with the stale address before cloud-init runs, so the
	// build-time resolve pins the handshake hole to an address the tunnel can
	// never be established through.
	torrentCfg.CloudInit.WriteFiles = append(torrentCfg.CloudInit.WriteFiles,
		vm.CloudInitWriteFile{
			Path:        "/etc/hosts",
			Content:     fmt.Sprintf("127.0.0.1 localhost\n%s %s\n", staleAddr, endpointHost),
			Permissions: "0644",
		})

	if err := mgr.Create(ctx, torrentCfg); err != nil {
		t.Fatalf("create torrent VM: %v", err)
	}
	if err := mgr.Start(ctx, torrentVMName, false); err != nil {
		t.Fatalf("start torrent VM: %v", err)
	}
	if err := mgr.WaitReady(ctx, torrentVMName, 20*time.Minute); err != nil {
		t.Fatalf("torrent VM not ready: %v", err)
	}

	torrentSSH := fmt.Sprintf("127.0.0.1:%d", resolveSSHPort(t, home, torrentVMName))
	waitSSHAuth(t, torrentSSH, guestUser, privKeyPath, 15*time.Minute)

	// SSH answering is not the same as cloud-init having finished: the
	// kill-switch runcmds, and the endpoint-addrs file they write, land later.
	// Reading the file before then races the very setup under test.
	ciStatus := sshRunLenient(t, torrentSSH, guestUser, privKeyPath, sudo+" cloud-init status --wait 2>&1")
	if strings.Contains(ciStatus, "status: error") {
		detail := sshRunLenient(t, torrentSSH, guestUser, privKeyPath,
			sudo+" cloud-init status --long 2>&1; echo '---'; "+sudo+" tail -60 /var/log/cloud-init-output.log 2>&1")
		t.Fatalf("torrent cloud-init failed:\n%s\n\n%s", ciStatus, detail)
	}
	t.Log("torrent VM ready")

	// The two bases express the pinned hole differently, so the dump used for
	// assertions and diagnostics has to follow the base in use.
	fwDump := sudo + " ufw status | grep -i udp || true"
	if distro == "alpine" {
		fwDump = sudo + " iptables -S OUTPUT | grep -i udp || true"
	}

	// The tunnel must be down: the endpoint resolves to a blackhole.
	pinned := sshRun(t, torrentSSH, guestUser, privKeyPath, sudo+" cat /etc/wireguard/endpoint-addrs")
	if !strings.Contains(pinned, staleAddr) {
		t.Fatalf("precondition failed: handshake hole should be pinned to %s, got %q", staleAddr, pinned)
	}
	t.Logf("handshake hole pinned to the stale address: %s", strings.TrimSpace(pinned))

	// --- the rotation ---
	t.Log("rotating the endpoint address (provider re-addresses the server)...")
	sshRun(t, torrentSSH, guestUser, privKeyPath, fmt.Sprintf(
		"%s sed -i 's/^%s /10.0.2.2 /' /etc/hosts && grep %s /etc/hosts",
		sudo, staleAddr, endpointHost))

	// Give the retry timer several cycles to notice and recover. It fires every
	// 60s; a guest that re-resolves picks the new address up here.
	t.Log("waiting for the guest to recover the tunnel...")
	var lastState string
	deadline := time.Now().Add(6 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(30 * time.Second)
		lastState = sshRunLenient(t, torrentSSH, guestUser, privKeyPath,
			sudo+" wg show wg0 latest-handshakes 2>/dev/null | awk '{print $2}'")
		if h := strings.TrimSpace(lastState); h != "" && h != "0" {
			t.Logf("tunnel recovered: latest handshake %s", h)

			addrs := sshRun(t, torrentSSH, guestUser, privKeyPath, sudo+" cat /etc/wireguard/endpoint-addrs")
			if strings.Contains(addrs, staleAddr) {
				t.Errorf("endpoint-addrs still names the stale address after recovery: %q", addrs)
			}
			rules := sshRunLenient(t, torrentSSH, guestUser, privKeyPath, fwDump)
			t.Logf("handshake hole after rotation:\n%s", rules)
			return
		}
	}

	diag := sshRunLenient(t, torrentSSH, guestUser, privKeyPath,
		"echo '--- endpoint-addrs ---'; "+sudo+" cat /etc/wireguard/endpoint-addrs; "+
			"echo '--- firewall ---'; "+fwDump+"; "+
			"echo '--- resolved now ---'; getent ahostsv4 "+endpointHost+" | awk '{print $1}' | sort -u; "+
			"echo '--- wg ---'; "+sudo+" wg show")
	t.Fatalf("tunnel never came up after the endpoint rotated: the handshake hole is still "+
		"pinned to the old address and nothing re-resolves it.\ndiag:\n%s", diag)
}
