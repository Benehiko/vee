package cloudinit

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
// The ISO is built with xorriso (preferred) or genisoimage; on macOS, where
// neither ships by default, it falls back to hdiutil (part of the base OS).
// Returns the absolute path to the ISO file.
func Generate(vmDir string, cfg *Config) (string, error) {
	if err := os.MkdirAll(vmDir, 0o750); err != nil {
		return "", err
	}

	userData, err := renderUserData(cfg)
	if err != nil {
		return "", err
	}
	metaData := renderMetaData(cfg)

	udPath := filepath.Join(vmDir, "user-data")
	mdPath := filepath.Join(vmDir, "meta-data")
	if err := os.WriteFile(udPath, []byte(userData), 0o600); err != nil {
		return "", err
	}
	if err := os.WriteFile(mdPath, []byte(metaData), 0o600); err != nil {
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
	if tool, args, ok := mkisofsTool(isoPath, udPath, mdPath); ok {
		return runISOTool(tool, args)
	}
	// macOS ships neither xorriso nor genisoimage, but hdiutil (part of the base
	// OS) can build the same ISO9660/Joliet image, so use it as a fallback there.
	if runtime.GOOS == "darwin" {
		if _, err := exec.LookPath("hdiutil"); err == nil {
			return buildISOHdiutil(isoPath, udPath, mdPath)
		}
	}
	return fmt.Errorf("no ISO build tool found: install xorriso or genisoimage " +
		"(on macOS, hdiutil is used automatically)")
}

// mkisofsTool returns the first available mkisofs-compatible tool and its args,
// or ok=false when none is on PATH. xorriso is preferred; genisoimage is the
// fallback. Both accept the seed files as explicit path arguments.
func mkisofsTool(isoPath, udPath, mdPath string) (string, []string, bool) {
	if _, err := exec.LookPath("xorriso"); err == nil {
		return "xorriso", []string{
			"-as", "mkisofs",
			"-output", isoPath,
			"-volid", "cidata",
			"-joliet", "-rock",
			udPath, mdPath,
		}, true
	}
	if _, err := exec.LookPath("genisoimage"); err == nil {
		return "genisoimage", []string{
			"-output", isoPath,
			"-volid", "cidata",
			"-joliet", "-rock",
			udPath, mdPath,
		}, true
	}
	return "", nil, false
}

// buildISOHdiutil builds the cidata ISO with macOS's hdiutil. Unlike
// xorriso/genisoimage, hdiutil images a whole directory, so the seed files are
// staged into a temporary directory that contains nothing else — otherwise
// stray files in the VM directory (the ISO itself, disk images) would leak into
// the seed and cloud-init's NoCloud datasource could read the wrong data.
func buildISOHdiutil(isoPath, udPath, mdPath string) error {
	seedDir, err := os.MkdirTemp("", "vee-cidata-")
	if err != nil {
		return fmt.Errorf("hdiutil seed dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(seedDir) }()

	for _, f := range []struct{ src, name string }{
		{udPath, "user-data"},
		{mdPath, "meta-data"},
	} {
		data, err := os.ReadFile(f.src) //nolint:gosec // src is an internally-built path under the VM directory, not user-controlled.
		if err != nil {
			return fmt.Errorf("hdiutil seed %s: %w", f.name, err)
		}
		// seedDir is an os.MkdirTemp result and f.name is one of two fixed
		// literals ("user-data"/"meta-data"); neither is user-controlled.
		dst := filepath.Join(seedDir, f.name)
		if err := os.WriteFile(dst, data, 0o600); err != nil { //nolint:gosec // dst is derived only from a temp dir and fixed literals; no path traversal is possible.
			return fmt.Errorf("hdiutil seed %s: %w", f.name, err)
		}
	}

	// makehybrid writes exactly to -o (no extension munging) and sets the ISO9660
	// volume name via -default-volume-name; -joliet preserves the lowercase
	// filenames the guest kernel needs to find user-data/meta-data.
	//
	// Unlike xorriso/genisoimage (built above with -joliet -rock), hdiutil emits
	// no Rock Ridge extension — Joliet only. That is fine for a NoCloud seed:
	// the guest reads the filenames from the Joliet descriptor and cloud-init
	// uses the files' contents, not their on-ISO POSIX perms/ownership (which is
	// all Rock Ridge would add here). See docs/macos.md for the full comparison.
	args := []string{
		"makehybrid", "-iso", "-joliet",
		"-default-volume-name", "cidata",
		"-o", isoPath,
		seedDir,
	}
	return runISOTool("hdiutil", args)
}

func runISOTool(tool string, args []string) error {
	//nolint:gosec,noctx // tool is a fixed literal ("xorriso"/"genisoimage"/"hdiutil") and args are internally-built ISO paths, not user-controlled shell input; buildISO/Generate take no ctx and threading one requires an exported-signature change plus an out-of-package caller edit for a short-lived local ISO build.
	cmd := exec.Command(tool, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w\n%s", tool, err, out)
	}
	return nil
}
