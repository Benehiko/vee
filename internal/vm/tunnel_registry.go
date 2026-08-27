package vm

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// BindLoopback and BindHost are the two addresses a background tunnel can
// listen on. Loopback is the default and matches the foreground `vee tunnel`
// behaviour; host binds 0.0.0.0 so any device on the LAN can reach the
// service, which is opt-in per tunnel via `--host`.
const (
	BindLoopback = "127.0.0.1"
	BindHost     = "0.0.0.0"
)

// TunnelSpec is one persisted background tunnel: a VM service that the daemon
// keeps proxied for as long as the VM is running, re-establishing it on host
// boot once the guest comes up.
//
// Port is the host port the proxy listens on. It is recorded rather than
// re-picked on every daemon start so a bookmarked URL and any DNS record
// pointing at it stay valid across reboots.
type TunnelSpec struct {
	VM      string `json:"vm"`
	Service string `json:"service"`
	Bind    string `json:"bind"`
	Port    int    `json:"port"`
	// Hostname is the vhost label the daemon's router matches on, defaulting
	// to the service name. Two VMs can expose a service with the same name,
	// so this is what disambiguates them in the routing table.
	Hostname string `json:"hostname,omitempty"`
	// Protocol is recorded at registration so `--list` can render a usable
	// URL for a tunnel whose VM is stopped, where the service list the
	// protocol would otherwise come from is unavailable.
	Protocol ServiceProtocol `json:"protocol,omitempty"`
}

// Key uniquely identifies a tunnel. A VM may background several services, but
// only one tunnel per (VM, service) pair.
func (t TunnelSpec) Key() string { return t.VM + "/" + t.Service }

// BindAddr returns the address the proxy listens on, defaulting to loopback
// for specs written before Bind existed or hand-edited without it.
func (t TunnelSpec) BindAddr() string {
	if t.Bind == BindHost {
		return BindHost
	}
	return BindLoopback
}

// VHost returns the hostname label this tunnel is routed under.
func (t TunnelSpec) VHost() string {
	if t.Hostname != "" {
		return t.Hostname
	}
	return t.Service
}

// TunnelRegistry is the on-disk set of background tunnels. It lives beside the
// daemon control socket in ~/.vee rather than under a single VM's directory,
// because the daemon reconciles the whole set at once and `vee tunnel --list`
// must be able to read it without knowing which VMs exist.
type TunnelRegistry struct {
	mu   sync.Mutex
	path string
}

// TunnelRegistryPath returns the path to the background-tunnel registry file.
// tunnelRegistryPath overrides it when set, which tests use to point a Manager
// at a temporary registry without a full provider.
func (m *Manager) TunnelRegistryPath() string {
	if m.tunnelRegistryPath != "" {
		return m.tunnelRegistryPath
	}
	return filepath.Join(filepath.Dir(m.storagePath()), "tunnels.json")
}

// Tunnels returns the registry of background tunnels.
func (m *Manager) Tunnels() *TunnelRegistry {
	return &TunnelRegistry{path: m.TunnelRegistryPath()}
}

// List returns every registered tunnel, sorted by VM then service so CLI
// output and the router's route table are stable across calls.
func (r *TunnelRegistry) List() ([]TunnelSpec, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.load()
}

func (r *TunnelRegistry) load() ([]TunnelSpec, error) {
	data, err := os.ReadFile(r.path) //nolint:gosec // path is vee-owned, derived from the configured storage path.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read tunnel registry: %w", err)
	}
	var specs []TunnelSpec
	if err := json.Unmarshal(data, &specs); err != nil {
		return nil, fmt.Errorf("parse tunnel registry %s: %w", r.path, err)
	}
	sortSpecs(specs)
	return specs, nil
}

func sortSpecs(specs []TunnelSpec) {
	sort.Slice(specs, func(i, j int) bool {
		if specs[i].VM != specs[j].VM {
			return specs[i].VM < specs[j].VM
		}
		return specs[i].Service < specs[j].Service
	})
}

// Add registers a tunnel, replacing any existing entry for the same VM and
// service so re-running `vee tunnel <vm> <svc> --background` with different
// flags updates in place instead of accumulating duplicates.
func (r *TunnelRegistry) Add(spec TunnelSpec) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	specs, err := r.load()
	if err != nil {
		return err
	}
	out := make([]TunnelSpec, 0, len(specs)+1)
	for _, s := range specs {
		if s.Key() != spec.Key() {
			out = append(out, s)
		}
	}
	out = append(out, spec)
	return r.save(out)
}

// Remove deletes the tunnel for vmName/service. It reports whether an entry
// was actually removed so the CLI can tell "unregistered" from "was not
// registered".
func (r *TunnelRegistry) Remove(vmName, service string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	specs, err := r.load()
	if err != nil {
		return false, err
	}
	key := TunnelSpec{VM: vmName, Service: service}.Key()
	out := make([]TunnelSpec, 0, len(specs))
	var removed bool
	for _, s := range specs {
		if s.Key() == key {
			removed = true
			continue
		}
		out = append(out, s)
	}
	if !removed {
		return false, nil
	}
	return removed, r.save(out)
}

// RemoveVM deletes every tunnel belonging to a VM. Called when a VM is
// deleted so the registry does not keep resurrecting proxies for a guest that
// no longer exists.
func (r *TunnelRegistry) RemoveVM(vmName string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	specs, err := r.load()
	if err != nil {
		return err
	}
	out := make([]TunnelSpec, 0, len(specs))
	for _, s := range specs {
		if s.VM != vmName {
			out = append(out, s)
		}
	}
	if len(out) == len(specs) {
		return nil
	}
	return r.save(out)
}

// save writes atomically: the daemon reads this file on a 5s tick, so a
// partially-written file would be parsed as a corrupt registry and drop every
// tunnel until the next write.
func (r *TunnelRegistry) save(specs []TunnelSpec) error {
	sortSpecs(specs)
	if specs == nil {
		specs = []TunnelSpec{}
	}
	data, err := json.MarshalIndent(specs, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal tunnel registry: %w", err)
	}
	data = append(data, '\n')

	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return fmt.Errorf("create tunnel registry dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(r.path), ".tunnels-*.json")
	if err != nil {
		return fmt.Errorf("create tunnel registry temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write tunnel registry: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close tunnel registry: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("chmod tunnel registry: %w", err)
	}
	if err := os.Rename(tmpName, r.path); err != nil {
		return fmt.Errorf("replace tunnel registry: %w", err)
	}
	return nil
}

// SanitizeHostname normalizes a vhost label to the characters a DNS label
// allows, so a service named "Home Assistant" still yields a resolvable name.
// Returns "" when nothing usable remains.
func SanitizeHostname(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == ' ', r == '_', r == '.':
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
