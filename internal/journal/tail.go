package journal

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// TailOptions controls what journal entries are streamed.
type TailOptions struct {
	// KernelOnly filters to kernel messages only (SYSLOG_IDENTIFIER=kernel or _TRANSPORT=kernel).
	KernelOnly bool
	// Follow tails new entries as they arrive (like journalctl -f).
	Follow bool
	// Lines limits initial output to the last N lines (0 = all).
	Lines int
}

// Tail streams journal entries from the files in dir to stdout using journalctl.
// Returns an error if no journal files exist or journalctl is unavailable.
func Tail(ctx context.Context, dir string, opts TailOptions) error {
	files, err := journalFiles(dir)
	if err != nil {
		return fmt.Errorf("read journal dir: %w", err)
	}
	if len(files) == 0 {
		return fmt.Errorf("no journal files in %s (has the VM connected yet?)", dir)
	}

	bin, err := exec.LookPath("journalctl")
	if err != nil {
		return fmt.Errorf("journalctl not found: %w", err)
	}

	args := []string{"--no-pager", "--output=short-precise"}
	for _, f := range files {
		args = append(args, "--file="+f)
	}
	if opts.KernelOnly {
		args = append(args, "_TRANSPORT=kernel")
	}
	if opts.Follow {
		args = append(args, "--follow")
	}
	if opts.Lines > 0 {
		args = append(args, fmt.Sprintf("--lines=%d", opts.Lines))
	}

	//nolint:gosec // bin is resolved via exec.LookPath and args are fixed flags plus validated file paths from the journal dir; no shell involvement.
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func journalFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".journal" {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	return files, nil
}
