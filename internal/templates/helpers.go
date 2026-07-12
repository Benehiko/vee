package templates

import "hash/fnv"

// deterministicSSHPort maps a VM name to a stable host port in [2200, 2299].
func deterministicSSHPort(name string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	return 2200 + int(h.Sum32()%100)
}
