package vpn

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"golang.org/x/crypto/curve25519"
)

// WireGuardKeyPair holds a base64-encoded WireGuard private/public key pair.
type WireGuardKeyPair struct {
	PrivateKey string
	PublicKey  string
}

// GenerateWireGuardKeyPair generates a new WireGuard keypair using Curve25519.
func GenerateWireGuardKeyPair() (*WireGuardKeyPair, error) {
	var priv [32]byte
	if _, err := rand.Read(priv[:]); err != nil {
		return nil, fmt.Errorf("generate private key: %w", err)
	}
	// Clamp per RFC 7748.
	priv[0] &= 248
	priv[31] = (priv[31] & 127) | 64

	pub, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return nil, fmt.Errorf("derive public key: %w", err)
	}

	return &WireGuardKeyPair{
		PrivateKey: base64.StdEncoding.EncodeToString(priv[:]),
		PublicKey:  base64.StdEncoding.EncodeToString(pub),
	}, nil
}
