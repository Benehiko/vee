package templates

import (
	"encoding/xml"
	"strings"
	"testing"

	"github.com/Benehiko/vee/internal/images"
)

// TestAutounattendXMLValid renders the answer file for each supported Windows
// version and checks it is well-formed XML, carries the injected storage-driver
// path for that version, and creates the expected local account.
func TestAutounattendXMLValid(t *testing.T) {
	for version, dir := range virtioWinDriverDir {
		xmlStr := autounattendXML(version, dir, "share")

		var v any
		if err := xml.Unmarshal([]byte(xmlStr), &v); err != nil {
			t.Errorf("version %s: Autounattend.xml is not well-formed: %v", version, err)
		}
		wantDriver := `\viostor\` + dir + `\amd64`
		if !strings.Contains(xmlStr, wantDriver) {
			t.Errorf("version %s: driver path %q missing from answer file", version, wantDriver)
		}
		if !strings.Contains(xmlStr, "<Name>"+winAdminUser+"</Name>") {
			t.Errorf("version %s: local account %q missing", version, winAdminUser)
		}
		if !strings.Contains(xmlStr, "BypassSecureBootCheck") {
			t.Errorf("version %s: Secure Boot bypass missing (OVMF vars are not enrolled)", version)
		}
	}
}

// TestGuestSetupPS1 checks the first-logon script references the WinFsp MSI and
// the virtio-win guest tools, and contains no stray Go-raw-string backticks
// (which would silently truncate the script when embedded).
func TestGuestSetupPS1(t *testing.T) {
	s := guestSetupPS1("share")
	if !strings.Contains(s, winfspMSI) {
		t.Errorf("guest setup script does not reference WinFsp MSI %q", winfspMSI)
	}
	if !strings.Contains(s, "virtio-win-guest-tools.exe") {
		t.Error("guest setup script does not run virtio-win guest tools")
	}
	if !strings.Contains(s, "VirtioFsSvc") {
		t.Error("guest setup script does not configure the VirtioFS service")
	}
	if strings.Contains(s, "`") {
		t.Error("guest setup script contains a backtick (would break the Go raw string literal)")
	}
}

// TestVirtioWinDriverDirCoversKnownVersions guards against adding a Windows
// version to images without a matching driver-dir mapping here.
func TestVirtioWinDriverDirCoversKnownVersions(t *testing.T) {
	for _, v := range images.KnownWindowsVersions {
		if _, ok := virtioWinDriverDir[v]; !ok {
			t.Errorf("version %s has no virtio-win driver dir mapping", v)
		}
	}
}
