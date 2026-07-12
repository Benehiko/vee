package mirror

import (
	"fmt"
	"os"
	"strings"
)

// renderConfig produces a pacoloco YAML configuration. We define a single
// "archlinux" repo whose upstream is the geo-aware pkgbuild mirror set used
// elsewhere in vee. Multilib and other repos live under the same mirror path,
// so a single entry handles core/extra/multilib/community via pacoloco's
// `$repo` substitution.
func renderConfig(p *Paths) string {
	var b strings.Builder
	fmt.Fprintf(&b, "cache_dir: %s\n", p.CacheDir)
	fmt.Fprintf(&b, "port: %d\n", DefaultPort)
	b.WriteString("purge_files_after: 2592000 # 30 days in seconds\n")
	b.WriteString("download_timeout: 600\n")
	b.WriteString("repos:\n")
	fmt.Fprintf(&b, "  %s:\n", RepoName)
	b.WriteString("    urls:\n")
	b.WriteString("      - https://geo.mirror.pkgbuild.com\n")
	b.WriteString("      - https://mirror.rackspace.com/archlinux\n")
	return b.String()
}

// WriteConfig writes the pacoloco YAML config to p.ConfigPath.
func WriteConfig(p *Paths) error {
	if err := os.MkdirAll(p.ConfigDir, 0o750); err != nil {
		return fmt.Errorf("mkdir %s: %w", p.ConfigDir, err)
	}
	return os.WriteFile(p.ConfigPath, []byte(renderConfig(p)), 0o600)
}

// renderUnit produces the systemd --user unit text.
func renderUnit(p *Paths) string {
	return fmt.Sprintf(`[Unit]
Description=vee pacman caching proxy (pacoloco)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s -config %s
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=default.target
`, p.BinPath, p.ConfigPath)
}

// WriteUnit writes the systemd --user unit file to p.UnitPath.
func WriteUnit(p *Paths) error {
	if err := os.MkdirAll(p.UnitDir, 0o750); err != nil {
		return fmt.Errorf("mkdir %s: %w", p.UnitDir, err)
	}
	return os.WriteFile(p.UnitPath, []byte(renderUnit(p)), 0o600)
}
