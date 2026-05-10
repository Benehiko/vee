package templates

import "crypto/sha1"

// deterministicSSHPort maps a VM name to a stable host port in [2200, 2299].
func deterministicSSHPort(name string) int {
	h := sha1.Sum([]byte(name))
	return 2200 + int(h[0])%100
}
