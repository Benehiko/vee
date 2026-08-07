package gpu

import (
	"fmt"
	"math/bits"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Resizable BAR (ReBAR) support.
//
// A PCI BAR can only be resized while no driver is bound to the device. Some
// GPUs (e.g. RDNA3 cards) do not survive a runtime unbind/rebind cycle and
// need a cold boot afterwards, so the only universally safe point to resize
// is early boot — after PCI enumeration, before any driver (vfio-pci from
// the initramfs MODULES list, or the native driver) binds. vee installs an
// initramfs early hook that reads RebarConfPath and applies each entry.
//
// Why resize at all: drivers like tinygrad's AMD backend map all VRAM
// CPU-visible and need BAR0 to cover the whole VRAM. The native driver
// resizes the BAR itself at probe time, but a device that goes straight to
// vfio-pci keeps the firmware default (typically 256M), and the guest is
// stuck with it — QEMU exposes only the current host BAR size.

// RebarConfPath lists the BARs to resize at boot, one entry per line:
// "<pci-addr> <bar-index> <size-exponent>". The exponent follows the ReBAR
// capability encoding: size = 2^exp MB.
const RebarConfPath = "/etc/vee/rebar.conf"

// RebarEntry is one boot-time BAR resize: resize BAR <Bar> of <Addr> to
// 2^Exp MB.
type RebarEntry struct {
	Addr string
	Bar  int
	Exp  int
}

// SizeBytes returns the target BAR size in bytes.
func (e RebarEntry) SizeBytes() uint64 {
	if e.Exp < 0 || e.Exp > 43 {
		return 0
	}
	return uint64(1) << (uint(e.Exp) + 20) //nolint:gosec // e.Exp bounds-checked above
}

// ParseRebarSize converts a human size string ("16G", "512M") to the ReBAR
// exponent encoding (size = 2^exp MB). The size must be a power of two and
// at least 1M.
func ParseRebarSize(s string) (int, error) {
	b, err := parseMemoryBytes(s)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: %w", s, err)
	}
	const mib = 1 << 20
	if b < mib || b%mib != 0 {
		return 0, fmt.Errorf("size %q must be a multiple of 1M", s)
	}
	if b&(b-1) != 0 {
		return 0, fmt.Errorf("size %q must be a power of two", s)
	}
	return bits.TrailingZeros64(b) - 20, nil
}

// RebarSupported reads the supported-sizes bitmask for one BAR of a device
// from sysfs (resource<bar>_resize). Bit n set means 2^n MB is supported.
// Returns an error if the device or BAR has no resize capability exposed.
func RebarSupported(addr string, bar int) (uint64, error) {
	pciAddr := normalizePCIAddr(addr)
	path := filepath.Join("/sys/bus/pci/devices", pciAddr, fmt.Sprintf("resource%d_resize", bar))
	raw, err := os.ReadFile(path) //nolint:gosec // fixed /sys/bus/pci sysfs path built from a normalized PCI address, not user input
	if err != nil {
		return 0, fmt.Errorf("device %s BAR%d is not resizable (%s): %w", pciAddr, bar, path, err)
	}
	mask, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 16, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", path, err)
	}
	return mask, nil
}

// FormatRebarSizes renders a supported-sizes bitmask as "256M, 512M, ... 16G".
func FormatRebarSizes(mask uint64) string {
	var parts []string
	for n := 0; n < 64; n++ {
		if mask&(1<<uint(n)) != 0 {
			parts = append(parts, FormatBytes(uint64(1)<<(uint(n)+20)))
		}
	}
	return strings.Join(parts, ", ")
}

// BARSizeBytes returns the current size of one BAR by parsing the device's
// sysfs resource table (line <bar>: "<start> <end> <flags>").
func BARSizeBytes(addr string, bar int) (uint64, error) {
	pciAddr := normalizePCIAddr(addr)
	path := filepath.Join("/sys/bus/pci/devices", pciAddr, "resource")
	raw, err := os.ReadFile(path) //nolint:gosec // fixed /sys/bus/pci sysfs path built from a normalized PCI address, not user input
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", path, err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if bar >= len(lines) {
		return 0, fmt.Errorf("device %s has no BAR%d", pciAddr, bar)
	}
	fields := strings.Fields(lines[bar])
	if len(fields) < 2 {
		return 0, fmt.Errorf("malformed resource line for %s BAR%d", pciAddr, bar)
	}
	start, err := strconv.ParseUint(fields[0], 0, 64)
	if err != nil {
		return 0, err
	}
	end, err := strconv.ParseUint(fields[1], 0, 64)
	if err != nil {
		return 0, err
	}
	if end < start || (start == 0 && end == 0) {
		return 0, fmt.Errorf("BAR%d of %s is unassigned", bar, pciAddr)
	}
	return end - start + 1, nil
}

// ParseRebarConf parses rebar.conf content. Blank lines and #-comments are
// ignored; malformed lines are an error so a typo cannot silently drop a
// resize.
func ParseRebarConf(content string) ([]RebarEntry, error) {
	var entries []RebarEntry
	for i, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return nil, fmt.Errorf("rebar.conf line %d: want \"<addr> <bar> <exp>\", got %q", i+1, line)
		}
		bar, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, fmt.Errorf("rebar.conf line %d: bar index: %w", i+1, err)
		}
		exp, err := strconv.Atoi(fields[2])
		if err != nil {
			return nil, fmt.Errorf("rebar.conf line %d: size exponent: %w", i+1, err)
		}
		entries = append(entries, RebarEntry{Addr: normalizePCIAddr(fields[0]), Bar: bar, Exp: exp})
	}
	return entries, nil
}

// RenderRebarConf renders entries back to rebar.conf content, sorted for a
// stable diff-friendly output.
func RenderRebarConf(entries []RebarEntry) string {
	sorted := make([]RebarEntry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Addr != sorted[j].Addr {
			return sorted[i].Addr < sorted[j].Addr
		}
		return sorted[i].Bar < sorted[j].Bar
	})
	var b strings.Builder
	b.WriteString("# Written by `vee gpu rebar`. One boot-time BAR resize per line:\n")
	b.WriteString("# <pci-addr> <bar-index> <size-exponent>   (size = 2^exp MB)\n")
	for _, e := range sorted {
		fmt.Fprintf(&b, "%s %d %d\n", e.Addr, e.Bar, e.Exp)
	}
	return b.String()
}

// MergeRebarEntry inserts or replaces the entry for (addr, bar). The
// address is normalized to the canonical domain-prefixed form.
func MergeRebarEntry(entries []RebarEntry, entry RebarEntry) []RebarEntry {
	entry.Addr = normalizePCIAddr(entry.Addr)
	for i, e := range entries {
		if e.Addr == entry.Addr && e.Bar == entry.Bar {
			entries[i] = entry
			return entries
		}
	}
	return append(entries, entry)
}

// RemoveRebarEntry drops the entry for (addr, bar); reports whether one was
// present.
func RemoveRebarEntry(entries []RebarEntry, addr string, bar int) ([]RebarEntry, bool) {
	addr = normalizePCIAddr(addr)
	for i, e := range entries {
		if e.Addr == addr && e.Bar == bar {
			return append(entries[:i], entries[i+1:]...), true
		}
	}
	return entries, false
}
