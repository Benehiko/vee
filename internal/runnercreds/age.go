// Package runnercreds persists a self-hosted GitHub Actions runner's
// registration credentials on the host, encrypted with age, so a runner VM can
// be recreated (vee create --reinstall) without fetching a fresh registration
// token from GitHub and without orphaning the old runner entry.
//
// The runner software stores three files under /opt/actions-runner once
// config.sh has registered the runner:
//
//	.credentials            runner identity + auth endpoints (JSON)
//	.credentials_rsaparams  RSA key params for the OAuth client assertion
//	.runner                 agent id, pool id, server URL (JSON)
//
// These are long-lived (they survive VM restarts) but are lost when the VM disk
// is destroyed. Snapshot pulls them off a running VM and writes an age-encrypted
// archive to the VM's storage dir; Restore decrypts it for re-injection via
// cloud-init on the next create.
package runnercreds

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"

	"filippo.io/age"
)

// CredFiles are the runner files persisted across recreate. Paths are relative
// to /opt/actions-runner inside the VM.
var CredFiles = []string{
	".credentials",
	".credentials_rsaparams",
	".runner",
}

// ageDir returns ~/.vee/age, creating it 0700 if missing.
func ageDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	dir := filepath.Join(home, ".vee", "age")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create age dir: %w", err)
	}
	return dir, nil
}

// identityPath is ~/.vee/age/identity.txt — the host's age private key.
func identityPath() (string, error) {
	dir, err := ageDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "identity.txt"), nil
}

// LoadOrCreateIdentity returns the host's age identity, generating a new X25519
// keypair on first use. The identity file is owner-only (0600) per the host
// trust model — the same model vee already uses for VM configs and SSH keys.
func LoadOrCreateIdentity() (*age.X25519Identity, error) {
	path, err := identityPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err == nil {
		id, perr := age.ParseX25519Identity(string(trimNewline(data)))
		if perr != nil {
			return nil, fmt.Errorf("parse age identity %s: %w", path, perr)
		}
		return id, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read age identity %s: %w", path, err)
	}

	// First use: generate and persist a fresh identity.
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, fmt.Errorf("generate age identity: %w", err)
	}
	// Write atomically with 0600 so the private key is never world-readable,
	// even briefly.
	tmp, err := writeSecret(path, []byte(id.String()+"\n"))
	if err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return nil, fmt.Errorf("install age identity: %w", err)
	}
	return id, nil
}

// writeSecret writes data to a temp file alongside dst with 0600 perms and
// returns the temp path for the caller to rename into place atomically.
func writeSecret(dst string, data []byte) (string, error) {
	dir := filepath.Dir(dst)
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("temp nonce: %w", err)
	}
	tmp := filepath.Join(dir, fmt.Sprintf(".%x.tmp", nonce))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("create secret temp: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", fmt.Errorf("write secret temp: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("close secret temp: %w", err)
	}
	return tmp, nil
}

func trimNewline(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}
