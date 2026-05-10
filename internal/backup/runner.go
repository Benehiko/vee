package backup

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Runner executes a backup run, updating DB status as it progresses.
type Runner struct {
	DB   *sql.DB
	Conn SSHConn
}

// Execute runs the backup for the given run ID. Dirs must already be persisted
// in backup_dirs. Sets status=running on start, done/failed on completion.
func (r *Runner) Execute(runID int64, dest string, dirs []string) error {
	if err := SetStatus(r.DB, runID, StatusRunning, ""); err != nil {
		return fmt.Errorf("mark running: %w", err)
	}

	var firstErr error
	for _, guestPath := range dirs {
		// Mirror the guest path structure under dest.
		// e.g. /home/user/.config → dest/home/user/.config/
		rel := strings.TrimLeft(guestPath, "/")
		localDest := filepath.Join(dest, rel)

		if err := os.MkdirAll(localDest, 0o755); err != nil {
			firstErr = fmt.Errorf("mkdir %s: %w", localDest, err)
			break
		}

		fmt.Printf("  → %s\n", guestPath)

		cmd, err := RunRsyncCmd(r.Conn, guestPath, localDest)
		if err != nil {
			firstErr = err
			break
		}
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			firstErr = fmt.Errorf("rsync %s: %w", guestPath, err)
			break
		}
	}

	if firstErr != nil {
		_ = SetStatus(r.DB, runID, StatusFailed, firstErr.Error())
		return firstErr
	}

	return SetStatus(r.DB, runID, StatusDone, "")
}
