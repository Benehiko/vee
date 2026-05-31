// Package runnerssh manages the SSH keypairs a self-hosted GitHub Actions
// runner uses to reach GitHub over SSH (cloning other private repositories or
// private submodules whose access is not covered by the workflow's scoped
// GITHUB_TOKEN).
//
// Two tiers of key are supported:
//
//	global         one shared keypair injected into every fresh runner. Add its
//	               public key to GitHub once (account SSH key, or a per-repo
//	               read-only deploy key).
//	per-instance   an optional unique keypair for a single runner (vee create
//	               --runner-ssh-key), used instead of the global key for that
//	               runner so it can be scoped to one repo via a read-only deploy
//	               key.
//
// Private keys live host-side under ~/.vee/runner-ssh, age-encrypted with the
// same host identity vee already uses for runner registration credentials
// (runnercreds.LoadOrCreateIdentity). The matching public key is stored
// alongside in plaintext (<name>.pub) for the `vee runner key` command. This
// mirrors the runnercreds design: the host is the trust boundary, and the
// encrypted key survives `vee create --reinstall`.
package runnerssh

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"
	"golang.org/x/crypto/ssh"
)

// dir returns ~/.vee/runner-ssh, creating it 0700 if missing.
func dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	d := filepath.Join(home, ".vee", "runner-ssh")
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", fmt.Errorf("create runner-ssh dir: %w", err)
	}
	return d, nil
}

// keyBase returns the on-disk basename for a key. An empty name is the global
// key; a non-empty name is a per-runner key keyed by VM name.
func keyBase(name string) string {
	if name == "" {
		return "global"
	}
	return name
}

// keyPaths returns the age-encrypted private key path and the plaintext public
// key path for a key. name=="" addresses the global key.
func keyPaths(name string) (agePath, pubPath string, err error) {
	d, err := dir()
	if err != nil {
		return "", "", err
	}
	base := keyBase(name)
	return filepath.Join(d, base+".age"), filepath.Join(d, base+".pub"), nil
}

// Has reports whether an encrypted private key exists for name.
func Has(name string) bool {
	agePath, _, err := keyPaths(name)
	if err != nil {
		return false
	}
	_, err = os.Stat(agePath)
	return err == nil
}

// EnsureKey returns the public key (authorized_keys format) for name, generating
// a fresh ed25519 keypair on first use. The private key is stored in OpenSSH
// format with no passphrase (CI cannot enter one), age-encrypted to <name>.age;
// the public key is written plaintext to <name>.pub. It is idempotent: when the
// key already exists the stored public key is returned unchanged. created
// reports whether a new keypair was generated on this call, so callers can show
// the "add to GitHub" hint only once.
func EnsureKey(id *age.X25519Identity, name string) (pub string, created bool, err error) {
	agePath, pubPath, err := keyPaths(name)
	if err != nil {
		return "", false, err
	}

	if _, statErr := os.Stat(agePath); statErr == nil {
		// Already generated — return the stored public key.
		data, rerr := os.ReadFile(pubPath)
		if rerr != nil {
			return "", false, fmt.Errorf("read public key %s: %w", pubPath, rerr)
		}
		return strings.TrimSpace(string(data)), false, nil
	} else if !os.IsNotExist(statErr) {
		return "", false, fmt.Errorf("stat private key %s: %w", agePath, statErr)
	}

	pubKey, privPEM, err := generateKeyPair()
	if err != nil {
		return "", false, err
	}

	// Encrypt the private key to the host identity and write it atomically 0600.
	enc, err := encrypt(id, privPEM)
	if err != nil {
		return "", false, err
	}
	if err := writeSecretFile(agePath, enc); err != nil {
		return "", false, err
	}

	// Public key is not secret; 0644 so it can be read without elevation.
	if err := writeFileAtomic(pubPath, []byte(pubKey+"\n"), 0o644); err != nil {
		_ = os.Remove(agePath)
		return "", false, err
	}

	return pubKey, true, nil
}

// PublicKey returns the stored public key for name. ok is false when no key has
// been generated yet.
func PublicKey(name string) (pub string, ok bool, err error) {
	_, pubPath, err := keyPaths(name)
	if err != nil {
		return "", false, err
	}
	data, rerr := os.ReadFile(pubPath)
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read public key %s: %w", pubPath, rerr)
	}
	return strings.TrimSpace(string(data)), true, nil
}

// LoadPrivateKey decrypts and returns the OpenSSH private key bytes for name,
// ready for cloud-init injection. It returns os.ErrNotExist (wrapped) when no
// key exists for name, so callers can fall back to the global key or skip.
func LoadPrivateKey(id *age.X25519Identity, name string) ([]byte, error) {
	agePath, _, err := keyPaths(name)
	if err != nil {
		return nil, err
	}
	enc, err := os.ReadFile(agePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no runner ssh key at %s: %w", agePath, os.ErrNotExist)
		}
		return nil, fmt.Errorf("read private key %s: %w", agePath, err)
	}
	r, err := age.Decrypt(bytes.NewReader(enc), id)
	if err != nil {
		return nil, fmt.Errorf("age decrypt %s (wrong identity?): %w", agePath, err)
	}
	priv, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read decrypted private key: %w", err)
	}
	return priv, nil
}

// generateKeyPair returns a fresh ed25519 keypair: the public key in
// authorized_keys format (trimmed) and the private key in OpenSSH PEM format
// with no passphrase. It reuses the same marshalling used by the vee VM keypair
// (internal/sshkeys).
func generateKeyPair() (pub string, privPEM []byte, err error) {
	pubRaw, privRaw, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", nil, fmt.Errorf("generate ed25519 key: %w", err)
	}
	sshPub, err := ssh.NewPublicKey(pubRaw)
	if err != nil {
		return "", nil, fmt.Errorf("marshal public key: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(privRaw, "")
	if err != nil {
		return "", nil, fmt.Errorf("marshal private key: %w", err)
	}
	pubLine := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))) + " vee-runner"
	return pubLine, pem.EncodeToMemory(block), nil
}

// encrypt age-encrypts data to the recipient of id.
func encrypt(id *age.X25519Identity, data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, id.Recipient())
	if err != nil {
		return nil, fmt.Errorf("age encrypt init: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return nil, fmt.Errorf("age encrypt write: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("age encrypt close: %w", err)
	}
	return buf.Bytes(), nil
}

// writeSecretFile writes data 0600 via a temp file + atomic rename, so a private
// key is never world-readable even briefly.
func writeSecretFile(dst string, data []byte) error {
	return writeFileAtomic(dst, data, 0o600)
}

// writeFileAtomic writes data to a temp file alongside dst with perm, then
// renames it into place.
func writeFileAtomic(dst string, data []byte, perm os.FileMode) error {
	d := filepath.Dir(dst)
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fmt.Errorf("temp nonce: %w", err)
	}
	tmp := filepath.Join(d, fmt.Sprintf(".%x.tmp", nonce))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return fmt.Errorf("create temp %s: %w", tmp, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write temp %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close temp %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("install %s: %w", dst, err)
	}
	return nil
}
