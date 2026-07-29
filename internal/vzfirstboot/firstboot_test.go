package vzfirstboot

import (
	"strings"
	"testing"
)

// attachFixture mirrors `hdiutil attach -nomount -plist` for a restored macOS
// guest image.
const attachFixture = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>system-entities</key>
	<array>
		<dict>
			<key>content-hint</key>
			<string>GUID_partition_scheme</string>
			<key>dev-entry</key>
			<string>/dev/disk4</string>
		</dict>
		<dict>
			<key>content-hint</key>
			<string>Apple_APFS_ISC</string>
			<key>dev-entry</key>
			<string>/dev/disk4s1</string>
		</dict>
		<dict>
			<key>content-hint</key>
			<string>Apple_APFS</string>
			<key>dev-entry</key>
			<string>/dev/disk4s2</string>
		</dict>
		<dict>
			<key>content-hint</key>
			<string>Apple_APFS_Recovery</string>
			<key>dev-entry</key>
			<string>/dev/disk4s3</string>
		</dict>
	</array>
</dict>
</plist>
`

// apfsListFixture mirrors `diskutil apfs list -plist`: nested containers,
// volumes and role arrays — the shape a flat key scanner cannot read. The
// first container is the HOST's (it also has a Data volume), so selection by
// physical store is what keeps vee off the host filesystem.
const apfsListFixture = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>Containers</key>
	<array>
		<dict>
			<key>ContainerReference</key>
			<string>disk1</string>
			<key>DesignatedPhysicalStore</key>
			<string>disk0s2</string>
			<key>PhysicalStores</key>
			<array>
				<dict>
					<key>DeviceIdentifier</key>
					<string>disk0s2</string>
				</dict>
			</array>
			<key>Volumes</key>
			<array>
				<dict>
					<key>DeviceIdentifier</key>
					<string>disk1s5</string>
					<key>Name</key>
					<string>Host HD - Data</string>
					<key>Roles</key>
					<array>
						<string>Data</string>
					</array>
				</dict>
			</array>
		</dict>
		<dict>
			<key>ContainerReference</key>
			<string>disk7</string>
			<key>DesignatedPhysicalStore</key>
			<string>disk4s2</string>
			<key>PhysicalStores</key>
			<array>
				<dict>
					<key>DeviceIdentifier</key>
					<string>disk4s2</string>
				</dict>
			</array>
			<key>Volumes</key>
			<array>
				<dict>
					<key>DeviceIdentifier</key>
					<string>disk7s1</string>
					<key>Name</key>
					<string>Macintosh HD</string>
					<key>Roles</key>
					<array>
						<string>System</string>
					</array>
				</dict>
				<dict>
					<key>DeviceIdentifier</key>
					<string>disk7s3</string>
					<key>Roles</key>
					<array>
						<string>Preboot</string>
					</array>
				</dict>
				<dict>
					<key>DeviceIdentifier</key>
					<string>disk7s5</string>
					<key>Name</key>
					<string>Macintosh HD - Data</string>
					<key>Roles</key>
					<array>
						<string>Data</string>
					</array>
				</dict>
			</array>
		</dict>
	</array>
</dict>
</plist>
`

func TestParsePlistNested(t *testing.T) {
	root, err := parsePlist([]byte(apfsListFixture))
	if err != nil {
		t.Fatalf("parsePlist: %v", err)
	}
	containers := arrayOf(dictOf(root)["Containers"])
	if len(containers) != 2 {
		t.Fatalf("got %d containers, want 2", len(containers))
	}
	// Nesting must not bleed between levels: each container keeps its own
	// volumes, and each volume its own roles.
	vols := arrayOf(dictOf(containers[1])["Volumes"])
	if len(vols) != 3 {
		t.Fatalf("got %d volumes in container 2, want 3", len(vols))
	}
	if got := stringOf(dictOf(vols[0])["Name"]); got != "Macintosh HD" {
		t.Errorf("volume 0 Name = %q", got)
	}
	roles := arrayOf(dictOf(vols[0])["Roles"])
	if len(roles) != 1 || stringOf(roles[0]) != "System" {
		t.Errorf("volume 0 Roles = %v, want [System]", roles)
	}
}

func TestParsePlistScalars(t *testing.T) {
	root, err := parsePlist([]byte(`<plist version="1.0"><dict>
		<key>s</key><string>text</string>
		<key>n</key><integer>42</integer>
		<key>f</key><real>1.5</real>
		<key>t</key><true/>
		<key>fa</key><false/>
	</dict></plist>`))
	if err != nil {
		t.Fatalf("parsePlist: %v", err)
	}
	d := dictOf(root)
	if stringOf(d["s"]) != "text" {
		t.Errorf("string = %v", d["s"])
	}
	if n, _ := d["n"].(int64); n != 42 {
		t.Errorf("integer = %v", d["n"])
	}
	if f, _ := d["f"].(float64); f != 1.5 {
		t.Errorf("real = %v", d["f"])
	}
	if b, _ := d["t"].(bool); !b {
		t.Errorf("true = %v", d["t"])
	}
	if b, _ := d["fa"].(bool); b {
		t.Errorf("false = %v", d["fa"])
	}
}

func TestDevicesFromAttach(t *testing.T) {
	root, err := parsePlist([]byte(attachFixture))
	if err != nil {
		t.Fatal(err)
	}
	got := devicesFromAttach(root)
	if got.WholeDisk != "disk4" {
		t.Errorf("WholeDisk = %q, want disk4", got.WholeDisk)
	}
	if got.Container != "disk4s2" {
		t.Errorf("Container = %q, want disk4s2", got.Container)
	}
	// An unmounted attach reports no volume roles; Data is resolved after.
	if got.Data != "" {
		t.Errorf("Data = %q, want empty before resolution", got.Data)
	}
}

func TestDevicesFromAttachWithoutPartitionScheme(t *testing.T) {
	// A raw image without a recognized scheme entity still needs a detach
	// handle: the shortest device node is the whole disk.
	root, err := parsePlist([]byte(`<plist version="1.0"><dict>
		<key>system-entities</key>
		<array>
			<dict><key>dev-entry</key><string>/dev/disk9s1</string></dict>
			<dict><key>dev-entry</key><string>/dev/disk9</string></dict>
		</array>
	</dict></plist>`))
	if err != nil {
		t.Fatal(err)
	}
	if got := devicesFromAttach(root); got.WholeDisk != "disk9" {
		t.Errorf("WholeDisk = %q, want disk9", got.WholeDisk)
	}
}

func TestDataVolumeForContainer(t *testing.T) {
	root, err := parsePlist([]byte(apfsListFixture))
	if err != nil {
		t.Fatal(err)
	}
	// The guest's container must be selected by physical store — never the
	// host's own container, which also has a Data volume.
	got, err := dataVolumeForContainer(root, "disk4s2")
	if err != nil {
		t.Fatalf("dataVolumeForContainer: %v", err)
	}
	if got != "disk7s5" {
		t.Errorf("Data volume = %q, want disk7s5 (the guest's)", got)
	}
	if _, err := dataVolumeForContainer(root, "disk99s9"); err == nil {
		t.Error("expected an error for an unknown container")
	}
}

func TestDataVolumeForContainerNoDataRole(t *testing.T) {
	root, err := parsePlist([]byte(`<plist version="1.0"><dict>
		<key>Containers</key>
		<array><dict>
			<key>DesignatedPhysicalStore</key><string>disk4s2</string>
			<key>Volumes</key><array><dict>
				<key>DeviceIdentifier</key><string>disk7s1</string>
				<key>Roles</key><array><string>System</string></array>
			</dict></array>
		</dict></array>
	</dict></plist>`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dataVolumeForContainer(root, "disk4s2"); err == nil {
		t.Error("expected an error when the container has no Data volume")
	}
}

func TestOptionsValidate(t *testing.T) {
	if err := (Options{User: "vee"}).Validate(); err != nil {
		t.Errorf("valid options rejected: %v", err)
	}
	for _, bad := range []string{"", "Vee", "vee user", "vee$", "vee.admin"} {
		if err := (Options{User: bad}).Validate(); err == nil {
			t.Errorf("Validate(%q): expected error", bad)
		}
	}
}

func TestGeneratePassword(t *testing.T) {
	p, err := GeneratePassword(16)
	if err != nil {
		t.Fatal(err)
	}
	if len(p) != 16 {
		t.Errorf("length = %d, want 16", len(p))
	}
	for _, r := range p {
		if !strings.ContainsRune(passwordAlphabet, r) {
			t.Errorf("password contains %q, outside the safe alphabet", r)
		}
	}
	other, err := GeneratePassword(16)
	if err != nil {
		t.Fatal(err)
	}
	if p == other {
		t.Error("two generated passwords are identical")
	}
	if _, err := GeneratePassword(0); err == nil {
		t.Error("expected an error for length 0")
	}
}

func TestRenderFirstBootScript(t *testing.T) {
	script, err := renderFirstBootScript(Options{
		User:                "vee",
		SSHPublicKeys:       []string{"ssh-ed25519 AAAAKEY1 a@h", "ssh-ed25519 AAAAKEY2 b@h"},
		Hostname:            "mymac",
		EnableScreenSharing: true,
	}, "s3cret")
	if err != nil {
		t.Fatalf("renderFirstBootScript: %v", err)
	}
	for _, want := range []string{
		"USER_NAME='vee'",
		"printf '%s' 's3cret'",
		"'ssh-ed25519 AAAAKEY1 a@h'",
		"'ssh-ed25519 AAAAKEY2 b@h'", // every key, not just the first
		// A never-loaded service cannot be kickstarted: bootstrap is required.
		"launchctl bootstrap system /System/Library/LaunchDaemons/ssh.plist",
		"launchctl enable system/com.openssh.sshd",
		"launchctl bootstrap system /System/Library/LaunchDaemons/com.apple.screensharing.plist",
		// Enabling the service is not enough: unconfigured Remote Management
		// refuses sessions with "Screen Sharing is not permitted".
		"ARDAgent.app/Contents/Resources/kickstart",
		"-configure -access -on",
		"scutil --set LocalHostName 'mymac'",
		// The payload removes itself once provisioning succeeds: it holds the
		// account password.
		`rm -f "/Library/LaunchDaemons/` + LaunchDaemonLabel + `.plist"`,
		`rm -f "$0"`,
		"launchctl bootout system/" + LaunchDaemonLabel,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q", want)
		}
	}

	minimal, err := renderFirstBootScript(Options{User: "vee"}, "pw")
	if err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"authorized_keys", "screensharing", "LocalHostName"} {
		if strings.Contains(minimal, absent) {
			t.Errorf("minimal script unexpectedly contains %q", absent)
		}
	}
}

func TestLaunchDaemonPlistLogsToOpenablePath(t *testing.T) {
	// launchd refuses to spawn a job whose Standard*Path cannot be opened.
	// vee's helper attaches no console device, so /dev/tty.virtio must not
	// appear here.
	if strings.Contains(launchDaemonPlist, "tty.virtio") {
		t.Error("plist logs to a console device vee does not attach")
	}
	if !strings.Contains(launchDaemonPlist, "/var/log/vee-firstboot.log") {
		t.Error("plist does not log to the guest log file")
	}
}

func TestPayloadFilesProtectPassword(t *testing.T) {
	// The script embeds the account password, so it must not be readable by
	// unprivileged guest processes; pre-existing directories must survive a
	// rollback.
	var sawScript, sawDir bool
	for _, f := range payloadFiles("#!/bin/sh\n", renderSudoers("vee")) {
		if f.Path == scriptPath {
			sawScript = true
			if f.Mode.Perm() != 0o700 {
				t.Errorf("%s mode = %o, want 0700", f.Path, f.Mode.Perm())
			}
			if f.Keep {
				t.Errorf("%s must be removable on rollback", f.Path)
			}
		}
		if f.Dir {
			sawDir = true
			if !f.Keep {
				t.Errorf("pre-existing directory %s must be kept on rollback", f.Path)
			}
		}
	}
	if !sawScript || !sawDir {
		t.Errorf("payload is missing the script or its directory")
	}
}

func TestShellQuoteEscapesQuotes(t *testing.T) {
	got := shellQuote("a'b")
	if got != `'a'\''b'` {
		t.Errorf("shellQuote(a'b) = %s", got)
	}
	script, err := renderFirstBootScript(Options{User: "vee"}, "pa'ss")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, `printf '%s' 'pa'\''ss'`) {
		t.Errorf("password quoting lost in script")
	}
}

func TestQuoteAllSkipsBlanks(t *testing.T) {
	got := quoteAll([]string{"a", "", "  ", "b"})
	if len(got) != 2 || got[0] != "'a'" || got[1] != "'b'" {
		t.Errorf("quoteAll = %v", got)
	}
}

func TestRenderSudoersGrantsOnlyShutdown(t *testing.T) {
	got := renderSudoers("vee")
	if !strings.Contains(got, "vee ALL=(ALL) NOPASSWD: /sbin/shutdown") {
		t.Errorf("sudoers rule missing or malformed:\n%s", got)
	}
	// Nothing else may be granted: this is the guest's only privileged path.
	for _, forbidden := range []string{"ALL) NOPASSWD: ALL", "NOPASSWD:ALL", "/bin/sh", "/usr/bin/sudo"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("sudoers grants more than shutdown (%q):\n%s", forbidden, got)
		}
	}
	if renderSudoers("") != "" {
		t.Error("no user should mean no sudoers file")
	}
}

func TestPayloadIncludesSudoersOnlyWhenRendered(t *testing.T) {
	withRule := payloadFiles("#!/bin/sh\n", renderSudoers("vee"))
	var found *payloadFile
	for i := range withRule {
		if withRule[i].Path == sudoersPath {
			found = &withRule[i]
		}
	}
	if found == nil {
		t.Fatalf("sudoers file missing from the payload")
	}
	// sudo ignores a rule file that is group- or world-writable.
	if found.Mode.Perm() != 0o440 {
		t.Errorf("sudoers mode = %o, want 0440", found.Mode.Perm())
	}
	if found.Keep {
		t.Error("sudoers must be removed on rollback")
	}

	for _, f := range payloadFiles("#!/bin/sh\n", "") {
		if f.Path == sudoersPath {
			t.Error("sudoers file written even though no rule was rendered")
		}
	}
}
