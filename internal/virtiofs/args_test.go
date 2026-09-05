package virtiofs

import (
	"slices"
	"testing"
)

// A read-only share must reach virtiofsd as --readonly: without it the daemon
// serves the directory read-write and a guest can modify host data that the
// caller asked to protect.
func TestArgsReadonly(t *testing.T) {
	vd := NewVirtiofsd(nil,
		WithVirtiofsdSocketPath("/tmp/vfs.sock"),
		WithVirtiofsdSharedDir("/mnt/library"),
		WithReadonly(true),
	)
	if got := vd.args(); !slices.Contains(got, "--readonly") {
		t.Errorf("args() = %v, want it to contain --readonly", got)
	}
}

func TestArgsReadonlyOmittedByDefault(t *testing.T) {
	vd := NewVirtiofsd(nil,
		WithVirtiofsdSocketPath("/tmp/vfs.sock"),
		WithVirtiofsdSharedDir("/mnt/library"),
	)
	if got := vd.args(); slices.Contains(got, "--readonly") {
		t.Errorf("args() = %v, want no --readonly when it was not requested", got)
	}
}
