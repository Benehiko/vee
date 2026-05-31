package runnercreds

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
)

// fakeSSH satisfies SSHRunner with canned responses keyed by a substring of the
// remote command, so the snapshot path can be exercised without a real VM.
type fakeSSH struct {
	credExists bool
	archive    []byte // raw tar bytes the guest "produces"
	err        error
}

func (f *fakeSSH) Output(_ context.Context, remoteCmd string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	if strings.Contains(remoteCmd, "test -f") {
		if f.credExists {
			return nil, nil
		}
		return nil, errors.New("exit status 1")
	}
	if strings.Contains(remoteCmd, "base64 -w0") {
		enc := base64.StdEncoding.EncodeToString(f.archive)
		return []byte(enc + "\n"), nil
	}
	return nil, errors.New("unexpected command: " + remoteCmd)
}

// makeTar builds an in-memory tar of name→content, mirroring what `tar -cf -`
// on the guest emits.
func makeTar(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, content := range files {
		hdr := &tar.Header{Name: name, Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("tar write: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	return buf.Bytes()
}

// isolateHome points HOME at a temp dir so the test uses its own
// ~/.vee/age and ~/.vee/runner-creds without touching the real ones.
func isolateHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	isolateHome(t)

	id, err := LoadOrCreateIdentity()
	if err != nil {
		t.Fatalf("identity: %v", err)
	}

	want := map[string]string{
		".credentials": `{"scheme":"OAuth"}`,
		".runner":      `{"agentId":7}`,
	}
	ssh := &fakeSSH{credExists: true, archive: makeTar(t, want)}

	if err := Snapshot(context.Background(), ssh, id, "ares-ci"); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if !Has("ares-ci") {
		t.Fatal("Has returned false after snapshot")
	}

	got, err := Restore(id, "ares-ci")
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("restored %d files, want %d", len(got), len(want))
	}
	for _, f := range got {
		if want[f.RelPath] != string(f.Content) {
			t.Errorf("file %q = %q, want %q", f.RelPath, f.Content, want[f.RelPath])
		}
	}
}

func TestSnapshotUnregisteredReturnsNotExist(t *testing.T) {
	isolateHome(t)
	id, _ := LoadOrCreateIdentity()
	ssh := &fakeSSH{credExists: false}

	err := Snapshot(context.Background(), ssh, id, "ares-ci")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("want ErrNotExist for unregistered runner, got %v", err)
	}
}

func TestRestoreMissingReturnsNotExist(t *testing.T) {
	isolateHome(t)
	id, _ := LoadOrCreateIdentity()

	_, err := Restore(id, "nope")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("want ErrNotExist for missing snapshot, got %v", err)
	}
}

func TestRestoreWrongIdentityFails(t *testing.T) {
	isolateHome(t)
	id, _ := LoadOrCreateIdentity()
	ssh := &fakeSSH{credExists: true, archive: makeTar(t, map[string]string{".runner": "x"})}
	if err := Snapshot(context.Background(), ssh, id, "ares-ci"); err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	other, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("gen other: %v", err)
	}
	if _, err := Restore(other, "ares-ci"); err == nil {
		t.Fatal("decrypt with wrong identity unexpectedly succeeded")
	}
}

func TestIdentityPersistsAndPerms(t *testing.T) {
	isolateHome(t)

	id1, err := LoadOrCreateIdentity()
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	id2, err := LoadOrCreateIdentity()
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if id1.Recipient().String() != id2.Recipient().String() {
		t.Error("identity regenerated instead of reused")
	}

	home, _ := os.UserHomeDir()
	info, err := os.Stat(filepath.Join(home, ".vee", "age", "identity.txt"))
	if err != nil {
		t.Fatalf("stat identity: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("identity perms = %o, want 0600", perm)
	}
}
