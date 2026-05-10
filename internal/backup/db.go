package backup

import (
	"database/sql"
	"time"
)

type Status string

const (
	StatusPending Status = "pending"
	StatusRunning Status = "running"
	StatusDone    Status = "done"
	StatusFailed  Status = "failed"
)

type Run struct {
	ID         int64
	VMName     string
	Dest       string
	Status     Status
	StartedAt  *time.Time
	FinishedAt *time.Time
	Error      string
	Dirs       []string
}

// CreateRun inserts a new backup run with status=pending and returns its ID.
func CreateRun(db *sql.DB, vmName, dest string, dirs []string) (int64, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.Exec(
		`INSERT INTO backup_runs (vm_name, dest, status) VALUES (?, ?, ?)`,
		vmName, dest, StatusPending,
	)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	for _, dir := range dirs {
		if _, err := tx.Exec(`INSERT INTO backup_dirs (run_id, guest_path) VALUES (?, ?)`, id, dir); err != nil {
			return 0, err
		}
	}

	return id, tx.Commit()
}

// SetStatus updates the status (and optionally error/timestamps) of a run.
func SetStatus(db *sql.DB, id int64, status Status, errMsg string) error {
	now := time.Now().UTC()
	switch status {
	case StatusRunning:
		_, err := db.Exec(
			`UPDATE backup_runs SET status=?, started_at=? WHERE id=?`,
			status, now, id,
		)
		return err
	case StatusDone, StatusFailed:
		_, err := db.Exec(
			`UPDATE backup_runs SET status=?, finished_at=?, error=? WHERE id=?`,
			status, now, errMsg, id,
		)
		return err
	default:
		_, err := db.Exec(`UPDATE backup_runs SET status=? WHERE id=?`, status, id)
		return err
	}
}

// LastIncomplete returns the most recent pending or failed run for a VM, or nil.
func LastIncomplete(db *sql.DB, vmName string) (*Run, error) {
	row := db.QueryRow(
		`SELECT id, dest, status, error FROM backup_runs
		 WHERE vm_name=? AND status IN ('pending','failed')
		 ORDER BY id DESC LIMIT 1`,
		vmName,
	)
	var r Run
	var errStr sql.NullString
	err := row.Scan(&r.ID, &r.Dest, &r.Status, &errStr)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.VMName = vmName
	r.Error = errStr.String
	dirs, err := loadDirs(db, r.ID)
	if err != nil {
		return nil, err
	}
	r.Dirs = dirs
	return &r, nil
}

// ListRuns returns all backup runs for a VM, newest first.
func ListRuns(db *sql.DB, vmName string) ([]*Run, error) {
	rows, err := db.Query(
		`SELECT id, dest, status, started_at, finished_at, error
		 FROM backup_runs WHERE vm_name=? ORDER BY id DESC`,
		vmName,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var runs []*Run
	for rows.Next() {
		var r Run
		var startedAt, finishedAt sql.NullString
		var errStr sql.NullString
		if err := rows.Scan(&r.ID, &r.Dest, &r.Status, &startedAt, &finishedAt, &errStr); err != nil {
			return nil, err
		}
		r.VMName = vmName
		r.Error = errStr.String
		if startedAt.Valid {
			t, _ := time.Parse(time.RFC3339, startedAt.String)
			r.StartedAt = &t
		}
		if finishedAt.Valid {
			t, _ := time.Parse(time.RFC3339, finishedAt.String)
			r.FinishedAt = &t
		}
		runs = append(runs, &r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, r := range runs {
		dirs, err := loadDirs(db, r.ID)
		if err != nil {
			return nil, err
		}
		r.Dirs = dirs
	}
	return runs, nil
}

func loadDirs(db *sql.DB, runID int64) ([]string, error) {
	rows, err := db.Query(`SELECT guest_path FROM backup_dirs WHERE run_id=? ORDER BY rowid`, runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var dirs []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		dirs = append(dirs, p)
	}
	return dirs, rows.Err()
}
