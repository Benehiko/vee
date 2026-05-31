package runnerssh

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"
	"golang.org/x/crypto/ssh"
)

// newID returns a throwaway age identity for the test.
func newID(t *testing.T) *age.X25519Identity {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	return id
}

// isolateHome points HOME (and USERPROFILE on Windows) at a temp dir so the
// package writes under a sandboxed ~/.vee/runner-ssh.
func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func TestEnsureKeyRoundTrip(t *testing.T) {
	home := isolateHome(t)
	id := newID(t)

	pub, created, err := EnsureKey(id, "")
	if err != nil {
		t.Fatalf("EnsureKey: %v", err)
	}
	if !created {
		t.Error("first EnsureKey should report created=true")
	}
	if pub == "" {
		t.Fatal("EnsureKey returned empty public key")
	}

	// Files exist with the expected perms.
	agePath := filepath.Join(home, ".vee", "runner-ssh", "global.age")
	pubPath := filepath.Join(home, ".vee", "runner-ssh", "global.pub")
	ageInfo, err := os.Stat(agePath)
	if err != nil {
		t.Fatalf("stat %s: %v", agePath, err)
	}
	if perm := ageInfo.Mode().Perm(); perm != 0o600 {
		t.Errorf("private key perms = %o, want 600", perm)
	}
	if _, err := os.Stat(pubPath); err != nil {
		t.Fatalf("stat %s: %v", pubPath, err)
	}

	// PublicKey matches EnsureKey's return.
	gotPub, ok, err := PublicKey("")
	if err != nil || !ok {
		t.Fatalf("PublicKey: ok=%v err=%v", ok, err)
	}
	if gotPub != pub {
		t.Errorf("PublicKey = %q, EnsureKey returned %q", gotPub, pub)
	}

	// LoadPrivateKey decrypts to a parseable OpenSSH ed25519 key.
	priv, err := LoadPrivateKey(id, "")
	if err != nil {
		t.Fatalf("LoadPrivateKey: %v", err)
	}
	signer, err := ssh.ParsePrivateKey(priv)
	if err != nil {
		t.Fatalf("parse decrypted private key: %v", err)
	}
	if kt := signer.PublicKey().Type(); kt != ssh.KeyAlgoED25519 {
		t.Errorf("decrypted key type = %q, want %q", kt, ssh.KeyAlgoED25519)
	}
}

func TestEnsureKeyIdempotent(t *testing.T) {
	isolateHome(t)
	id := newID(t)

	pub1, created1, err := EnsureKey(id, "")
	if err != nil {
		t.Fatalf("first EnsureKey: %v", err)
	}
	pub2, created2, err := EnsureKey(id, "")
	if err != nil {
		t.Fatalf("second EnsureKey: %v", err)
	}
	if !created1 {
		t.Error("first EnsureKey created should be true")
	}
	if created2 {
		t.Error("second EnsureKey created should be false")
	}
	if pub1 != pub2 {
		t.Errorf("EnsureKey not idempotent: %q != %q", pub1, pub2)
	}
}

func TestGlobalAndPerInstanceSeparate(t *testing.T) {
	home := isolateHome(t)
	id := newID(t)

	globalPub, _, err := EnsureKey(id, "")
	if err != nil {
		t.Fatalf("global EnsureKey: %v", err)
	}
	instPub, _, err := EnsureKey(id, "ci-1")
	if err != nil {
		t.Fatalf("per-instance EnsureKey: %v", err)
	}
	if globalPub == instPub {
		t.Error("global and per-instance keys must differ")
	}

	// Per-instance files keyed by name.
	if _, err := os.Stat(filepath.Join(home, ".vee", "runner-ssh", "ci-1.age")); err != nil {
		t.Errorf("per-instance age file missing: %v", err)
	}
	if !Has("ci-1") {
		t.Error("Has(ci-1) = false, want true")
	}
	if Has("ci-2") {
		t.Error("Has(ci-2) = true for a key that was never generated")
	}
}

func TestPublicKeyAbsent(t *testing.T) {
	isolateHome(t)
	_, ok, err := PublicKey("nope")
	if err != nil {
		t.Fatalf("PublicKey absent: unexpected err %v", err)
	}
	if ok {
		t.Error("PublicKey ok=true for absent key")
	}
}

func TestLoadPrivateKeyAbsent(t *testing.T) {
	isolateHome(t)
	id := newID(t)
	_, err := LoadPrivateKey(id, "missing")
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("LoadPrivateKey absent err = %v, want os.ErrNotExist", err)
	}
}
