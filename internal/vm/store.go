package vm

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const (
	configFile = "vm.yaml"
	stateFile  = "state.json"
)

func vmDir(storagePath, name string) string {
	return filepath.Join(storagePath, name)
}

func SaveConfig(storagePath string, cfg *VMConfig) error {
	dir := vmDir(storagePath, cfg.Name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	return atomicWrite(filepath.Join(dir, configFile), func() ([]byte, error) {
		return yaml.Marshal(cfg)
	})
}

func LoadConfig(storagePath, name string) (*VMConfig, error) {
	path := filepath.Join(vmDir(storagePath, name), configFile)
	data, err := os.ReadFile(path) //nolint:gosec // path derived from vee storage dir + VM name, not untrusted input
	if err != nil {
		return nil, err
	}
	var cfg VMConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func SaveState(storagePath string, state *VMState) error {
	dir := vmDir(storagePath, "")
	_ = dir
	return nil
}

func SaveStateForVM(storagePath, name string, state *VMState) error {
	dir := vmDir(storagePath, name)
	return atomicWrite(filepath.Join(dir, stateFile), func() ([]byte, error) {
		return json.Marshal(state)
	})
}

func LoadState(storagePath, name string) (*VMState, error) {
	path := filepath.Join(vmDir(storagePath, name), stateFile)
	data, err := os.ReadFile(path) //nolint:gosec // path derived from vee storage dir + VM name, not untrusted input
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &VMState{Running: false}, nil
		}
		return nil, err
	}
	var state VMState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func ClearState(storagePath, name string) error {
	path := filepath.Join(vmDir(storagePath, name), stateFile)
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func ListAll(storagePath string) ([]*VMConfig, error) {
	entries, err := os.ReadDir(storagePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var configs []*VMConfig
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		cfg, err := LoadConfig(storagePath, e.Name())
		if err != nil {
			continue
		}
		configs = append(configs, cfg)
	}
	return configs, nil
}

// freeTCPPort asks the kernel for a free TCP port on localhost.
func freeTCPPort() (int, error) {
	//nolint:noctx // ephemeral port probe; no ctx available and adding one requires an API change across callers
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port, nil
}

// isPortInUse returns true if something is already listening on the given TCP port.
func isPortInUse(port int) bool {
	//nolint:noctx // port-in-use probe; no ctx available and adding one requires an API change across callers
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return true
	}
	_ = ln.Close()
	return false
}

func atomicWrite(path string, marshal func() ([]byte, error)) error {
	data, err := marshal()
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
