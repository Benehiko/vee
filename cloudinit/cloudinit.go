package cloudinit

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// WriteFile describes a file to drop on the VM's first boot.
type WriteFile struct {
	Path        string
	Content     string
	Permissions string
}

// Config defines the cloud-init user-data for first-boot configuration.
type Config struct {
	Hostname string
	User     string
	// SSHKeys are added to the user's authorized_keys.
	SSHKeys []string
	// Packages to install on first boot.
	Packages []string
	// RunCmds are shell commands run after package installation.
	RunCmds []string
	// WriteFiles are files to create on the VM before runcmd.
	WriteFiles []WriteFile
}

// Generate writes a cloud-init cidata ISO to vmDir/cidata.iso.
// The ISO is built with xorriso (preferred) or genisoimage.
// Returns the absolute path to the ISO file.
func Generate(vmDir string, cfg *Config) (string, error) {
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		return "", err
	}

	userData, err := renderUserData(cfg)
	if err != nil {
		return "", err
	}
	metaData := renderMetaData(cfg)

	udPath := filepath.Join(vmDir, "user-data")
	mdPath := filepath.Join(vmDir, "meta-data")
	if err := os.WriteFile(udPath, []byte(userData), 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(mdPath, []byte(metaData), 0o644); err != nil {
		return "", err
	}

	isoPath := filepath.Join(vmDir, "cidata.iso")
	if err := buildISO(isoPath, udPath, mdPath); err != nil {
		return "", err
	}
	return isoPath, nil
}

func renderUserData(cfg *Config) (string, error) {
	var sb strings.Builder
	sb.WriteString("#cloud-config\n")

	if cfg.Hostname != "" {
		fmt.Fprintf(&sb, "hostname: %s\n", cfg.Hostname)
	}

	if cfg.User != "" {
		sb.WriteString("users:\n")
		fmt.Fprintf(&sb, "  - name: %s\n", cfg.User)
		sb.WriteString("    sudo: ALL=(ALL) NOPASSWD:ALL\n")
		sb.WriteString("    shell: /bin/bash\n")
		if len(cfg.SSHKeys) > 0 {
			sb.WriteString("    ssh_authorized_keys:\n")
			for _, k := range cfg.SSHKeys {
				fmt.Fprintf(&sb, "      - %s\n", k)
			}
		}
	}

	if len(cfg.WriteFiles) > 0 {
		sb.WriteString("write_files:\n")
		for _, wf := range cfg.WriteFiles {
			fmt.Fprintf(&sb, "  - path: %s\n", wf.Path)
			perms := wf.Permissions
			if perms == "" {
				perms = "0644"
			}
			fmt.Fprintf(&sb, "    permissions: '%s'\n", perms)
			sb.WriteString("    content: |\n")
			for line := range strings.SplitSeq(wf.Content, "\n") {
				fmt.Fprintf(&sb, "      %s\n", line)
			}
		}
	}

	if len(cfg.Packages) > 0 {
		sb.WriteString("packages:\n")
		for _, p := range cfg.Packages {
			fmt.Fprintf(&sb, "  - %s\n", p)
		}
		sb.WriteString("package_update: true\n")
		sb.WriteString("package_upgrade: false\n")
	}

	if len(cfg.RunCmds) > 0 {
		sb.WriteString("runcmd:\n")
		for _, c := range cfg.RunCmds {
			fmt.Fprintf(&sb, "  - %s\n", c)
		}
	}

	return sb.String(), nil
}

func renderMetaData(cfg *Config) string {
	hostname := cfg.Hostname
	if hostname == "" {
		hostname = "vee-vm"
	}
	return fmt.Sprintf("instance-id: %s\nlocal-hostname: %s\n", hostname, hostname)
}

func buildISO(isoPath, udPath, mdPath string) error {
	// Prefer xorriso, fall back to genisoimage.
	tool, args := isoTool(isoPath, udPath, mdPath)
	cmd := exec.Command(tool, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w\n%s", tool, err, out)
	}
	return nil
}

func isoTool(isoPath, udPath, mdPath string) (string, []string) {
	if _, err := exec.LookPath("xorriso"); err == nil {
		return "xorriso", []string{
			"-as", "mkisofs",
			"-output", isoPath,
			"-volid", "cidata",
			"-joliet", "-rock",
			udPath, mdPath,
		}
	}
	// genisoimage fallback
	return "genisoimage", []string{
		"-output", isoPath,
		"-volid", "cidata",
		"-joliet", "-rock",
		udPath, mdPath,
	}
}
