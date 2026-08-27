package shacrypt

import (
	"strings"
	"testing"
)

// Vectors from the SHA-crypt specification (Drepper, SHA-crypt.txt) that use
// the default 5000 rounds, plus the format contract GenerateHash promises.
func TestSha512CryptSpecVectors(t *testing.T) {
	cases := []struct {
		password, salt, want string
	}{
		{
			password: "Hello world!",
			salt:     "saltstring",
			want:     "$6$saltstring$svn8UoSVapNtMuq1ukKS4tPQd8iKwSMHWjl/O817G3uBnIFNjnQJuesI68u4OTLiBFdcbYEdFCoEOfaS35inz1",
		},
	}
	for _, tc := range cases {
		got, err := Sha512Crypt(tc.password, tc.salt)
		if err != nil {
			t.Fatalf("Sha512Crypt(%q, %q): %v", tc.password, tc.salt, err)
		}
		if got != tc.want {
			t.Errorf("Sha512Crypt(%q, %q):\n got  %s\n want %s", tc.password, tc.salt, got, tc.want)
		}
	}
}

func TestSha512CryptRejectsBadSalt(t *testing.T) {
	if _, err := Sha512Crypt("pw", ""); err == nil {
		t.Error("empty salt accepted")
	}
	if _, err := Sha512Crypt("pw", "01234567890123456"); err == nil {
		t.Error("17-character salt accepted")
	}
	if _, err := Sha512Crypt("pw", "bad salt"); err == nil {
		t.Error("salt with space accepted")
	}
}

func TestGenerateHashFormat(t *testing.T) {
	h, err := GenerateHash("secret")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(h, "$")
	// "", "6", salt, hash
	if len(parts) != 4 || parts[1] != "6" {
		t.Fatalf("unexpected hash shape: %s", h)
	}
	if len(parts[2]) != 16 {
		t.Errorf("salt length = %d, want 16", len(parts[2]))
	}
	if len(parts[3]) != 86 {
		t.Errorf("hash length = %d, want 86", len(parts[3]))
	}
	// Same password twice must differ (fresh salt).
	h2, err := GenerateHash("secret")
	if err != nil {
		t.Fatal(err)
	}
	if h == h2 {
		t.Error("two GenerateHash calls produced identical hashes")
	}
}
