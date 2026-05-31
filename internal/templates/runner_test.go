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
}
