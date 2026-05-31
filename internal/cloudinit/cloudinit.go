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
	Owner       string
	// Defer defers write_files to the final modules stage so the target user
	// already exists (e.g. when injecting into a user created by cloud-init).
	Defer bool
	// Encoding maps to cloud-init's `encoding:` key (e.g. "b64") so Content may
	// carry base64-encoded binary data. Empty writes Content literally.
	Encoding string
}

// Config defines the cloud-init user-data for first-boot configuration.
type Config struct {
	Hostname string
	User     string
	// Password is the plain-text login password for User and DefaultUser.
	// Rendered via chpasswd in runcmd so console / TTY login works without an
	// SSH key. Empty means no password is set (key-only login).
	Password string
	// DefaultUser is the distro's built-in cloud image user (e.g. "ubuntu").
	// When set, SSH keys are injected into this user in addition to User.
	DefaultUser string
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

// RenderUserData returns the cloud-config user-data string for cfg.
func RenderUserData(cfg *Config) (string, error) {
	return renderUserData(cfg)
}

// RenderMetaData returns the meta-data string for cfg.
func RenderMetaData(cfg *Config) string {
	return renderMetaData(cfg)
}

func renderUserData(cfg *Config) (string, error) {
	var sb strings.Builder
	sb.WriteString("#cloud-config\n")

	if cfg.Hostname != "" {
		fmt.Fprintf(&sb, "hostname: %s\n", cfg.Hostname)
	}

	// cloud-init v25+ ignores top-level ssh_authorized_keys when a users: block
	// is present. Keys for the default user are injected via runcmd instead (see below).
	hasUsersBlock := cfg.User != "" && cfg.User != cfg.DefaultUser
	if len(cfg.SSHKeys) > 0 && !hasUsersBlock {
		// Simple case: no custom user, so top-level directive works fine.
		sb.WriteString("ssh_authorized_keys:\n")
		for _, k := range cfg.SSHKeys {
			fmt.Fprintf(&sb, "  - %s\n", k)
		}
	}

	if hasUsersBlock {
		sb.WriteString("users:\n")
		// Emit the default cloud-image user entry with SSH keys so they are set
		// during the config stage (cloud-init v25+ ignores top-level
		// ssh_authorized_keys when any users: block is present).
		// Preserve the typical default-user capabilities: sudo and docker groups.
		if cfg.DefaultUser != "" && len(cfg.SSHKeys) > 0 {
			fmt.Fprintf(&sb, "  - name: %s\n", cfg.DefaultUser)
			sb.WriteString("    sudo: ALL=(ALL) NOPASSWD:ALL\n")
			sb.WriteString("    groups: [adm, cdrom, dip, lxd, sudo, plugdev]\n")
			sb.WriteString("    shell: /bin/bash\n")
			sb.WriteString("    ssh_authorized_keys:\n")
			for _, k := range cfg.SSHKeys {
				fmt.Fprintf(&sb, "      - %s\n", k)
			}
		} else {
			sb.WriteString("  - default\n")
		}
		fmt.Fprintf(&sb, "  - name: %s\n", cfg.User)
		sb.WriteString("    sudo: ALL=(ALL) NOPASSWD:ALL\n")
		sb.WriteString("    shell: /bin/bash\n")
		// no_user_group makes useradd skip creating a per-user primary group
		// (useradd -N). Without it, a User name that collides with an existing
		// system group — e.g. "admin", which ships as a group in the Ubuntu
		// cloud image — makes useradd fail with "group <name> exists", which
		// aborts the whole users_groups module so the user is never created.
		// With -N the account joins the default group (gid 100, "users").
		sb.WriteString("    no_user_group: true\n")
		if len(cfg.SSHKeys) > 0 {
			sb.WriteString("    ssh_authorized_keys:\n")
			for _, k := range cfg.SSHKeys {
				fmt.Fprintf(&sb, "      - %s\n", k)
			}
		}
	}

	writeFiles := cfg.WriteFiles
	runCmds := cfg.RunCmds

	// Password is applied via chpasswd before any other runcmd so console /
	// TTY login is available as soon as the user exists. Apply to both the
	// custom user (when set) and the distro default user so users can log in
	// either way regardless of which identity the template promoted.
	if cfg.Password != "" {
		targets := []string{}
		if cfg.User != "" {
			targets = append(targets, cfg.User)
		}
		if cfg.DefaultUser != "" && cfg.DefaultUser != cfg.User {
			targets = append(targets, cfg.DefaultUser)
		}
		safePw := strings.ReplaceAll(cfg.Password, "'", `'\''`)
		pwCmds := make([]string, 0, len(targets))
		for _, t := range targets {
			pwCmds = append(pwCmds, fmt.Sprintf("echo '%s:%s' | chpasswd", t, safePw))
		}
		runCmds = append(pwCmds, runCmds...)
	}

	if len(writeFiles) > 0 {
		sb.WriteString("write_files:\n")
		for _, wf := range writeFiles {
			fmt.Fprintf(&sb, "  - path: %s\n", wf.Path)
			perms := wf.Permissions
			if perms == "" {
				perms = "0644"
			}
			fmt.Fprintf(&sb, "    permissions: '%s'\n", perms)
			if wf.Owner != "" {
				fmt.Fprintf(&sb, "    owner: '%s'\n", wf.Owner)
			}
			if wf.Defer {
				sb.WriteString("    defer: true\n")
			}
			if wf.Encoding != "" {
				fmt.Fprintf(&sb, "    encoding: %s\n", wf.Encoding)
			}
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

	if len(runCmds) > 0 {
		sb.WriteString("runcmd:\n")
		for _, c := range runCmds {
			if !strings.Contains(c, "\n") {
				fmt.Fprintf(&sb, "  - %s\n", c)
				continue
			}
			// Multi-line command: emit as a YAML literal block scalar.
			sb.WriteString("  - |\n")
			for line := range strings.SplitSeq(c, "\n") {
				fmt.Fprintf(&sb, "    %s\n", line)
			}
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
