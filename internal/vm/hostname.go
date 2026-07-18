package vm

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const (
	hostsFile   = "/etc/hosts"
	hostsMarker = "# vee-managed"
)

// CanWriteHosts reports whether this process can update /etc/hosts without an
// interactive prompt: either it already owns the file (root / writable) or
// passwordless sudo is available. Callers use it to skip hostname registration
// on hosts without passwordless sudo, instead of attempting it and logging a
// failure on every VM start. See https://github.com/Benehiko/vee/issues/40.
func CanWriteHosts() bool {
	if f, err := os.OpenFile(hostsFile, os.O_WRONLY|os.O_APPEND, 0); err == nil {
		_ = f.Close()
		return true
	}
	// `sudo -n -v` validates the cached credential / NOPASSWD rule without
	// running a command and without prompting; exit 0 means sudo would not block.
	//nolint:noctx // one-shot local probe; no ctx plumbing needed
	return exec.Command("sudo", "-n", "-v").Run() == nil
}

// RegisterHostname adds hostname → ip to /etc/hosts (via sudo).
func RegisterHostname(hostname, ip string) error {
	if err := removeHostsEntry(hostname); err != nil {
		return err
	}
	entry := fmt.Sprintf("%s\t%s\t%s.local %s\n", ip, hostname, hostname, hostsMarker)
	// -n: never prompt for password. If sudo would block on a password we want
	// a fast error so the caller can log+continue, not a 12-minute hang under
	// non-interactive runners (e2e tests, headless daemons).
	//nolint:noctx // RegisterHostname has no ctx; adding one changes its exported signature and all cmd/ callers
	cmd := exec.Command("sudo", "-n", "tee", "-a", hostsFile)
	cmd.Stdin = strings.NewReader(entry)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("write /etc/hosts: %w: %s", err, out)
	}
	return nil
}

// UnregisterHostname removes a vee-managed hostname entry from /etc/hosts (via sudo).
func UnregisterHostname(hostname string) error {
	return removeHostsEntry(hostname)
}

// removeHostsEntry strips lines that contain both the vee marker and hostname.
func removeHostsEntry(hostname string) error {
	f, err := os.Open(hostsFile)
	if err != nil {
		return fmt.Errorf("open %s: %w", hostsFile, err)
	}
	var kept []string
	var total int
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		total++
		line := scanner.Text()
		if strings.Contains(line, hostsMarker) && strings.Contains(line, hostname) {
			continue
		}
		kept = append(kept, line)
	}
	_ = f.Close()
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read %s: %w", hostsFile, err)
	}

	// Nothing matched — the file already lacks this entry, so there is no write
	// to perform. Short-circuit so an unregister on a host without passwordless
	// sudo (where registration was skipped) doesn't fail on the sudo write.
	if len(kept) == total {
		return nil
	}

	content := strings.Join(kept, "\n") + "\n"
	//nolint:noctx // removeHostsEntry has no ctx; adding one changes exported Register/UnregisterHostname signatures and all cmd/ callers
	cmd := exec.Command("sudo", "-n", "tee", hostsFile)
	cmd.Stdin = strings.NewReader(content)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("write %s: %w: %s", hostsFile, err, out)
	}
	return nil
}
