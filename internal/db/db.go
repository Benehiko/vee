package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const currentVersion = 1

const schema = `
CREATE TABLE IF NOT EXISTS vms (
	name        TEXT PRIMARY KEY,
	template    TEXT NOT NULL,
	config_json TEXT NOT NULL,
	created_at  DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS vm_states (
	vm_name    TEXT PRIMARY KEY REFERENCES vms(name) ON DELETE CASCADE,
	running    INTEGER NOT NULL DEFAULT 0,
	pid        INTEGER,
	ssh_port   INTEGER,
	state_json TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS backup_runs (
	id          INTEGER PRIMARY KEY,
	vm_name     TEXT NOT NULL REFERENCES vms(name) ON DELETE CASCADE,
	dest        TEXT NOT NULL,
	status      TEXT NOT NULL CHECK(status IN ('pending','running','done','failed')),
	started_at  DATETIME,
	finished_at DATETIME,
	error       TEXT
);

CREATE TABLE IF NOT EXISTS backup_dirs (
	run_id     INTEGER NOT NULL REFERENCES backup_runs(id) ON DELETE CASCADE,
	guest_path TEXT NOT NULL
);
`

// Open opens (or creates) the vee SQLite database at the given path, applies
// the schema, and migrates existing YAML/JSON VM data from storagePath when
// upgrading from version 0.
func Open(dbPath, storagePath string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	db.SetMaxOpenConns(1)

	if _, err := db.Exec("PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("pragma: %w", err)
	}

	ver, err := userVersion(db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}

	if ver == 0 {
		if err := initSchema(db, storagePath); err != nil {
			_ = db.Close()
			return nil, err
		}
	}

	return db, nil
}

func userVersion(db *sql.DB) (int, error) {
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("user_version: %w", err)
	}
	return v, nil
}

func initSchema(db *sql.DB, storagePath string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(schema); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}

	if err := migrateFromYAML(tx, storagePath); err != nil {
		return fmt.Errorf("migrate yaml: %w", err)
	}

	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", currentVersion)); err != nil {
		return err
	}

	return tx.Commit()
}

// migrateFromYAML walks storagePath/*/vm.yaml and state.json and inserts rows.
func migrateFromYAML(tx *sql.Tx, storagePath string) error {
	entries, err := os.ReadDir(storagePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		vmDir := filepath.Join(storagePath, name)

		cfgData, err := os.ReadFile(filepath.Join(vmDir, "vm.yaml"))
		if err != nil {
			continue
		}

		// Convert YAML bytes → opaque JSON for storage.
		cfgJSON, template, createdAt, err := yamlToJSON(cfgData)
		if err != nil {
			continue
		}

		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO vms (name, template, config_json, created_at) VALUES (?,?,?,?)`,
			name, template, cfgJSON, createdAt,
		); err != nil {
			return err
		}

		// state.json — best-effort
		stateData, err := os.ReadFile(filepath.Join(vmDir, "state.json"))
		if err != nil {
			stateData = []byte(`{"running":false}`)
		}

		var stateMap map[string]any
		_ = json.Unmarshal(stateData, &stateMap)
		running := 0
		if r, ok := stateMap["running"].(bool); ok && r {
			running = 1
		}
		var pid, sshPort int
		if v, ok := stateMap["pid"].(float64); ok {
			pid = int(v)
		}
		if v, ok := stateMap["ssh_port"].(float64); ok {
			sshPort = int(v)
		}

		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO vm_states (vm_name, running, pid, ssh_port, state_json) VALUES (?,?,?,?,?)`,
			name, running, pid, sshPort, string(stateData),
		); err != nil {
			return err
		}
	}

	return nil
}
