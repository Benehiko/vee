// Package vzfirstboot prepares a freshly restored macOS guest disk image so
// the guest is usable without a GUI session: it skips Setup Assistant and
// installs a first-boot LaunchDaemon that creates an admin account, enables
// Remote Login (SSH) and Screen Sharing, and drops in vee's SSH key.
//
// Without this, an IPSW restore boots into Setup Assistant, which needs a
// human at a display — no SSH, no IP, nothing vee can drive.
//
// The mechanism follows lima's macOS guest patching (Apache-2.0,
// lima-vm/lima pkg/guestpatch/macos): attach the image without mounting,
// mount the APFS Data volume with -o noowners (writable without root), write
// the payload, then fix ownership — launchd refuses daemon plists that are
// not root-owned. vee fixes ownership with one batched sudo invocation
// rather than rewriting APFS inode records on the raw device, because that
// route depends on the on-disk owner being exactly 99:99, which does not
// hold on current macOS hosts.
package vzfirstboot

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
)

// ErrGuestDiskBusy reports that vee could not release the guest disk it
// attached to the host. Nothing else releases it, and neither
// Virtualization.framework nor a later attach can use a disk the host's
// DiskImages driver still owns, so callers must abort rather than boot the VM
// and must tell the user to run `hdiutil detach` on it.
var ErrGuestDiskBusy = errors.New("guest disk is still attached to the host")

// Options configures the guest payload.
type Options struct {
	// User is the admin account to create (e.g. "vee").
	User string
	// Password is the account's login password. Generated when empty.
	Password string
	// SSHPublicKeys are authorized for the account. Optional but strongly
	// recommended — without them only password SSH works.
	SSHPublicKeys []string
	// Hostname sets the guest's LocalHostName. Optional.
	Hostname string
	// EnableScreenSharing turns on macOS Screen Sharing (VNC on :5900) so
	// `vee view` can attach without a Setup Assistant session.
	EnableScreenSharing bool
}

// Result reports what the patch did.
type Result struct {
	// Password is the account password (generated when Options.Password was
	// empty) so the caller can persist/report it.
	Password string
}

// passwordAlphabet avoids look-alike and shifted characters: the password is
// typed by hand at the guest's GUI login window, where the keyboard layout is
// whatever macOS picked.
const passwordAlphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// GeneratePassword returns a random password of n characters.
func GeneratePassword(n int) (string, error) {
	if n <= 0 {
		return "", fmt.Errorf("password length must be > 0")
	}
	out := make([]byte, n)
	max := big.NewInt(int64(len(passwordAlphabet)))
	for i := range out {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		out[i] = passwordAlphabet[idx.Int64()]
	}
	return string(out), nil
}
