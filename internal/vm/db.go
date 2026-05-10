package vm

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// dbSaveConfig upserts a VMConfig into the vms table.
func dbSaveConfig(db *sql.DB, cfg *VMConfig) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	_, err = db.Exec(
		`INSERT INTO vms (name, template, config_json, created_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET template=excluded.template, config_json=excluded.config_json`,
		cfg.Name, cfg.Template, string(data), cfg.CreatedAt.UTC(),
	)
	return err
}

// dbLoadConfig reads a VMConfig from the vms table.
func dbLoadConfig(db *sql.DB, name string) (*VMConfig, error) {
	var raw string
	err := db.QueryRow(`SELECT config_json FROM vms WHERE name = ?`, name).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("VM %q not found", name)
	}
	if err != nil {
		return nil, err
	}
	var cfg VMConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	return &cfg, nil
}

// dbSaveState upserts a VMState into vm_states.
func dbSaveState(db *sql.DB, name string, state *VMState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	running := 0
	if state.Running {
		running = 1
	}
	_, err = db.Exec(
		`INSERT INTO vm_states (vm_name, running, pid, ssh_port, state_json)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(vm_name) DO UPDATE SET
		   running=excluded.running, pid=excluded.pid,
		   ssh_port=excluded.ssh_port, state_json=excluded.state_json`,
		name, running, state.PID, state.SSHPort, string(data),
	)
	return err
}

// dbLoadState reads a VMState from vm_states.
func dbLoadState(db *sql.DB, name string) (*VMState, error) {
	var raw string
	err := db.QueryRow(`SELECT state_json FROM vm_states WHERE vm_name = ?`, name).Scan(&raw)
	if err == sql.ErrNoRows {
		return &VMState{Running: false}, nil
	}
	if err != nil {
		return nil, err
	}
	var state VMState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return nil, fmt.Errorf("unmarshal state: %w", err)
	}
	return &state, nil
}

// dbListAll returns all VMConfigs from the vms table.
func dbListAll(db *sql.DB) ([]*VMConfig, error) {
	rows, err := db.Query(`SELECT config_json FROM vms ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var configs []*VMConfig
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var cfg VMConfig
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			continue
		}
		configs = append(configs, &cfg)
	}
	return configs, rows.Err()
}

// dbDeleteVM removes a VM and its state from the DB (cascade handles vm_states).
func dbDeleteVM(db *sql.DB, name string) error {
	_, err := db.Exec(`DELETE FROM vms WHERE name = ?`, name)
	return err
}

// dbEnsureVM inserts a stub vms row if one doesn't exist yet (needed before
// saving state for a newly-created VM before its full config is written).
func dbEnsureVM(db *sql.DB, name, template string) error {
	_, err := db.Exec(
		`INSERT OR IGNORE INTO vms (name, template, config_json, created_at)
		 VALUES (?, ?, '{}', ?)`,
		name, template, time.Now().UTC(),
	)
	return err
}
