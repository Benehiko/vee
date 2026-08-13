package vm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// ResizeResult reports what ResizeBootDisk changed.
type ResizeResult struct {
	// Path is the boot disk image's path after the resize (the managed file
	// name embeds the size, so a resize can rename the file).
	Path    string
	OldSize string
	NewSize string
	// GuestHint tells the user how the guest claims the new space, keyed off
	// what vee knows about the guest OS. Empty never — there is always a
	// generic fallback.
	GuestHint string
}

// ResizeBootDisk grows a VM's managed boot disk image to the size given by
// spec — either an absolute size ("60G") or a relative increase ("+40G") —
// and persists the new size to the VM config. The VM must be stopped;
// callers are responsible for stopping it first. Shrinking is refused: the
// guest filesystem would be truncated mid-block and destroyed.
//
// Growing only changes the virtual disk size. The guest still has to grow
// its partition and filesystem into the new space; the returned GuestHint
// says how for the guest OS vee knows about (cloud-init Linux does it
// automatically on the next boot).
func (m *Manager) ResizeBootDisk(ctx context.Context, name, spec string) (*ResizeResult, error) {
	state, err := m.loadState(name)
	if err != nil {
		return nil, err
	}
	if state.Running && isAlive(state.PID) {
		return nil, fmt.Errorf("VM %q is running; stop it first", name)
	}

	cfg, err := m.loadConfig(name)
	if err != nil {
		return nil, err
	}

	idx := findResizableBootDisk(cfg)
	if idx < 0 {
		return nil, fmt.Errorf("VM %q has no managed boot disk image to resize (raw-device --boot-disk VMs are partitioned by their owner)", name)
	}

	// Resolve the source path relative to the VM directory so an empty or
	// relative Path (stored verbatim by older configs) still resolves correctly.
	src := managedBootDiskAbsPath(name, cfg.Disks[idx])
	if !filepath.IsAbs(src) {
		src = filepath.Join(m.vmDir(name), src)
	}
	if _, statErr := os.Stat(src); statErr != nil {
		return nil, fmt.Errorf("boot disk not found at %s: %w", src, statErr)
	}

	qemuImg, err := vzQemuImgPath()
	if err != nil {
		return nil, err
	}

	format, curBytes, err := imageVirtualSize(ctx, qemuImg, src)
	if err != nil {
		return nil, err
	}

	relative, specBytes, err := ParseDiskSize(spec)
	if err != nil {
		return nil, err
	}
	target := specBytes
	if relative {
		target = curBytes + specBytes
	}
	if target == curBytes {
		return nil, fmt.Errorf("boot disk is already %s", humanDiskSize(curBytes))
	}
	if target < curBytes {
		return nil, fmt.Errorf("boot disk is %s; shrinking to %s is not supported — it would truncate the guest filesystem",
			humanDiskSize(curBytes), humanDiskSize(target))
	}

	//nolint:gosec // qemu-img resolved from vee-managed locations; args are vee-derived disk paths and a validated size
	if out, err := exec.CommandContext(ctx, qemuImg, "resize", "-f", format, src, strconv.FormatInt(target, 10)).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("qemu-img resize: %w: %s", err, strings.TrimSpace(string(out)))
	}

	// Persist the new size. The managed file name embeds the size
	// (disk-<vm>-<size>.<format>), so a generated-name disk is renamed to keep
	// the name truthful; an explicit file path stays where it is. If the
	// rename cannot happen, the old path is pinned explicitly so config and
	// image never disagree.
	cfg.Disks[idx].Size = humanDiskSize(target)
	dst := managedBootDiskAbsPath(name, cfg.Disks[idx])
	if !filepath.IsAbs(dst) {
		dst = filepath.Join(m.vmDir(name), dst)
	}
	finalPath := src
	if dst != src {
		if _, statErr := os.Stat(dst); statErr == nil {
			cfg.Disks[idx].Path = src
		} else if renameErr := os.Rename(src, dst); renameErr == nil {
			finalPath = dst
		} else {
			cfg.Disks[idx].Path = src
		}
	}

	if err := m.saveConfig(cfg); err != nil {
		// Undo the rename so the still-persisted old config keeps matching
		// the on-disk file. The grow itself is harmless to leave in place.
		if finalPath != src {
			_ = os.Rename(dst, src)
		}
		return nil, fmt.Errorf("save config: %w (the image itself was already grown to %s)", err, humanDiskSize(target))
	}

	return &ResizeResult{
		Path:      finalPath,
		OldSize:   humanDiskSize(curBytes),
		NewSize:   humanDiskSize(target),
		GuestHint: guestGrowHint(cfg),
	}, nil
}

// findResizableBootDisk returns the index of the VM's managed boot disk image —
// the first writable, non-passthrough qcow2 or raw "disk". Broader than
// findManagedBootDisk (move): vz guests materialize their boot disk as a raw
// image (#127), which qemu-img resizes fine but which the qcow2-only move
// path must never touch.
func findResizableBootDisk(cfg *VMConfig) int {
	for i := range cfg.Disks {
		d := &cfg.Disks[i]
		if d.Passthrough || d.Media != "disk" || d.Readonly {
			continue
		}
		switch d.Format {
		// Empty format defaults to qcow2, mirroring managedBootDiskAbsPath.
		case "qcow2", "raw", "":
			return i
		}
	}
	return -1
}

// imageVirtualSize returns an image's format and virtual size in bytes via
// qemu-img info.
func imageVirtualSize(ctx context.Context, qemuImg, path string) (string, int64, error) {
	//nolint:gosec // qemu-img resolved from vee-managed locations; path is a VM-owned disk image
	out, err := exec.CommandContext(ctx, qemuImg, "info", "--output=json", path).Output()
	if err != nil {
		return "", 0, fmt.Errorf("qemu-img info %s: %w", path, err)
	}
	var info struct {
		Format      string `json:"format"`
		VirtualSize int64  `json:"virtual-size"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return "", 0, fmt.Errorf("parse qemu-img info for %s: %w", path, err)
	}
	return info.Format, info.VirtualSize, nil
}

// ParseDiskSize parses a disk size spec: an integer with an optional binary
// suffix (K, M, G, T — 1K = 1024 bytes, matching qemu-img), e.g. "60G",
// "512M", or a bare byte count. A leading "+" marks the spec as relative
// ("grow by this much") and is reported via the first return value.
func ParseDiskSize(spec string) (relative bool, bytes int64, err error) {
	s := strings.TrimSpace(spec)
	if rest, ok := strings.CutPrefix(s, "+"); ok {
		relative = true
		s = rest
	}
	if s == "" {
		return false, 0, fmt.Errorf("empty disk size %q", spec)
	}

	mult := int64(1)
	switch s[len(s)-1] {
	case 'k', 'K':
		mult = 1 << 10
	case 'm', 'M':
		mult = 1 << 20
	case 'g', 'G':
		mult = 1 << 30
	case 't', 'T':
		mult = 1 << 40
	}
	digits := s
	if mult > 1 {
		digits = s[:len(s)-1]
	}

	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || n <= 0 {
		return false, 0, fmt.Errorf("invalid disk size %q (expected forms: 60G, 512M, +40G)", spec)
	}
	if n > (1<<63-1)/mult {
		return false, 0, fmt.Errorf("disk size %q overflows", spec)
	}
	return relative, n * mult, nil
}

// humanDiskSize renders a byte count in the largest binary unit that divides
// it evenly — the same "60G" shape disk sizes take everywhere else in vee
// (config Size fields, generated file names).
func humanDiskSize(b int64) string {
	units := []struct {
		mult   int64
		suffix string
	}{
		{1 << 40, "T"},
		{1 << 30, "G"},
		{1 << 20, "M"},
		{1 << 10, "K"},
	}
	for _, u := range units {
		if b >= u.mult && b%u.mult == 0 {
			return strconv.FormatInt(b/u.mult, 10) + u.suffix
		}
	}
	return strconv.FormatInt(b, 10)
}

// guestGrowHint says how the guest grows its filesystem into space added by
// a resize, keyed off what vee knows about the guest OS.
func guestGrowHint(cfg *VMConfig) string {
	switch {
	case strings.Contains(strings.ToLower(cfg.Template), "windows"):
		return `Inside Windows, extend C: over the new space (Disk Management → Extend Volume, or):
  powershell "Resize-Partition -DriveLetter C -Size (Get-PartitionSupportedSize -DriveLetter C).SizeMax"
If Windows placed a recovery partition between C: and the free space, delete it
first: diskpart → select disk 0 → list partition → select partition <n> →
delete partition override.`
	case cfg.MacOS != nil:
		return `Inside macOS, grow the APFS container into the new space:
  diskutil apfs resizeContainer disk0s2 0`
	case cfg.CloudInit != nil:
		return "cloud-init grows the root filesystem into the new space automatically on the next boot."
	default:
		return "Grow the partition and filesystem inside the guest to claim the new space (e.g. growpart + resize2fs)."
	}
}
