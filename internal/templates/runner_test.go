package templates

import (
	"strings"
	"testing"
)

// TestGitHubRunnerCloudInit guards the regression where the runner user's UID
// was pinned to 1001. cloud-init assigns the distro default user (ubuntu) UID
// 1000 and the custom admin user UID 1001, so a hardcoded 1001 collided and
// useradd aborted — the runner user was never created, registration never ran,
// and the runner never appeared on GitHub. The runner must take the next free
// UID and derive its rootless paths from $(id -u runner) at boot.
func TestGitHubRunnerCloudInit(t *testing.T) {
	files, runs := githubRunnerCloudInit(
		"https://github.com/Benehiko/ares",
		"TESTTOKEN",
		"ares-ci",
		nil,
		nil,
		nil,
	)

	if len(files) == 0 {
		t.Fatal("no write_files generated")
	}
	if len(runs) == 0 {
		t.Fatal("no runcmd generated")
	}

	joined := strings.Join(runs, "\n")

	// The runner user must be created without a pinned UID.
	if !strings.Contains(joined, "useradd --create-home --shell /bin/bash runner") {
		t.Error("runcmd missing UID-less useradd for runner")
	}
	if strings.Contains(joined, "--uid 1001") {
		t.Error("runcmd pins runner UID to 1001 — collides with the admin user")
	}

	// No rootless path may embed a literal /run/user/1001; everything must go
	// through the runtime-resolved ${RUNNER_UID}.
	if strings.Contains(joined, "/run/user/1001") {
		t.Error("runcmd contains literal /run/user/1001 — must use ${RUNNER_UID}")
	}
	if !strings.Contains(joined, "RUNNER_UID=$(id -u runner)") {
		t.Error("runcmd never resolves RUNNER_UID from id -u runner")
	}

	// The UID-dependent env must be appended to runner.env at boot, and the
	// shipped runner.env must NOT carry a stale XDG_RUNTIME_DIR / socket paths.
	if !strings.Contains(joined, ">> /etc/actions-runner/runner.env") {
		t.Error("runcmd never appends UID-derived vars to runner.env")
	}
	var runnerEnv string
	for _, f := range files {
		if f.Path == "/etc/actions-runner/runner.env" {
			runnerEnv = f.Content
		}
	}
	if runnerEnv == "" {
		t.Fatal("no /etc/actions-runner/runner.env write_file")
	}
	for _, stale := range []string{"XDG_RUNTIME_DIR", "CONTAINERD_ADDRESS", "BUILDKIT_HOST", "/run/user/"} {
		if strings.Contains(runnerEnv, stale) {
			t.Errorf("runner.env ships stale UID-dependent var %q; must be appended at boot", stale)
		}
	}
	for _, want := range []string{"RUNNER_URL=", "RUNNER_TOKEN=TESTTOKEN", "RUNNER_NAME=ares-ci"} {
		if !strings.Contains(runnerEnv, want) {
			t.Errorf("runner.env missing %q", want)
		}
	}

	// Registration must still run after the user and stack are set up.
	if !strings.Contains(joined, "config.sh --unattended") {
		t.Error("runcmd missing GitHub runner registration step")
	}

	// No leaked Sprintf escapes in any runcmd.
	for _, bad := range []string{"%!", "%(MISSING)"} {
		if strings.Contains(joined, bad) {
			t.Errorf("runcmd contains stray %q (Sprintf escape leaked)", bad)
		}
	}

	// Empty labels must default to the standard self-hosted set.
	if !strings.Contains(runnerEnv, "RUNNER_LABELS=self-hosted,linux,kvm") {
		t.Errorf("default labels not applied: %q", runnerEnv)
	}

	// The disk GC timer must be installed and enabled — without it CI build
	// leftovers fill the disk and the runner crash-loops offline.
	if !strings.Contains(joined, "systemctl enable --now vee-runner-gc.timer") {
		t.Error("runcmd missing vee-runner-gc.timer enable")
	}
	var gcScript, gcTimer, journaldConf, sudoersConf string
	haveGCService, haveGCTimer := false, false
	for _, f := range files {
		switch f.Path {
		case "/usr/local/bin/vee-runner-gc.sh":
			gcScript = f.Content
			if f.Permissions != "0755" {
				t.Errorf("vee-runner-gc.sh must be executable (0755), got %q", f.Permissions)
			}
		case "/etc/systemd/system/vee-runner-gc.service":
			haveGCService = true
		case "/etc/systemd/system/vee-runner-gc.timer":
			gcTimer = f.Content
			haveGCTimer = true
		case "/etc/systemd/journald.conf.d/vee-runner.conf":
			journaldConf = f.Content
		case "/etc/sudoers.d/vee-runner-gc":
			sudoersConf = f.Content
			// sudo silently ignores any sudoers file that is group- or
			// world-writable, which would make the journal vacuum fail.
			if f.Permissions != "0440" {
				t.Errorf("sudoers drop-in must be 0440, got %q", f.Permissions)
			}
		}
	}
	if gcScript == "" {
		t.Fatal("no /usr/local/bin/vee-runner-gc.sh write_file")
	}
	if !haveGCService {
		t.Error("no vee-runner-gc.service write_file")
	}
	if !haveGCTimer {
		t.Error("no vee-runner-gc.timer write_file")
	}
	// GC must derive the rootless env from its own UID: runner.env is root-owned
	// 0600 and unreadable by the runner user GC runs as.
	if strings.Contains(gcScript, ". /etc/actions-runner/runner.env") {
		t.Error("GC script sources root-owned runner.env — must derive env from id -u instead")
	}
	if !strings.Contains(gcScript, "XDG_RUNTIME_DIR=\"/run/user/${RUNNER_UID}\"") {
		t.Error("GC script does not derive XDG_RUNTIME_DIR from its own UID")
	}
	if !strings.Contains(gcScript, "nerdctl system prune") {
		t.Error("GC script does not prune nerdctl")
	}
	// The whole GC run must NOT be skipped when a job is in progress — that guard
	// let orphaned stacks accumulate until the disk filled. Every destructive
	// action is age-gated instead, so nothing needs an early `exit 0`.
	if strings.Contains(gcScript, "job in progress, skipping") {
		t.Error("GC script still skips the whole run when a job is in progress — reap must be age-gated instead")
	}
	// Stale containers left "Up" by a canceled/crashed job are the primary
	// disk-growth source and nerdctl's own prune never reaps them; GC must
	// force-remove containers past an age ceiling even while running.
	if !strings.Contains(gcScript, "ORPHAN_MAX_AGE_SEC") {
		t.Error("GC script does not age-reap orphaned containers (no ORPHAN_MAX_AGE_SEC)")
	}
	if !strings.Contains(gcScript, "nerdctl rm -f") {
		t.Error("GC script does not force-remove stale containers")
	}
	// The go-build cache must be bounded by SIZE with an age floor, not cleared
	// wholesale. The previous design gated a full `go clean -cache` on a live
	// Runner.Worker, so the cache either grew unchecked on a busy runner or went
	// completely cold on an idle one. Age-gated LRU eviction protects a live
	// build's working set without ever discarding the whole cache, which also
	// makes the Runner.Worker check unnecessary.
	if !strings.Contains(gcScript, "GOCACHE_CEILING_MB") {
		t.Error("GC script does not bound the go-build cache by size (no GOCACHE_CEILING_MB)")
	}
	if !strings.Contains(gcScript, "GOCACHE_MIN_AGE_MIN") {
		t.Error("GC script does not age-gate go-build eviction (no GOCACHE_MIN_AGE_MIN)")
	}
	if strings.Contains(gcScript, "go clean -cache") {
		t.Error("GC script still clears the whole go-build cache instead of trimming to a ceiling")
	}
	// The age floor must be in MINUTES. A day-granularity guard is useless here:
	// a busy runner rewrites its entire cache within a single day, so nothing is
	// ever a day old and the ceiling can never be enforced (observed live: 16584
	// cache files, all stamped the same day, 0 eligible for eviction).
	if !strings.Contains(gcScript, "-mmin") {
		t.Error("go-build eviction must select candidates by minutes (-mmin), not days")
	}
	if strings.Contains(gcScript, "-mtime +1 -printf") {
		t.Error("go-build eviction uses day-granularity -mtime; a busy runner never has day-old entries")
	}
	// The journal is the one genuinely unbounded path: systemd's default ceiling
	// is 10% of the filesystem, so growing the runner disk silently raises it.
	// Vacuuming needs sudo — the runner user cannot unlink files under
	// /var/log/journal, and an unprivileged vacuum fails per-file while still
	// exiting 0, so the failure is silent.
	if !strings.Contains(gcScript, "sudo -n journalctl --vacuum-size") {
		t.Error("GC script does not vacuum the journal via sudo (unprivileged vacuum frees nothing)")
	}
	// systemd's default journal ceiling is 10% of the filesystem — a proportional
	// cap that silently grows when the runner disk is resized (20G -> 60G raised
	// it from ~1.9G to ~5.8G). Pin an absolute cap instead.
	if !strings.Contains(journaldConf, "SystemMaxUse=") {
		t.Error("journald drop-in does not cap journal size (no SystemMaxUse)")
	}
	// The cap only takes effect once journald reloads; without the restart the
	// daemon keeps running with the compiled-in 10%-of-filesystem default.
	if !strings.Contains(strings.Join(runs, "\n"), "systemctl restart systemd-journald") {
		t.Error("runcmd does not restart journald, so the size cap never takes effect")
	}
	// The vacuum in the GC script runs as the runner user and needs exactly this
	// grant; without it the vacuum fails per-file and frees nothing.
	if !strings.Contains(sudoersConf, "journalctl --vacuum-size") {
		t.Error("sudoers drop-in does not grant the journal vacuum the GC script runs")
	}
	if !strings.Contains(sudoersConf, "NOPASSWD:") {
		t.Error("sudoers grant must be NOPASSWD — the GC service runs non-interactively")
	}

	// Cadence must be hourly, not daily: a daily timer lets orphans accumulate
	// for up to 24h between runs on a busy runner.
	if !strings.Contains(gcTimer, "OnCalendar=hourly") {
		t.Errorf("GC timer must run hourly, got:\n%s", gcTimer)
	}
	if strings.Contains(gcTimer, "OnCalendar=daily") {
		t.Error("GC timer still set to daily — must be hourly")
	}
}

// TestGitHubRunnerRestore verifies that supplying restored credential files
// makes the template inject them and skip config.sh registration, so a
// recreated runner rejoins GitHub with its persisted identity rather than a
// fresh token.
func TestGitHubRunnerRestore(t *testing.T) {
	restored := []RunnerCredFile{
		{RelPath: ".credentials", Content: []byte(`{"scheme":"OAuth"}`)},
		{RelPath: ".runner", Content: []byte(`{"agentId":42}`)},
	}
	_, runs := githubRunnerCloudInit(
		"https://github.com/Benehiko/ares",
		"", // no token on restore
		"ares-ci",
		nil,
		restored,
		nil,
	)
	joined := strings.Join(runs, "\n")

	// config.sh registration must NOT run when restoring.
	if strings.Contains(joined, "config.sh --unattended") {
		t.Error("restore path still runs config.sh registration — must reuse persisted creds")
	}
	// Each restored file must be base64-decoded into /opt/actions-runner.
	for _, rc := range restored {
		want := "base64 -d > /opt/actions-runner/" + rc.RelPath
		if !strings.Contains(joined, want) {
			t.Errorf("restore path missing decode of %q (looking for %q)", rc.RelPath, want)
		}
	}
	// Restored creds must be re-owned and locked down.
	if !strings.Contains(joined, "chown -R runner:runner /opt/actions-runner") {
		t.Error("restore path missing chown to runner")
	}
}

// TestGitHubRunnerSSHKey verifies that injecting an SSH private key makes the
// template provision /home/runner/.ssh with the key, GitHub's host keys, and an
// ssh config pinning the identity — all owned by the runner user — and that no
// such provisioning happens when no key is supplied.
func TestGitHubRunnerSSHKey(t *testing.T) {
	priv := []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nFAKEKEYBYTES\n-----END OPENSSH PRIVATE KEY-----\n")
	_, runs := githubRunnerCloudInit(
		"https://github.com/Benehiko/ares",
		"TESTTOKEN",
		"ares-ci",
		nil,
		nil,
		priv,
	)
	joined := strings.Join(runs, "\n")

	// SSH dir created 0700 owned by runner before any file is dropped.
	if !strings.Contains(joined, "install -d -m 700 -o runner -g runner /home/runner/.ssh") {
		t.Error("missing 0700 runner-owned /home/runner/.ssh creation")
	}
	// Private key, known_hosts and config decoded into place.
	for _, want := range []string{
		"base64 -d > /home/runner/.ssh/id_ed25519",
		"base64 -d > /home/runner/.ssh/known_hosts",
		"base64 -d > /home/runner/.ssh/config",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("ssh setup missing %q", want)
		}
	}
	// Perms + ownership locked down.
	if !strings.Contains(joined, "chmod 600 /home/runner/.ssh/id_ed25519") {
		t.Error("private key not chmod 600")
	}
	if !strings.Contains(joined, "chown -R runner:runner /home/runner/.ssh") {
		t.Error("ssh dir not chowned to runner")
	}
	// known_hosts content must be the GitHub host keys, decodable from the
	// base64 embedded in the runcmd.
	if !strings.Contains(GitHubKnownHosts, "github.com ssh-ed25519 ") {
		t.Error("GitHubKnownHosts missing ed25519 host key")
	}

	// No key supplied -> no /home/runner/.ssh provisioning at all.
	_, noKeyRuns := githubRunnerCloudInit(
		"https://github.com/Benehiko/ares",
		"TESTTOKEN",
		"ares-ci",
		nil,
		nil,
		nil,
	)
	if strings.Contains(strings.Join(noKeyRuns, "\n"), "/home/runner/.ssh") {
		t.Error("ssh setup ran without an injected key")
	}
}

// TestGitHubRunnerNoWriteFileOwnsAsRunner guards a cloud-init ordering bug:
// write_files runs in the config stage, BEFORE runcmd creates the "runner" user.
// Any write_file with Owner "runner:..." makes the whole write_files module abort
// (getpwnam: name not found), so the unit files never land and the runner never
// starts. Ownership of /etc/actions-runner/runner.env must instead be fixed up by
// a runcmd chown after useradd. This test fails if any write_file regresses to a
// runner owner. It checks fresh, restore and no-key paths.
func TestGitHubRunnerNoWriteFileOwnsAsRunner(t *testing.T) {
	priv := []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nFAKE\n-----END OPENSSH PRIVATE KEY-----\n")
	for _, tc := range []struct {
		name     string
		restored []RunnerCredFile
		key      []byte
	}{
		{"fresh", nil, priv},
		{"restore", []RunnerCredFile{{RelPath: ".credentials", Content: []byte("x"), Mode: 0o600}}, priv},
		{"nokey", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files, runs := githubRunnerCloudInit("https://github.com/o/r", "tok", "ci-1", nil, tc.restored, tc.key)
			for _, f := range files {
				if f.Owner == "runner" || strings.HasPrefix(f.Owner, "runner:") {
					t.Fatalf("write_file %q sets Owner %q, but the runner user does not exist at write_files time; chown it in runcmd instead", f.Path, f.Owner)
				}
			}
			// runner.env must still end up runner-owned, via a runcmd chown.
			joined := strings.Join(runs, "\n")
			if !strings.Contains(joined, "chown runner:runner") || !strings.Contains(joined, "/etc/actions-runner/runner.env") {
				t.Fatalf("expected a runcmd chown of runner.env to runner, got:\n%s", joined)
			}
		})
	}
}
