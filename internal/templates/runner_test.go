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
	var gcScript string
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
			haveGCTimer = true
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
	// GC must derive the rootless env from its own UID (runner.env is
	// root-owned 0600 and unreadable by the runner user GC runs as) and refuse
	// to run during a job.
	if !strings.Contains(gcScript, "Runner.Worker") {
		t.Error("GC script does not guard against in-progress jobs (Runner.Worker check)")
	}
	if strings.Contains(gcScript, ". /etc/actions-runner/runner.env") {
		t.Error("GC script sources root-owned runner.env — must derive env from id -u instead")
	}
	if !strings.Contains(gcScript, "XDG_RUNTIME_DIR=\"/run/user/${RUNNER_UID}\"") {
		t.Error("GC script does not derive XDG_RUNTIME_DIR from its own UID")
	}
	if !strings.Contains(gcScript, "nerdctl system prune") {
		t.Error("GC script does not prune nerdctl")
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
