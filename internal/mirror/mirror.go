// Package mirror manages a host-side pacman package caching proxy (pacoloco)
// so guest VMs do not re-download identical packages from upstream Arch
// mirrors on every first boot.
//
// The daemon runs as a systemd user unit (vee-pacoloco.service) bound to
// 127.0.0.1:9129. Guests in QEMU user-mode networking reach the host via the
// well-known NAT gateway 10.0.2.2, so guest pacman is pointed at
// http://10.0.2.2:9129/repo/archlinux/$repo/os/$arch.
package mirror

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	// UnitName is the systemd --user service that runs pacoloco.
	UnitName = "vee-pacoloco.service"

	// DefaultPort is pacoloco's listener port.
	DefaultPort = 9129

	// GuestGatewayAddress is the address guests use to reach the host's
	// pacoloco when running in QEMU user-mode networking.
	GuestGatewayAddress = "10.0.2.2"

	// RepoName is the pacoloco "repo" identifier used in the URL path.
	// http://<host>:<port>/repo/<RepoName>/$repo/os/$arch
	RepoName = "archlinux"
)

// Paths gathers all on-disk locations pacoloco uses.
type Paths struct {
	BinDir     string // ~/.vee/bin
	BinPath    string // ~/.vee/bin/pacoloco
	ConfigDir  string // ~/.config/vee/mirror
	ConfigPath string // ~/.config/vee/mirror/pacoloco.yaml
	CacheDir   string // ~/.cache/vee/mirror/pacoloco
	UnitDir    string // ~/.config/systemd/user
	UnitPath   string // ~/.config/systemd/user/vee-pacoloco.service
}

// ResolvePaths returns the canonical pacoloco paths for the current user.
func ResolvePaths() (*Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("user home: %w", err)
	}
	binDir := filepath.Join(home, ".vee", "bin")
	cfgDir := filepath.Join(home, ".config", "vee", "mirror")
	cacheDir := filepath.Join(home, ".cache", "vee", "mirror", "pacoloco")
	unitDir := filepath.Join(home, ".config", "systemd", "user")
	return &Paths{
		BinDir:     binDir,
		BinPath:    filepath.Join(binDir, "pacoloco"),
		ConfigDir:  cfgDir,
		ConfigPath: filepath.Join(cfgDir, "pacoloco.yaml"),
		CacheDir:   cacheDir,
		UnitDir:    unitDir,
		UnitPath:   filepath.Join(unitDir, UnitName),
	}, nil
}

// GuestMirrorURL returns the URL guests should use as their pacman Server line.
// addr is "host:port"; pass "" to use the default gateway.
func GuestMirrorURL(addr string) string {
	if addr == "" {
		addr = fmt.Sprintf("%s:%d", GuestGatewayAddress, DefaultPort)
	}
	return fmt.Sprintf("http://%s/repo/%s/$repo/os/$arch", addr, RepoName)
}

// Install ensures the binary, config, cache dir, and systemd unit exist, then
// runs `systemctl --user daemon-reload`. It is idempotent.
func Install(ctx context.Context) (*Paths, error) {
	p, err := ResolvePaths()
	if err != nil {
		return nil, err
	}
	for _, dir := range []string{p.BinDir, p.ConfigDir, p.CacheDir, p.UnitDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	if _, err := EnsureBinary(ctx, p); err != nil {
		return nil, err
	}
	if err := WriteConfig(p); err != nil {
		return nil, err
	}
	if err := WriteUnit(p); err != nil {
		return nil, err
	}
	if err := systemctlUser(ctx, "daemon-reload"); err != nil {
		return nil, fmt.Errorf("systemctl daemon-reload: %w", err)
	}
	return p, nil
}

// Start enables and starts the pacoloco user unit.
func Start(ctx context.Context) error {
	if _, err := Install(ctx); err != nil {
		return err
	}
	return systemctlUser(ctx, "enable", "--now", UnitName)
}

// Stop disables and stops the pacoloco user unit. Missing units are ignored.
func Stop(ctx context.Context) error {
	// `disable --now` stops + disables in one call. We ignore errors when the
	// unit is not installed.
	_ = systemctlUser(ctx, "disable", "--now", UnitName)
	return nil
}

// Status reports whether the unit is active.
type Status struct {
	Installed bool
	Active    bool
	CacheSize int64 // bytes on disk in CacheDir
	Paths     *Paths
}

// GetStatus returns the current state of the pacoloco unit and cache.
func GetStatus(ctx context.Context) (*Status, error) {
	p, err := ResolvePaths()
	if err != nil {
		return nil, err
	}
	s := &Status{Paths: p}
	if _, err := os.Stat(p.UnitPath); err == nil {
		s.Installed = true
	}
	cmd := exec.CommandContext(ctx, "systemctl", "--user", "is-active", UnitName)
	out, _ := cmd.Output()
	s.Active = strings.TrimSpace(string(out)) == "active"
	s.CacheSize = dirSize(p.CacheDir)
	return s, nil
}

// Purge removes the pacoloco cache directory contents.
func Purge() error {
	p, err := ResolvePaths()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(p.CacheDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		full := filepath.Join(p.CacheDir, e.Name())
		if err := os.RemoveAll(full); err != nil {
			return err
		}
	}
	return nil
}

func systemctlUser(ctx context.Context, args ...string) error {
	full := append([]string{"--user"}, args...)
	cmd := exec.CommandContext(ctx, "systemctl", full...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	return nil
}

func dirSize(root string) int64 {
	var total int64
	_ = filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}
