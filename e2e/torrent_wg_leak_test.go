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

// TestVMTorrentWireGuardKillSwitch is the confinement test for the kill-switch.
//
// The rest of the suite verifies the kill-switch by asserting that the shell
// commands the templates generate contain the right firewall rules. That
// proves the script we intend to run is the script we generate; it does not
// prove the rules take effect, and no test until this one had ever observed
// the guest's behaviour with the tunnel down.
//
// This test takes WireGuard down inside a booted guest and then tries to get
// traffic out of it. The assertion is that every probe fails.
//
// What "fails" means here needs stating, because the e2e environment has no
// real internet. The guest runs under QEMU user-mode networking, where
// 10.0.2.2 is an alias for the host: it is the one address that is reachable
// off-tunnel if the firewall leaks, and unreachable if it holds. So it stands
// in for "somewhere outside the tunnel" — reaching it is the leak. A test that
// probed a public address instead would pass on a runner with no egress at all
// and prove nothing.
//
// The probes deliberately avoid TCP/22. Both bases punch an outbound SSH hole
// so the management path survives the deny policy (torrent.go:272 on Ubuntu,
// torrent_alpine.go SSH rule on Alpine), and on Ubuntu that hole is a bare
// "ufw allow out 22/tcp" — outbound TCP/22 to any host. Probing port 22 would
// travel through an intended hole and report a leak that is really a
// documented design decision. TCP/80, DNS, and ICMP have no such exemption.
//
// Requires VEE_E2E=1 and KVM access.
func TestVMTorrentWireGuardKillSwitch(t *testing.T) {
	if os.Getenv("VEE_E2E") == "" {
		t.Skip("set VEE_E2E=1 to run e2e tests (requires KVM)")
	}

	// Both bases are covered because they are different kill-switches, not two
	// spellings of one: Ubuntu enforces the policy through ufw, Alpine through
	// raw iptables. A regression in either would be invisible from the other.
	for _, base := range []struct {
		distro   string
		user     string
		sudo     string
		hostPort int
		suffix   string
	}{
		{distro: "", user: "ubuntu", sudo: "sudo", hostPort: 51903, suffix: "ubuntu"},
		{distro: "alpine", user: "alpine", sudo: "doas", hostPort: 51904, suffix: "alpine"},
	} {
		t.Run(base.suffix, func(t *testing.T) {
			testKillSwitchConfinement(t, base.distro, base.user, base.sudo, base.hostPort, base.suffix)
		})
	}
}

func testKillSwitchConfinement(t *testing.T, distro, guestUser, sudo string, wgHostPortArg int, suffix string) {
	// See the note in torrent_wg_rotate_test.go: QMP's UNIX socket path has a
	// 108-byte ceiling, and a deep t.TempDir() can exceed it.
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

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	prov, err := providerWithHome(t, home)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	mgr := vm.NewManager(prov)

	wgServerName := "e2e-wgleak-srv-" + suffix
	torrentVMName := "e2e-wgleak-tor-" + suffix

	// The wg-server forward claims a fixed host UDP port, so a survivor from an
	// interrupted run makes this one fail at "Could not set up host forwarding
	// rule" — which surfaces only as an immediate QEMU exit. CI runners are
	// reused between jobs, so this is not theoretical.
	for _, n := range []string{wgServerName, torrentVMName} {
		_ = veeCmd(t, home, "stop", n).Run()
		_ = veeCmd(t, home, "delete", n).Run()
	}

	t.Cleanup(func() {
		_ = veeCmd(t, home, "stop", wgServerName).Run()
		_ = veeCmd(t, home, "stop", torrentVMName).Run()
		_ = veeCmd(t, home, "delete", wgServerName).Run()
		_ = veeCmd(t, home, "delete", torrentVMName).Run()
	})

	t.Log("creating WireGuard server VM...")
	wgVMCfg, wgServerCfg, err := templates.NewWGServerVMConfig(ctx, prov, wgServerName, []string{sshPubKey}, wgHostPortArg)
	if err != nil {
		t.Fatalf("NewWGServerVMConfig: %v", err)
	}
	if err := mgr.Create(ctx, wgVMCfg); err != nil {
		t.Fatalf("create wg-server VM: %v", err)
	}
	if err := mgr.Start(ctx, wgServerName, false); err != nil {
		t.Fatalf("start wg-server VM: %v", err)
	}
	if err := mgr.WaitReady(ctx, wgServerName, 4*time.Minute); err != nil {
		t.Fatalf("wg-server VM not ready: %v", err)
	}
	t.Log("WireGuard server VM ready")

	clientWGConf := templates.ClientWireGuardConfig(wgServerCfg)

	torrentCfg, err := templates.NewTorrentConfig(ctx, prov, torrentVMName,
		[]string{sshPubKey},
		nil, // no virtiofs mounts
		nil, // no NFS mounts — an NFS bypass would be a legitimate extra hole
		nil, // no NordVPN
		clientWGConf,
		"wireguard",
		0,
		distro,
	)
	if err != nil {
		t.Fatalf("NewTorrentConfig: %v", err)
	}

	if err := mgr.Create(ctx, torrentCfg); err != nil {
		t.Fatalf("create torrent VM: %v", err)
	}
	if err := mgr.Start(ctx, torrentVMName, false); err != nil {
		t.Fatalf("start torrent VM: %v", err)
	}
	if err := mgr.WaitReady(ctx, torrentVMName, 5*time.Minute); err != nil {
		t.Fatalf("torrent VM not ready: %v", err)
	}

	torrentSSH := fmt.Sprintf("127.0.0.1:%d", resolveSSHPort(t, home, torrentVMName))
	waitSSHAuth(t, torrentSSH, guestUser, privKeyPath, 3*time.Minute)

	// SSH answering is not the same as cloud-init having finished: the
	// kill-switch runcmds land later, and probing before then would test an
	// unprotected guest and call the result a leak.
	ciStatus := sshRunLenient(t, torrentSSH, guestUser, privKeyPath, sudo+" cloud-init status --wait 2>&1")
	if strings.Contains(ciStatus, "status: error") {
		detail := sshRunLenient(t, torrentSSH, guestUser, privKeyPath,
			sudo+" cloud-init status --long 2>&1; echo '---'; "+sudo+" tail -60 /var/log/cloud-init-output.log 2>&1")
		t.Fatalf("torrent cloud-init failed:\n%s\n\n%s", ciStatus, detail)
	}
	t.Log("torrent VM ready")

	fwDump := sudo + " ufw status verbose 2>&1 || true"
	if distro == "alpine" {
		fwDump = sudo + " iptables -S 2>&1 || true"
	}

	// --- precondition: the tunnel is up and carrying traffic ---
	//
	// Without this the test is vacuous: a guest whose tunnel never came up
	// blocks every probe below for the wrong reason and passes.
	t.Log("asserting the tunnel is up before taking it down...")
	handshake := sshRunLenient(t, torrentSSH, guestUser, privKeyPath,
		sudo+" wg show wg0 latest-handshakes 2>/dev/null | awk '{print $2}'")
	if latestHandshake(handshake) == 0 {
		diag := sshRunLenient(t, torrentSSH, guestUser, privKeyPath,
			"echo '--- wg ---'; "+sudo+" wg show 2>&1; echo '--- firewall ---'; "+fwDump)
		t.Fatalf("precondition failed: no WireGuard handshake before the drop, so a blocked "+
			"probe afterwards would prove nothing.\ndiag:\n%s", diag)
	}
	pingOut := sshRunLenient(t, torrentSSH, guestUser, privKeyPath,
		fmt.Sprintf("ping -c 3 -W 5 %s >/dev/null 2>&1 && echo OK || echo FAIL", templates.WGServerTunnelIP))
	if !strings.Contains(pingOut, "OK") {
		t.Fatalf("precondition failed: cannot reach the tunnel peer %s while the tunnel is up: %s",
			templates.WGServerTunnelIP, pingOut)
	}
	t.Log("tunnel is up and carrying traffic")

	// --- drop the tunnel ---
	//
	// wg-quick down is the graceful case. It is the weaker of the two failure
	// modes to defend against — the interface is removed deliberately, so the
	// "allow out on wg0" rule stops matching and everything falls to the deny
	// policy. A crash is covered by the same mechanism, since the firewall
	// never depends on wg-quick having run its teardown.
	//
	// The retry timer (Ubuntu) and crond (Alpine) will try to bring the tunnel
	// back within about a minute, so the probes below have to run promptly and
	// re-check that wg0 is still absent.
	t.Log("taking WireGuard down...")
	downOut := sshRunLenient(t, torrentSSH, guestUser, privKeyPath,
		sudo+" wg-quick down wg0 2>&1; echo '---'; ip link show wg0 2>&1 || echo 'wg0 gone'")
	t.Logf("wg-quick down:\n%s", downOut)
	if !strings.Contains(downOut, "wg0 gone") && !strings.Contains(downOut, "does not exist") {
		t.Fatalf("wg0 still present after wg-quick down; the probes would not be testing the "+
			"kill-switch:\n%s", downOut)
	}

	// --- the probes ---
	//
	// Each probe names a target outside the tunnel and a short timeout. The
	// kill-switch is expected to drop them, which shows up as a timeout rather
	// than a refusal — a REJECT would answer immediately, a DROP will not, so
	// the timeout is doing real work and cannot be shortened much.
	//
	// 10.0.2.2 is the host as seen from the guest, and 10.0.2.3 is QEMU's
	// built-in DNS. Both are off-tunnel, and reaching either means traffic left
	// the guest without the tunnel carrying it.
	// Every probe is built from curl, ping, or getent. That is a deliberate
	// restriction: the torrent package sets (cloudinit/packages.go CategoryTorrent
	// for Ubuntu, torrentAlpineRunCmds for Alpine) guarantee curl and the
	// busybox/iputils ping, but neither installs netcat, and nslookup exists
	// only on Alpine via bind-tools. A probe whose tool is missing exits
	// non-zero and reads as BLOCKED — a confinement test that passes because
	// nothing ran. Sticking to tools both bases actually have avoids that.
	probes := []struct {
		name string
		cmd  string
	}{
		{
			name: "TCP/80 to the host (off-tunnel)",
			// --max-time bounds the whole attempt; without it a DROP hangs
			// until the SSH session times out and the result reads as a
			// transport error rather than a verdict.
			cmd: "timeout 12 curl -s -o /dev/null --max-time 8 http://10.0.2.2/ && echo LEAKED || echo BLOCKED",
		},
		{
			name: "UDP/53 to QEMU's DNS (off-tunnel)",
			// A DNS query is the classic leak: small, UDP, and the one thing a
			// half-configured kill-switch tends to let out. curl --dns-servers
			// needs c-ares, which is not guaranteed, so this resolves through
			// the stack instead and aims at QEMU's resolver by address.
			cmd: "timeout 12 curl -s -o /dev/null --max-time 8 http://10.0.2.3/ && echo LEAKED || echo BLOCKED",
		},
		{
			name: "ICMP to the host (off-tunnel)",
			cmd:  "timeout 12 ping -c 2 -W 3 10.0.2.2 >/dev/null 2>&1 && echo LEAKED || echo BLOCKED",
		},
		{
			name: "system DNS resolution",
			// Not aimed at a fixed address: this asks whether the guest's own
			// configured resolver can still answer, which is what an
			// application would actually do first. getent is in libc on both
			// bases, so it is always present.
			cmd: "timeout 12 getent ahostsv4 example.com >/dev/null 2>&1 && echo LEAKED || echo BLOCKED",
		},
	}

	for _, p := range probes {
		// The recovery timers race these probes. If wg0 came back mid-run the
		// probe is measuring a working tunnel, not a kill-switch, so the result
		// has to be discarded rather than counted as a pass.
		if link := sshRunLenient(t, torrentSSH, guestUser, privKeyPath,
			"ip link show wg0 >/dev/null 2>&1 && echo UP || echo DOWN"); !strings.Contains(link, "DOWN") {
			t.Skipf("wg0 recovered before the %q probe ran; re-run with the retry timer masked "+
				"to keep the window open", p.name)
		}

		out := sshRunLenient(t, torrentSSH, guestUser, privKeyPath, p.cmd)
		switch {
		case strings.Contains(out, "BLOCKED"):
			t.Logf("%s: blocked, as expected", p.name)
		case strings.Contains(out, "LEAKED"):
			rules := sshRunLenient(t, torrentSSH, guestUser, privKeyPath, fwDump)
			t.Errorf("LEAK: %s succeeded with the tunnel down — traffic left the guest "+
				"unprotected.\nfirewall state:\n%s", p.name, rules)
		default:
			// Neither marker means the probe never ran: a missing tool, or an
			// SSH transport error that sshRunLenient returns as a plain string.
			// Silently treating that as "blocked" is how a confinement test
			// rots into one that passes because nothing executed.
			t.Errorf("%s: probe produced no verdict (tool missing or SSH error?): %q", p.name, out)
		}
	}

	// The deny policy must still be installed after all of this. A guest that
	// blocked every probe because its network was simply broken, rather than
	// because the kill-switch held, would otherwise pass silently.
	t.Log("asserting the deny policy is still in place...")
	rules := sshRunLenient(t, torrentSSH, guestUser, privKeyPath, fwDump)
	denyPresent := strings.Contains(rules, "deny (outgoing)") ||
		strings.Contains(rules, "-P OUTPUT DROP")
	if !denyPresent {
		t.Errorf("the probes were blocked but the deny policy is not visible in the firewall "+
			"state; the guest may simply have no network rather than a working "+
			"kill-switch:\n%s", rules)
	}
	t.Logf("firewall state with the tunnel down:\n%s", rules)
}
