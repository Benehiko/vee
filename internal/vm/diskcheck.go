package vm

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// DiskWarning describes a disk that appears to contain existing data.
type DiskWarning struct {
	Path   string
	Reason string
}

// CheckDisksForData inspects the disks in cfg and returns a warning for each
// one that appears to contain existing data. It checks:
//   - Passthrough block devices: reads the first 512 bytes looking for a
//     non-zero MBR signature or GPT magic, indicating a live partition table.
//   - Image files (qcow2/raw/etc.) that already exist on disk: queries
//     qemu-img info for a virtual size > 0, treating any present image as
//     potentially containing data.
//
// Disks that will be freshly created (image file does not yet exist) are
// skipped — they cannot contain data.
func CheckDisksForData(cfg *VMConfig) ([]DiskWarning, error) {
	var warnings []DiskWarning
	for _, d := range cfg.Disks {
		if d.Media == "cdrom" || d.InstallISO {
			continue
		}
		// Passthrough disks are expected to contain data when SkipInstall is
		// set — the user explicitly chose to boot from an existing disk.
		if d.Passthrough && cfg.SkipInstall {
			continue
		}
		if d.Passthrough {
			w, err := checkBlockDevice(d.Path)
			if err != nil {
				return nil, err
			}
			if w != nil {
				warnings = append(warnings, *w)
			}
			continue
		}
		// Image file: only warn if it already exists.
		path := d.Path
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		}
		w, err := checkImageFile(path)
		if err != nil {
			return nil, err
		}
		if w != nil {
			warnings = append(warnings, *w)
		}
	}
	return warnings, nil
}

// checkBlockDevice reads the first 512 bytes of the device and looks for
// MBR or GPT signatures indicating an existing partition table.
func checkBlockDevice(path string) (*DiskWarning, error) {
	f, err := os.Open(path)
	if err != nil {
		// Device may require elevated permissions; warn rather than hard-error.
		return &DiskWarning{Path: path, Reason: fmt.Sprintf("could not open device to inspect: %v", err)}, nil
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, 512)
	n, err := f.Read(buf)
	if err != nil || n < 512 {
		return nil, nil
	}

	// MBR boot signature at offset 510–511.
	if buf[510] == 0x55 && buf[511] == 0xAA {
		// GPT protective MBR or GPT header at LBA 1 (offset 512); we only
		// have 512 bytes, so check the GPT magic in the partition type field
		// at offset 446 (first partition entry type byte 0xEE = GPT protective).
		reason := "MBR partition table detected"
		if buf[446+4] == 0xEE {
			reason = "GPT partition table detected"
		}
		return &DiskWarning{Path: path, Reason: reason}, nil
	}

	// GPT header magic "EFI PART" at offset 0 (if device starts at LBA 0 = GPT header directly).
	if binary.LittleEndian.Uint64(buf[0:8]) == 0x5452415020494645 {
		return &DiskWarning{Path: path, Reason: "GPT header detected at sector 0"}, nil
	}

	return nil, nil
}

// checkImageFile runs qemu-img info on the image and warns if it reports a
// non-zero virtual size, which means the image was previously created and
// may contain data.
func checkImageFile(path string) (*DiskWarning, error) {
	out, err := exec.Command("qemu-img", "info", "--output=human", path).Output()
	if err != nil {
		// Not a recognised image format; treat as empty.
		return nil, nil
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		if val, ok := strings.CutPrefix(line, "virtual size:"); ok {
			size := strings.TrimSpace(val)
			return &DiskWarning{Path: path, Reason: fmt.Sprintf("existing image file (virtual size %s)", size)}, nil
		}
	}
	return nil, nil
}
