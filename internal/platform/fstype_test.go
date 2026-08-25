//go:build !windows

package platform

import (
	"os/exec"
	"strings"
	"testing"
)

// TestNetworkFilesystemNameLocal checks a local directory is not misreported.
// A false positive here would reject a perfectly good --pg-data-dir.
func TestNetworkFilesystemNameLocal(t *testing.T) {
	if got := NetworkFilesystemName(t.TempDir()); got != "" {
		t.Errorf("temp dir reported as %q, want local", got)
	}
}

// TestNetworkFilesystemNameDetectsNFS is the case that matters: pointing a
// PostgreSQL data directory at NFS does not fail, it hangs — initdb fsyncs
// thousands of small files and an NFS "hard" mount never returns an error, so
// the guest blocks forever and cloud-init stalls before the firewall or the
// VPN tunnel are ever configured.
//
// Skipped when the host has no NFS mount, since it cannot be simulated without
// root.
func TestNetworkFilesystemNameDetectsNFS(t *testing.T) {
	out, err := exec.CommandContext(t.Context(), "findmnt", "-t", "nfs,nfs4", "-n", "-o", "TARGET").Output()
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		t.Skip("no NFS mount on this host")
	}
	target := strings.Fields(strings.TrimSpace(string(out)))[0]

	got := NetworkFilesystemName(target)
	if got != "NFS" {
		t.Errorf("NetworkFilesystemName(%q) = %q, want NFS", target, got)
	}
}

// TestNetworkFilesystemNameMissingPath checks an unreadable path is treated as
// local rather than rejected. Undetectable is not the same as unsafe, and
// failing the create here would be a worse outcome than letting it proceed.
func TestNetworkFilesystemNameMissingPath(t *testing.T) {
	if got := NetworkFilesystemName("/nonexistent/path/xyzzy"); got != "" {
		t.Errorf("missing path reported as %q, want empty", got)
	}
}
