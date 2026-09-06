package vm

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"github.com/Benehiko/vee/internal/platform"
)

// ParseCPUPinning converts a comma-separated string like "2,3,4,5,6,7,8,9" to
// a sorted, de-duplicated []int. An empty string returns a nil slice (no
// pinning). Unlike the TUI's parser, invalid entries are a hard error — a
// scripted caller should not have a typo silently become "no pinning".
func ParseCPUPinning(s string) ([]int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	seen := make(map[int]bool)
	var out []int
	for part := range strings.SplitSeq(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("invalid CPU index %q: must be a non-negative integer", part)
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out, nil
}

// ValidateCPUPinning checks that every index in cpus names a real host CPU.
func ValidateCPUPinning(cpus []int) error {
	max := runtime.NumCPU()
	for _, c := range cpus {
		if c >= max {
			return fmt.Errorf("CPU index %d is out of range: host has %d CPUs (0-%d)", c, max, max-1)
		}
	}
	return nil
}

// SetCPUPinning records the host CPU indices the VM's vCPU threads should be
// pinned to via taskset after QEMU starts. QEMU cannot repin a running
// machine's vCPU threads through this path, so the change takes effect the
// next time the VM starts. An empty cpus slice disables pinning.
func (m *Manager) SetCPUPinning(name string, cpus []int) error {
	if !platform.SupportsCPUPinning() {
		return fmt.Errorf("CPU pinning is not supported on %s (requires taskset + /proc, Linux hosts only)", platform.HostOS())
	}
	if err := ValidateCPUPinning(cpus); err != nil {
		return err
	}
	cfg, err := m.loadConfig(name)
	if err != nil {
		return err
	}
	cfg.CPUPinning = cpus
	return m.saveConfig(cfg)
}
