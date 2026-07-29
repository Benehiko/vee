package vzfirstboot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // pure-Go driver: the manager must build without cgo
)

// tccDBPath is the guest's system privacy database, relative to the APFS Data
// volume root. It is protected by SIP while the guest runs, but an offline
// image is writable — which is the only way to grant these permissions
// without a GUI session or device management.
const tccDBPath = "Library/Application Support/com.apple.TCC/TCC.db"

// screenSharingClient is the bundle identifier of the agent that serves a
// Screen Sharing session.
const screenSharingClient = "com.apple.screensharing.agent"

// screenSharingServices are the permissions a Screen Sharing session needs.
// Enabling the service alone leaves it listening but refusing sessions with
// "Screen Sharing is not permitted on <host>", because since macOS 12.1 the
// agent also needs these privacy grants — and macOS only offers them through
// System Settings or MDM, neither of which a headless guest can reach.
var screenSharingServices = []string{
	"kTCCServiceScreenCapture",
	"kTCCServicePostEvent",
	"kTCCServiceAccessibility",
}

// ErrNoTCCDB reports that the guest has no privacy database yet, so the grants
// were skipped. macOS creates that database on the guest's first boot, so this
// is the expected state for a guest that has never run — callers retry at the
// next start rather than treating it as a failure.
var ErrNoTCCDB = errors.New("guest has no TCC database yet")

// ErrUnknownTCCSchema reports a privacy database vee does not recognize. Unlike
// ErrNoTCCDB this never resolves by itself, so callers must stop retrying
// rather than attach the guest disk at every start forever.
var ErrUnknownTCCSchema = errors.New("guest TCC database has no access table")

// applyPrivacyGrants authorizes the guest's screen-sharing agent when Screen
// Sharing was requested, and is a no-op otherwise — a guest reached only over
// SSH gets no privacy grants written at all.
//
// A missing or unrecognized privacy database is reported as ErrNoTCCDB so the
// caller can warn and carry on: everything else about the guest still works,
// and Screen Sharing falls back to needing its manual toggle.
func applyPrivacyGrants(ctx context.Context, mnt string, opts Options) error {
	if !opts.EnableScreenSharing {
		return nil
	}
	return grantScreenSharing(ctx, mnt)
}

// dropPrivacyGrants undoes applyPrivacyGrants, so a rolled-back patch leaves
// the guest's privacy database exactly as it was found.
func dropPrivacyGrants(ctx context.Context, mnt string, opts Options) error {
	if !opts.EnableScreenSharing {
		return nil
	}
	return revokeScreenSharing(ctx, mnt)
}

// grantScreenSharing authorizes the guest's screen-sharing agent by writing
// directly into the guest's privacy database. mnt is the mounted Data volume.
//
// Rows are written with a NULL csreq (the code-requirement blob macOS records
// when a user grants a permission through the UI); a macOS 26 guest accepts
// that and serves Screen Sharing sessions normally.
func grantScreenSharing(ctx context.Context, mnt string) error {
	db, closeDB, err := openTCC(ctx, mnt)
	if err != nil {
		return err
	}
	defer closeDB()

	for _, service := range screenSharingServices {
		// auth_value 2 = allowed, auth_reason 2 = set by the user,
		// client_type 0 = bundle identifier.
		_, err := db.ExecContext(ctx, `INSERT OR REPLACE INTO access
			(service, client, client_type, auth_value, auth_reason, auth_version, flags, last_modified)
			VALUES (?, ?, 0, 2, 2, 1, 0, strftime('%s','now'))`, service, screenSharingClient)
		if err != nil {
			return fmt.Errorf("grant %s to %s: %w", service, screenSharingClient, err)
		}
	}
	return nil
}

// revokeScreenSharing removes the grants written by grantScreenSharing, so a
// rolled-back patch leaves the guest's privacy database as it was.
func revokeScreenSharing(ctx context.Context, mnt string) error {
	db, closeDB, err := openTCC(ctx, mnt)
	if err != nil {
		return err
	}
	defer closeDB()

	for _, service := range screenSharingServices {
		if _, err := db.ExecContext(ctx, `DELETE FROM access WHERE service = ? AND client = ?`,
			service, screenSharingClient); err != nil {
			return fmt.Errorf("revoke %s from %s: %w", service, screenSharingClient, err)
		}
	}
	return nil
}

// openTCC opens the guest's privacy database for writing. The returned close
// function also clears any write-ahead files, which would otherwise be left
// owned by the host user on a volume whose files belong to root.
func openTCC(ctx context.Context, mnt string) (*sql.DB, func(), error) {
	path := filepath.Join(mnt, tccDBPath)
	if _, err := os.Stat(path); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrNoTCCDB, err)
	}
	// A rollback journal is deleted on commit; WAL files would survive and be
	// owned by the wrong user.
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(DELETE)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, nil, fmt.Errorf("open guest TCC database: %w", err)
	}
	closeDB := func() {
		_ = db.Close()
		for _, suffix := range []string{"-wal", "-shm"} {
			_ = os.Remove(path + suffix)
		}
	}
	// A guest whose database exists but has no access table is not one vee
	// should be writing to.
	if _, err := db.ExecContext(ctx, `SELECT 1 FROM access LIMIT 1`); err != nil {
		closeDB()
		return nil, nil, fmt.Errorf("%w: %v", ErrUnknownTCCSchema, err)
	}
	return db, closeDB, nil
}

// screenSharingGranted reports whether every permission the agent needs is
// already authorized, so a start that has nothing to do stays silent.
func screenSharingGranted(ctx context.Context, mnt string) (bool, error) {
	db, closeDB, err := openTCC(ctx, mnt)
	if err != nil {
		return false, err
	}
	defer closeDB()

	for _, service := range screenSharingServices {
		// The access table's primary key is wider than (service, client), so a
		// guest can hold several rows per service. Count only rows in the shape
		// grantScreenSharing writes — a bundle-identifier client, allowed — so
		// an unrelated row cannot report vee's own grant as done.
		var allowed int
		err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM access
			 WHERE service = ? AND client = ? AND client_type = 0 AND auth_value = 2`,
			service, screenSharingClient).Scan(&allowed)
		if err != nil {
			return false, fmt.Errorf("read %s grant: %w", service, err)
		}
		if allowed == 0 {
			return false, nil
		}
	}
	return true, nil
}
