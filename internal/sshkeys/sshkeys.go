package sshkeys

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// EnsureVeeKeyPair returns the vee-managed public key string (authorized_keys format),
// generating the keypair at <home>/.vee/ssh/id_ed25519 if it doesn't exist yet.
func EnsureVeeKeyPair(home string) (pubKey string, privKeyPath string, err error) {
	dir := filepath.Join(home, ".vee", "ssh")
	privKeyPath = filepath.Join(dir, "id_ed25519")
	pubKeyPath := privKeyPath + ".pub"

	if _, err := os.Stat(privKeyPath); err == nil {
		// Already exists — read the public key.
		data, err := os.ReadFile(pubKeyPath)
		if err != nil {
			return "", "", fmt.Errorf("read vee public key: %w", err)
		}
		return strings.TrimSpace(string(data)), privKeyPath, nil
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", fmt.Errorf("create ssh dir: %w", err)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generate key: %w", err)
	}

	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", "", fmt.Errorf("marshal public key: %w", err)
	}

	privPEM, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return "", "", fmt.Errorf("marshal private key: %w", err)
	}

	if err := os.WriteFile(privKeyPath, pem.EncodeToMemory(privPEM), 0o600); err != nil {
		return "", "", fmt.Errorf("write private key: %w", err)
	}

	pubKeyBytes := ssh.MarshalAuthorizedKey(sshPub)
	if err := os.WriteFile(pubKeyPath, pubKeyBytes, 0o644); err != nil {
		return "", "", fmt.Errorf("write public key: %w", err)
	}

	return strings.TrimSpace(string(pubKeyBytes)), privKeyPath, nil
}
