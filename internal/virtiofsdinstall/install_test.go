package virtiofsdinstall

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyChecksum(t *testing.T) {
	content := []byte("hello virtiofsd\n")
	sum := sha256.Sum256(content)
	wantHex := hex.EncodeToString(sum[:])

	dir := t.TempDir()
	path := filepath.Join(dir, "blob")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write blob: %v", err)
	}

	t.Run("match", func(t *testing.T) {
		if err := verifyChecksum(path, wantHex); err != nil {
			t.Fatalf("expected nil error for matching checksum, got %v", err)
		}
	})

	t.Run("mismatch names both hashes", func(t *testing.T) {
		badWant := strings.Repeat("0", 64)
		err := verifyChecksum(path, badWant)
		if err == nil {
			t.Fatal("expected error for mismatched checksum, got nil")
		}
		msg := err.Error()
		if !strings.Contains(msg, badWant) {
			t.Errorf("error should name the expected hash %q; got %q", badWant, msg)
		}
		if !strings.Contains(msg, wantHex) {
			t.Errorf("error should name the actual hash %q; got %q", wantHex, msg)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		if err := verifyChecksum(filepath.Join(dir, "nope"), wantHex); err == nil {
			t.Fatal("expected error for missing file, got nil")
		}
	})
}

// TestSourceAccessorsPinned guards against the exported source pins drifting out
// of sync with the private constants the container build path uses.
func TestSourceAccessorsPinned(t *testing.T) {
	if SourceURL() != virtiofsdSrcURL {
		t.Errorf("SourceURL() = %q, want %q", SourceURL(), virtiofsdSrcURL)
	}
	if SourceSHA256() != virtiofsdSHA256 {
		t.Errorf("SourceSHA256() = %q, want %q", SourceSHA256(), virtiofsdSHA256)
	}
	if !strings.Contains(SourceURL(), virtiofsdCommit) {
		t.Errorf("SourceURL() should pin the commit %q; got %q", virtiofsdCommit, SourceURL())
	}
	if len(virtiofsdSHA256) != 64 {
		t.Errorf("virtiofsdSHA256 should be a 64-char hex sha256; got %d chars", len(virtiofsdSHA256))
	}
}
