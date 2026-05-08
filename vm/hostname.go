package vm

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// RegisterHostname maps hostname → ip via systemd-resolved (if active) or /etc/hosts.
func RegisterHostname(hostname, ip string) error {
	if isSystemdResolved() {
		return registerResolvectl(hostname, ip)
	}
	return registerEtcHosts(hostname, ip)
}

// UnregisterHostname removes a hostname entry added by RegisterHostname.
func UnregisterHostname(hostname string) error {
	if isSystemdResolved() {
		return unregisterResolvectl(hostname)
	}
	return unregisterEtcHosts(hostname)
}

func isSystemdResolved() bool {
	out, err := exec.Command("systemctl", "is-active", "--quiet", "systemd-resolved").CombinedOutput()
	_ = out
	return err == nil
}

// registerResolvectl adds a static DNS entry via a loopback dummy interface.
// It creates a per-VM dummy network interface named vee-<hostname> so that
// resolvectl can bind the record to a specific interface and remove it cleanly.
func registerResolvectl(hostname, ip string) error {
	iface := "vee-" + hostname

	// Create dummy interface (idempotent — ignore error if already exists).
	_ = exec.Command("sudo", "ip", "link", "add", iface, "type", "dummy").Run()
	if err := exec.Command("sudo", "ip", "link", "set", iface, "up").Run(); err != nil {
		return fmt.Errorf("bring up dummy interface %s: %w", iface, err)
	}

	// Point the interface's DNS at the VM IP.
	if err := exec.Command("sudo", "resolvectl", "dns", iface, ip).Run(); err != nil {
		return fmt.Errorf("resolvectl dns: %w", err)
	}
	// Bind the hostname to this interface's domain.
	if err := exec.Command("sudo", "resolvectl", "domain", iface, "~"+hostname).Run(); err != nil {
		return fmt.Errorf("resolvectl domain: %w", err)
	}
	return nil
}

func unregisterResolvectl(hostname string) error {
	iface := "vee-" + hostname
	_ = exec.Command("sudo", "ip", "link", "delete", iface).Run()
	return nil
}

const (
	hostsFile   = "/etc/hosts"
	hostsMarker = "# vee-managed"
)

func registerEtcHosts(hostname, ip string) error {
	// Remove any existing entry for this hostname first.
	if err := removeHostsEntry(hostname); err != nil {
		return err
	}
	entry := fmt.Sprintf("%s\t%s\t%s %s\n", ip, hostname, hostname+".local", hostsMarker)
	cmd := exec.Command("sudo", "tee", "-a", hostsFile)
	cmd.Stdin = strings.NewReader(entry)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("write /etc/hosts: %w: %s", err, out)
	}
	return nil
}

func unregisterEtcHosts(hostname string) error {
	return removeHostsEntry(hostname)
}

// removeHostsEntry removes lines containing hostname and the vee marker.
func removeHostsEntry(hostname string) error {
	f, err := os.Open(hostsFile)
	if err != nil {
		return fmt.Errorf("open %s: %w", hostsFile, err)
	}
	var kept []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		// Drop lines that match both our marker and the hostname.
		if strings.Contains(line, hostsMarker) && strings.Contains(line, hostname) {
			continue
		}
		kept = append(kept, line)
	}
	_ = f.Close()
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read %s: %w", hostsFile, err)
	}

	content := strings.Join(kept, "\n") + "\n"
	cmd := exec.Command("sudo", "tee", hostsFile)
	cmd.Stdin = strings.NewReader(content)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("write %s: %w: %s", hostsFile, err, out)
	}
	return nil
}
