package cmd

import (
	"fmt"
	"os"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"
)

// promptDNSAdminPassword asks for the AdGuard Home admin password twice and
// returns its bcrypt hash. Only the hash is written into the VM's cloud-init
// seed, so the plaintext never lands on disk.
//
// An empty password is accepted: it leaves the AdGuard Home web UI without a
// login, which the template's firewall compensates for by restricting the UI
// port to RFC1918 sources. The choice is the operator's, so it is reported
// rather than rejected.
func promptDNSAdminPassword() (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) { //nolint:gosec // small OS file descriptor; no overflow.
		return "", fmt.Errorf("dns-sink template needs an interactive terminal to set the AdGuard Home admin password")
	}

	fmt.Fprintf(os.Stderr, "AdGuard Home admin password for user %q (empty for no UI login): ", createDNSAdminUser)
	first, err := term.ReadPassword(int(os.Stdin.Fd())) //nolint:gosec // small OS file descriptor; no overflow.
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read admin password: %w", err)
	}

	if len(first) == 0 {
		fmt.Fprintln(os.Stderr, "warning: AdGuard Home web UI will have no login; it is reachable from any LAN host.")
		return "", nil
	}

	fmt.Fprint(os.Stderr, "Confirm password: ")
	second, err := term.ReadPassword(int(os.Stdin.Fd())) //nolint:gosec // small OS file descriptor; no overflow.
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read admin password confirmation: %w", err)
	}
	if string(first) != string(second) {
		return "", fmt.Errorf("passwords do not match")
	}

	hash, err := bcrypt.GenerateFromPassword(first, bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash admin password: %w", err)
	}
	return string(hash), nil
}
