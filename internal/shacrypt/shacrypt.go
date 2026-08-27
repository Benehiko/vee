// Package shacrypt implements the SHA-512 variant of Ulrich Drepper's
// SHA-crypt password hashing scheme — the "$6$" hashes produced by
// crypt(3) and `openssl passwd -6`. vee uses it to seed credentials for
// unattended installers (archinstall's user_credentials.json) without
// shelling out to openssl on the host.
//
// Reference: https://www.akkadia.org/drepper/SHA-crypt.txt
package shacrypt

import (
	"crypto/rand"
	"crypto/sha512"
	"fmt"
	"strings"
)

const (
	// defaultRounds is the spec default; hashes made with it omit the
	// "rounds=" field, matching crypt(3) and openssl output.
	defaultRounds = 5000
	saltAlphabet  = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	maxSaltLen    = 16
)

// Sha512Crypt hashes password with the given salt at the default 5000
// rounds and returns the full "$6$<salt>$<hash>" string. The salt must be
// 1-16 characters from the crypt base64 alphabet ("./0-9A-Za-z").
func Sha512Crypt(password, salt string) (string, error) {
	if salt == "" || len(salt) > maxSaltLen {
		return "", fmt.Errorf("shacrypt: salt must be 1-%d characters, got %d", maxSaltLen, len(salt))
	}
	for _, c := range salt {
		if !strings.ContainsRune(saltAlphabet, c) {
			return "", fmt.Errorf("shacrypt: salt contains invalid character %q", c)
		}
	}
	return "$6$" + salt + "$" + sha512crypt([]byte(password), []byte(salt), defaultRounds), nil
}

// GenerateHash hashes password with a fresh random 16-character salt.
func GenerateHash(password string) (string, error) {
	raw := make([]byte, maxSaltLen)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("shacrypt: salt: %w", err)
	}
	salt := make([]byte, maxSaltLen)
	for i, b := range raw {
		salt[i] = saltAlphabet[int(b)%len(saltAlphabet)]
	}
	return Sha512Crypt(password, string(salt))
}

// sha512crypt runs the SHA-crypt core (steps 1-22 of the spec) and returns
// the 86-character base64 encoding of the final digest.
func sha512crypt(password, salt []byte, rounds int) string {
	// Digest B: password + salt + password.
	b := sha512.New()
	b.Write(password)
	b.Write(salt)
	b.Write(password)
	digestB := b.Sum(nil)

	// Digest A: password + salt, then digest B repeated/truncated to
	// len(password) bytes, then for each bit of len(password) (low to high)
	// digest B when set, password when clear.
	a := sha512.New()
	a.Write(password)
	a.Write(salt)
	writeRepeated(a, digestB, len(password))
	for cnt := len(password); cnt > 0; cnt >>= 1 {
		if cnt&1 != 0 {
			a.Write(digestB)
		} else {
			a.Write(password)
		}
	}
	digestA := a.Sum(nil)

	// Sequence P: digest of password repeated len(password) times, expanded
	// back out to len(password) bytes.
	dp := sha512.New()
	for range len(password) {
		dp.Write(password)
	}
	p := expand(dp.Sum(nil), len(password))

	// Sequence S: digest of salt repeated 16+digestA[0] times, expanded to
	// len(salt) bytes.
	ds := sha512.New()
	for range 16 + int(digestA[0]) {
		ds.Write(salt)
	}
	s := expand(ds.Sum(nil), len(salt))

	// The rounds loop mixes P, S, and the running digest per the spec's
	// alternation schedule.
	c := digestA
	for i := range rounds {
		h := sha512.New()
		if i%2 != 0 {
			h.Write(p)
		} else {
			h.Write(c)
		}
		if i%3 != 0 {
			h.Write(s)
		}
		if i%7 != 0 {
			h.Write(p)
		}
		if i%2 != 0 {
			h.Write(c)
		} else {
			h.Write(p)
		}
		c = h.Sum(nil)
	}

	return encode(c)
}

// writeRepeated writes digest to h in full 64-byte blocks covering n bytes,
// with the remainder truncated — the spec's "add for any character in the
// key" construction.
func writeRepeated(h interface{ Write([]byte) (int, error) }, digest []byte, n int) {
	for ; n > sha512.Size; n -= sha512.Size {
		_, _ = h.Write(digest)
	}
	_, _ = h.Write(digest[:n])
}

// expand builds the P/S byte sequences: digest repeated to cover n bytes,
// remainder truncated.
func expand(digest []byte, n int) []byte {
	out := make([]byte, 0, n)
	for ; n > sha512.Size; n -= sha512.Size {
		out = append(out, digest...)
	}
	return append(out, digest[:n]...)
}

// encode renders the 64-byte digest in crypt's base64 variant with the
// SHA-512-specific byte permutation (glibc's b64_from_24bit call sequence).
func encode(digest []byte) string {
	// Each triple is emitted low six bits first from b2 | b1<<8 | b0<<16.
	triples := [][3]int{
		{0, 21, 42},
		{22, 43, 1},
		{44, 2, 23},
		{3, 24, 45},
		{25, 46, 4},
		{47, 5, 26},
		{6, 27, 48},
		{28, 49, 7},
		{50, 8, 29},
		{9, 30, 51},
		{31, 52, 10},
		{53, 11, 32},
		{12, 33, 54},
		{34, 55, 13},
		{56, 14, 35},
		{15, 36, 57},
		{37, 58, 16},
		{59, 17, 38},
		{18, 39, 60},
		{40, 61, 19},
		{62, 20, 41},
	}
	var sb strings.Builder
	sb.Grow(86)
	for _, t := range triples {
		w := uint32(digest[t[0]])<<16 | uint32(digest[t[1]])<<8 | uint32(digest[t[2]])
		for range 4 {
			sb.WriteByte(saltAlphabet[w&0x3f])
			w >>= 6
		}
	}
	// Final lone byte yields two characters.
	w := uint32(digest[63])
	sb.WriteByte(saltAlphabet[w&0x3f])
	sb.WriteByte(saltAlphabet[(w>>6)&0x3f])
	return sb.String()
}
