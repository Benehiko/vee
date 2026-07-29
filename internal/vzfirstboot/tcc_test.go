package vzfirstboot

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// tccSchema is the access table of a real macOS 26 guest, as reported by
// PRAGMA table_info(access) on a freshly restored guest.
const tccSchema = `CREATE TABLE access (
	service TEXT,
	client TEXT,
	client_type INTEGER,
	auth_value INTEGER,
	auth_reason INTEGER,
	auth_version INTEGER,
	csreq BLOB,
	policy_id INTEGER,
	indirect_object_identifier_type INTEGER,
	indirect_object_identifier TEXT,
	indirect_object_code_identity BLOB,
	flags INTEGER,
	last_modified INTEGER,
	pid INTEGER,
	pid_version INTEGER,
	boot_uuid TEXT,
	last_reminded INTEGER,
	PRIMARY KEY (service, client, client_type, indirect_object_identifier)
);`

// newGuestVolume builds a mounted-Data-volume lookalike containing an empty
// privacy database with the guest's schema.
func newGuestVolume(t *testing.T) string {
	t.Helper()
	mnt := t.TempDir()
	dbPath := filepath.Join(mnt, tccDBPath)
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), tccSchema); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return mnt
}

// grantedServices returns the services authorized for the screen-sharing agent.
func grantedServices(t *testing.T, mnt string) map[string]int {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(mnt, tccDBPath))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(t.Context(), `SELECT service, auth_value FROM access WHERE client = ?`, screenSharingClient)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int{}
	for rows.Next() {
		var service string
		var auth int
		if err := rows.Scan(&service, &auth); err != nil {
			t.Fatal(err)
		}
		out[service] = auth
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestApplyPrivacyGrants(t *testing.T) {
	mnt := newGuestVolume(t)

	if err := applyPrivacyGrants(t.Context(), mnt, Options{User: "vee", EnableScreenSharing: true}); err != nil {
		t.Fatalf("applyPrivacyGrants: %v", err)
	}
	got := grantedServices(t, mnt)
	for _, service := range screenSharingServices {
		auth, ok := got[service]
		if !ok {
			t.Errorf("%s was not granted", service)
			continue
		}
		if auth != 2 {
			t.Errorf("%s auth_value = %d, want 2 (allowed)", service, auth)
		}
	}

	// Re-patching an image must not fail or duplicate rows.
	if err := applyPrivacyGrants(t.Context(), mnt, Options{User: "vee", EnableScreenSharing: true}); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if again := grantedServices(t, mnt); len(again) != len(got) {
		t.Errorf("rows after re-apply = %d, want %d", len(again), len(got))
	}

	// No write-ahead files may be left behind: they would belong to the host
	// user on a volume whose files belong to root.
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(filepath.Join(mnt, tccDBPath+suffix)); err == nil {
			t.Errorf("left a %s file behind", suffix)
		}
	}
}

func TestPrivacyGrantsSkippedWhenScreenSharingIsOff(t *testing.T) {
	mnt := newGuestVolume(t)

	if err := applyPrivacyGrants(t.Context(), mnt, Options{User: "vee"}); err != nil {
		t.Fatalf("applyPrivacyGrants: %v", err)
	}
	if got := grantedServices(t, mnt); len(got) != 0 {
		t.Errorf("granted %v with Screen Sharing disabled; a guest reached only over SSH should get none", got)
	}
}

func TestDropPrivacyGrantsRestoresTheDatabase(t *testing.T) {
	mnt := newGuestVolume(t)
	opts := Options{User: "vee", EnableScreenSharing: true}

	if err := applyPrivacyGrants(t.Context(), mnt, opts); err != nil {
		t.Fatal(err)
	}
	if len(grantedServices(t, mnt)) == 0 {
		t.Fatal("nothing was granted, so the revoke test proves nothing")
	}
	if err := dropPrivacyGrants(t.Context(), mnt, opts); err != nil {
		t.Fatalf("dropPrivacyGrants: %v", err)
	}
	if got := grantedServices(t, mnt); len(got) != 0 {
		t.Errorf("grants survived the rollback: %v", got)
	}
}

func TestPrivacyGrantsLeaveOtherRowsAlone(t *testing.T) {
	mnt := newGuestVolume(t)
	opts := Options{User: "vee", EnableScreenSharing: true}

	db, err := sql.Open("sqlite", filepath.Join(mnt, tccDBPath))
	if err != nil {
		t.Fatal(err)
	}
	// A grant the guest's owner made for something else entirely.
	if _, err := db.ExecContext(t.Context(), `INSERT INTO access (service, client, client_type, auth_value, auth_reason, auth_version)
		VALUES ('kTCCServiceMicrophone', 'com.example.app', 0, 2, 2, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if err := applyPrivacyGrants(t.Context(), mnt, opts); err != nil {
		t.Fatal(err)
	}
	if err := dropPrivacyGrants(t.Context(), mnt, opts); err != nil {
		t.Fatal(err)
	}

	db, err = sql.Open("sqlite", filepath.Join(mnt, tccDBPath))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var count int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM access WHERE client = 'com.example.app'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("unrelated grant count = %d, want 1 — vee must only touch its own rows", count)
	}
}

func TestPrivacyGrantsWithoutDatabase(t *testing.T) {
	opts := Options{User: "vee", EnableScreenSharing: true}

	// A guest with no privacy database at all must produce ErrNoTCCDB, which
	// the caller downgrades to a warning rather than failing the patch.
	err := applyPrivacyGrants(t.Context(), t.TempDir(), opts)
	if !errors.Is(err, ErrNoTCCDB) {
		t.Errorf("missing database: got %v, want ErrNoTCCDB", err)
	}

	// A database whose schema vee does not recognize is a different case: it
	// never becomes usable, so the start path must stop retrying rather than
	// attach the guest disk forever.
	mnt := t.TempDir()
	dbPath := filepath.Join(mnt, tccDBPath)
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `CREATE TABLE unrelated (x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	err = applyPrivacyGrants(t.Context(), mnt, opts)
	if !errors.Is(err, ErrUnknownTCCSchema) {
		t.Errorf("unknown schema: got %v, want ErrUnknownTCCSchema", err)
	}
	if errors.Is(err, ErrNoTCCDB) {
		t.Error("an unrecognized schema reported as a missing database, so the start path would retry it forever")
	}
}

func TestScreenSharingGrantedReportsState(t *testing.T) {
	mnt := newGuestVolume(t)
	opts := Options{User: "vee", EnableScreenSharing: true}

	// An empty database — what macOS creates on first boot — is not granted.
	granted, err := screenSharingGranted(t.Context(), mnt)
	if err != nil {
		t.Fatalf("screenSharingGranted: %v", err)
	}
	if granted {
		t.Error("an empty database reported as granted")
	}

	if err := applyPrivacyGrants(t.Context(), mnt, opts); err != nil {
		t.Fatal(err)
	}
	granted, err = screenSharingGranted(t.Context(), mnt)
	if err != nil {
		t.Fatalf("screenSharingGranted after apply: %v", err)
	}
	if !granted {
		t.Error("grants were applied but not reported as granted")
	}

	// A partial state must not count, whether the row is missing or present
	// and denied — a guest whose owner said no is not a granted guest.
	for _, tc := range []struct {
		name string
		stmt string
	}{
		{"row deleted", `DELETE FROM access WHERE service = ?`},
		{"row denied", `UPDATE access SET auth_value = 0 WHERE service = ?`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mnt := newGuestVolume(t)
			if err := applyPrivacyGrants(t.Context(), mnt, opts); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", filepath.Join(mnt, tccDBPath))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(t.Context(), tc.stmt, screenSharingServices[0]); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if granted, err := screenSharingGranted(t.Context(), mnt); err != nil || granted {
				t.Errorf("partial grants reported as complete (granted=%v, err=%v)", granted, err)
			}
		})
	}
}

func TestScreenSharingGrantedIgnoresUnrelatedRows(t *testing.T) {
	// The access table's primary key is wider than (service, client), so a
	// guest can hold more than one row per service. An allowed row that is not
	// the one vee writes must not answer for vee's own grant, or a guest would
	// be recorded as done while Screen Sharing stays refused.
	mnt := newGuestVolume(t)
	db, err := sql.Open("sqlite", filepath.Join(mnt, tccDBPath))
	if err != nil {
		t.Fatal(err)
	}
	for _, service := range screenSharingServices {
		// client_type 1 is a path-keyed client, not the bundle identifier vee
		// grants, and the indirect object makes it a distinct primary key.
		if _, err := db.ExecContext(t.Context(), `INSERT INTO access
			(service, client, client_type, auth_value, auth_reason, auth_version, indirect_object_identifier)
			VALUES (?, ?, 1, 2, 2, 1, 'com.example.other')`, service, screenSharingClient); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	granted, err := screenSharingGranted(t.Context(), mnt)
	if err != nil {
		t.Fatal(err)
	}
	if granted {
		t.Error("an unrelated allowed row was read as vee's grant")
	}
}

func TestScreenSharingGrantedWithoutDatabase(t *testing.T) {
	// The pre-first-boot state must be distinguishable, since the start path
	// treats it as "retry later" rather than an error worth reporting.
	_, err := screenSharingGranted(t.Context(), t.TempDir())
	if !errors.Is(err, ErrNoTCCDB) {
		t.Errorf("missing database: got %v, want ErrNoTCCDB", err)
	}
}
