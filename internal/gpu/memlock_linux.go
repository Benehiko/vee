//go:build linux

package gpu

import "golang.org/x/sys/unix"

// readMemlockLimits returns the current soft and hard RLIMIT_MEMLOCK limits in
// bytes. VFIO DMA-maps the entire guest RAM, so this limit gates passthrough.
func readMemlockLimits() (soft, hard uint64, ok bool) {
	var rLimit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_MEMLOCK, &rLimit); err != nil {
		return 0, 0, false
	}
	return rLimit.Cur, rLimit.Max, true
}
