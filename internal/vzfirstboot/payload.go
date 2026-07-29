package vzfirstboot

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

// LaunchDaemonLabel identifies the first-boot job inside the guest.
const LaunchDaemonLabel = "io.vee.firstboot"

// Guest-relative paths (from the APFS Data volume root). /private/var,
// /Library and /usr/local are firmlinked onto the Data volume, so these are
// the canonical writable locations.
const (
	// appleSetupDonePath skips the system Setup Assistant.
	appleSetupDonePath = "private/var/db/.AppleSetupDone"
	// skipBuddyPath lands in the user template so every created account
	// skips the per-user setup screens too.
	skipBuddyPath = "Library/User Template/.skipbuddy"
	// scriptPath is the first-boot script the LaunchDaemon runs.
	scriptPath = "usr/local/sbin/vee-firstboot.sh"
	// plistPath is the LaunchDaemon that runs the script.
	plistPath = "Library/LaunchDaemons/" + LaunchDaemonLabel + ".plist"
)

// launchDaemonPlist runs the script at every boot until it succeeds; the
// script removes itself (plist included) once provisioning is complete.
//
// Output goes to a guest log file rather than a console device: launchd
// refuses to spawn a job whose Standard*Path cannot be opened, and vee's vz
// helper attaches no serial console.
const launchDaemonPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + LaunchDaemonLabel + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>/usr/local/sbin/vee-firstboot.sh</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>StandardOutPath</key>
	<string>/var/log/vee-firstboot.log</string>
	<key>StandardErrorPath</key>
	<string>/var/log/vee-firstboot.log</string>
</dict>
</plist>
`

// firstBootScript provisions the guest on first boot. Every step is
// idempotent: the daemon runs at every boot until it succeeds, and a repaired
// guest must not be damaged by a second pass.
const firstBootScript = `#!/bin/sh
# Written by vee (macOS guest first boot). Safe to re-run.
set -u

log() { echo "vee-firstboot: $*"; }

USER_NAME={{.UserQ}}
USER_HOME="/Users/${USER_NAME}"
MARKER="/private/var/db/.vee-firstboot-done"

# 1. Admin account. sysadminctl is the supported non-interactive path; it
#    reads the password from stdin with -password -.
if /usr/bin/dscl . -read "/Users/${USER_NAME}" >/dev/null 2>&1; then
	log "account ${USER_NAME} already exists"
else
	log "creating admin account ${USER_NAME}"
	printf '%s' {{.PasswordQ}} | /usr/sbin/sysadminctl \
		-addUser "${USER_NAME}" -fullName "vee" -password - -admin -shell /bin/zsh \
		>/dev/null 2>&1
	# sysadminctl does not populate the home directory.
	if [ ! -d "${USER_HOME}" ]; then
		/usr/bin/ditto --noqtn "/System/Library/User Template/English.lproj" "${USER_HOME}" 2>/dev/null
	fi
	UID_NUM=$(/usr/bin/id -u "${USER_NAME}" 2>/dev/null)
	if [ -n "${UID_NUM}" ]; then
		/usr/sbin/chown -R "${UID_NUM}:staff" "${USER_HOME}"
		/bin/chmod 700 "${USER_HOME}"
	fi
	/usr/sbin/dseditgroup -o edit -a "${USER_NAME}" -t user admin 2>/dev/null
fi

{{if .SSHPublicKeys}}
# 2. Authorize the SSH keys vee will connect with.
log "installing authorized_keys"
/bin/mkdir -p "${USER_HOME}/.ssh"
/usr/bin/touch "${USER_HOME}/.ssh/authorized_keys"
{{range .SSHPublicKeysQ}}
KEY={{.}}
if ! /usr/bin/grep -qxF "${KEY}" "${USER_HOME}/.ssh/authorized_keys"; then
	echo "${KEY}" >> "${USER_HOME}/.ssh/authorized_keys"
fi
{{end}}
/bin/chmod 700 "${USER_HOME}/.ssh"
/bin/chmod 600 "${USER_HOME}/.ssh/authorized_keys"
UID_NUM=$(/usr/bin/id -u "${USER_NAME}" 2>/dev/null)
[ -n "${UID_NUM}" ] && /usr/sbin/chown -R "${UID_NUM}:staff" "${USER_HOME}/.ssh"
{{end}}

# 3. Remote Login (SSH). A fresh install ships ssh.plist disabled, so the
#    service is not in the system domain yet: enable clears the persisted
#    override for later boots, bootstrap loads it now, kickstart starts it.
#    systemsetup -setremotelogin needs Full Disk Access in a daemon context on
#    modern macOS, so it is not used.
log "enabling Remote Login"
/bin/launchctl enable system/com.openssh.sshd
/bin/launchctl bootstrap system /System/Library/LaunchDaemons/ssh.plist 2>/dev/null
/bin/launchctl kickstart system/com.openssh.sshd 2>/dev/null

{{if .EnableScreenSharing}}
# 4. Screen Sharing (VNC :5900) so the guest's screen is reachable without a
#    Virtualization.framework window. Note macOS 12.1+ also gates the
#    screen-sharing agent behind TCC grants that only MDM can create, so a
#    connection may still be refused; the guest's own display remains the
#    fallback.
log "enabling Screen Sharing"
# kickstart activates and configures Remote Management. On macOS 26 it can no
# longer grant screen recording/control by itself — it says as much ("must be
# enabled from System Settings or via MDM") — so a session may still be
# refused with "Screen Sharing is not permitted on <host>", and the remedy is
# to toggle Sharing inside the guest. It is still run because it configures
# everything it can and helps on older guests; failures are non-fatal.
ARD=/System/Library/CoreServices/RemoteManagement/ARDAgent.app/Contents/Resources/kickstart
if [ -x "${ARD}" ]; then
	"${ARD}" -activate -configure -access -on -restart -agent -privs -all -allowAccessFor -allUsers 2>/dev/null
fi
/bin/launchctl enable system/com.apple.screensharing
/bin/launchctl bootstrap system /System/Library/LaunchDaemons/com.apple.screensharing.plist 2>/dev/null
/bin/launchctl kickstart system/com.apple.screensharing 2>/dev/null
{{end}}

{{if .Hostname}}
# 5. Hostname.
/usr/sbin/scutil --set LocalHostName {{.HostnameQ}} 2>/dev/null
/usr/sbin/scutil --set ComputerName {{.HostnameQ}} 2>/dev/null
{{end}}

# 6. Keep the guest quiet: no automatic update nagging in a disposable VM.
/usr/bin/defaults write /Library/Preferences/com.apple.SoftwareUpdate AutomaticCheckEnabled -bool false 2>/dev/null

# Provisioning is complete only if the account exists and sshd is loaded.
if /usr/bin/dscl . -read "/Users/${USER_NAME}" >/dev/null 2>&1 &&
	/bin/launchctl print system/com.openssh.sshd >/dev/null 2>&1; then
	/usr/bin/touch "${MARKER}"
	log "provisioning complete"
	# Remove the payload: this script holds the account password, and the
	# settings above must not be forcibly re-applied on every later boot.
	/bin/rm -f "/Library/LaunchDaemons/` + LaunchDaemonLabel + `.plist"
	/bin/rm -f "$0"
	/bin/launchctl bootout system/` + LaunchDaemonLabel + ` 2>/dev/null
	exit 0
fi

log "provisioning incomplete; will retry on next boot"
exit 1
`

// payloadFile is one file written into the guest.
type payloadFile struct {
	// Path is relative to the Data volume root.
	Path string
	Mode os.FileMode
	Data []byte
	// Dir marks a directory that must also be root-owned.
	Dir bool
	// Keep marks paths that existed before vee touched them, so a rollback
	// asserts ownership on them but never removes them.
	Keep bool
}

// payloadFiles lists what the patch installs, in creation order.
func payloadFiles(script string) []payloadFile {
	return []payloadFile{
		{Path: appleSetupDonePath, Mode: 0o400},
		{Path: skipBuddyPath, Mode: 0o400},
		// Pre-existing on any real install.
		{Path: filepath.Dir(scriptPath), Dir: true, Mode: 0o755, Keep: true},
		// 0700: the script carries the account password, so unprivileged
		// processes in the guest must not be able to read it.
		{Path: scriptPath, Mode: 0o700, Data: []byte(script)},
		{Path: plistPath, Mode: 0o644, Data: []byte(launchDaemonPlist)},
	}
}

// scriptData is the template payload for firstBootScript. The *Q fields are
// single-quoted for safe shell interpolation.
type scriptData struct {
	UserQ               string
	PasswordQ           string
	SSHPublicKeys       []string
	SSHPublicKeysQ      []string
	Hostname            string
	HostnameQ           string
	EnableScreenSharing bool
}

// renderFirstBootScript produces the guest script for the given options.
func renderFirstBootScript(opts Options, password string) (string, error) {
	tmpl, err := template.New("firstboot").Parse(firstBootScript)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	err = tmpl.Execute(&buf, scriptData{
		UserQ:               shellQuote(opts.User),
		PasswordQ:           shellQuote(password),
		SSHPublicKeys:       opts.SSHPublicKeys,
		SSHPublicKeysQ:      quoteAll(opts.SSHPublicKeys),
		Hostname:            opts.Hostname,
		HostnameQ:           shellQuote(opts.Hostname),
		EnableScreenSharing: opts.EnableScreenSharing,
	})
	if err != nil {
		return "", err
	}
	return buf.String(), nil
}

// quoteAll shell-quotes every element.
func quoteAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if strings.TrimSpace(s) == "" {
			continue
		}
		out = append(out, shellQuote(s))
	}
	return out
}

// shellQuote renders s as a single-quoted shell word.
func shellQuote(s string) string {
	var buf bytes.Buffer
	buf.WriteByte('\'')
	for _, r := range s {
		if r == '\'' {
			buf.WriteString(`'\''`)
			continue
		}
		buf.WriteRune(r)
	}
	buf.WriteByte('\'')
	return buf.String()
}

// Validate rejects options the guest script could not act on. Callers should
// validate BEFORE an expensive restore, not after.
func (o Options) Validate() error {
	if o.User == "" {
		return fmt.Errorf("first-boot user is required")
	}
	for _, r := range o.User {
		// dscl/sysadminctl account names: keep it boring.
		lower := r >= 'a' && r <= 'z'
		digit := r >= '0' && r <= '9'
		if !lower && !digit && r != '-' && r != '_' {
			return fmt.Errorf("first-boot user %q must be lowercase alphanumeric, - or _", o.User)
		}
	}
	return nil
}
