package cloudinit

import (
	"fmt"
	"os"
	"path/filepath"
)

// SeedFile is one file to place in the root of an installer seed ISO.
type SeedFile struct {
	Name    string
	Content []byte
}

// GenerateSeed writes vmDir/seed.iso: an ISO9660+Joliet image labelled
// "cidata" carrying exactly the given files in its root directory.
//
// This is the generic sibling of Generate: instead of the NoCloud
// user-data/meta-data pair it accepts arbitrary files, for installers that
// key off the cidata volume label but read their own file set — Omarchy's
// autoinstall (user_configuration.json + user_credentials.json + friends)
// being the first user. The native writer needs no external tools and the
// files are kilobytes, so there is no external-tool fallback chain here.
func GenerateSeed(vmDir string, files []SeedFile) (string, error) {
	if len(files) == 0 {
		return "", fmt.Errorf("seed ISO: no files given")
	}
	if err := os.MkdirAll(vmDir, 0o750); err != nil {
		return "", err
	}

	isoFiles := make([]isoFile, len(files))
	for i, f := range files {
		isoFiles[i] = isoFile{name: f.Name, data: f.Content}
	}
	img, err := writeISO("cidata", isoFiles)
	if err != nil {
		return "", err
	}

	isoPath := filepath.Join(vmDir, "seed.iso")
	// isoPath is derived from the VM directory, not user input.
	if err := os.WriteFile(isoPath, img, 0o600); err != nil { //nolint:gosec // internally-built VM path
		return "", fmt.Errorf("write ISO %s: %w", filepath.Base(isoPath), err)
	}
	return isoPath, nil
}
