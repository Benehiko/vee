//go:build !linux

package gpu

// readMemlockLimits is Linux-only. RLIMIT_MEMLOCK only matters for VFIO, which
// is unavailable off Linux, so report "not applicable" (ok=false) and let the
// preflight result carry zeroed memlock fields. The overall GPU/VFIO path is
// gated by platform.SupportsVFIO() before reaching here.
func readMemlockLimits() (soft, hard uint64, ok bool) {
	return 0, 0, false
}
