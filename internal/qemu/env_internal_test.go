package qemu

import (
	"slices"
	"testing"
)

func TestProcessEnvAppendsOverrides(t *testing.T) {
	q := &BaseMachine{}
	WithEnv([]string{"RENDER_SERVER_EXEC_PATH=/r/virgl_render_server", "LD_LIBRARY_PATH=/r/lib"})(q)
	env := q.processEnv("/opt/qemu-helios/bin/qemu-system-x86_64")
	if len(env) < 2 {
		t.Fatalf("env too short: %v", env)
	}
	// Overrides come last so they win over inherited/derived values (exec
	// uses the last entry for a duplicate key).
	tail := env[len(env)-2:]
	if !slices.Equal(tail, []string{"RENDER_SERVER_EXEC_PATH=/r/virgl_render_server", "LD_LIBRARY_PATH=/r/lib"}) {
		t.Errorf("overrides not last: %v", tail)
	}
}

func TestProcessEnvNoOverrides(t *testing.T) {
	q := &BaseMachine{}
	if got, want := q.processEnv("/x/qemu"), qemuEnv("/x/qemu"); !slices.Equal(got, want) {
		t.Errorf("processEnv without overrides = %v, want qemuEnv's %v", got, want)
	}
}
