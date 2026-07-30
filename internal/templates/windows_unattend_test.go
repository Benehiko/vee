package templates

import (
	"encoding/xml"
	"strings"
	"testing"

	"github.com/Benehiko/vee/internal/images"
)

// TestAutounattendXMLValid renders the answer file for each supported Windows
// version on both media architectures and checks it is well-formed XML,
// carries the injected storage-driver path for that version and arch, stamps
// every component with the media's processorArchitecture, and creates the
// expected local account.
func TestAutounattendXMLValid(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		procArch, driverLeaf := windowsUnattendArch(arch)
		for version, dir := range virtioWinDriverDir {
			xmlStr := autounattendXML(version, arch, dir, "share")

			var v any
			if err := xml.Unmarshal([]byte(xmlStr), &v); err != nil {
				t.Errorf("%s/%s: Autounattend.xml is not well-formed: %v", arch, version, err)
			}
			// The answer file uses the wcm: prefix throughout; it MUST declare the
			// wcm namespace or Windows Setup rejects the file (Go's lenient XML
			// decoder does not, which is why this needs an explicit check).
			if strings.Contains(xmlStr, "wcm:") && !strings.Contains(xmlStr, `xmlns:wcm=`) {
				t.Errorf("%s/%s: answer file uses wcm: prefix but does not declare xmlns:wcm", arch, version)
			}
			// Setup ignores components whose processorArchitecture does not
			// match the running media, so a stray hardcoded arch means whole
			// passes silently do not apply.
			want := `processorArchitecture="` + procArch + `"`
			other := "amd64"
			if procArch == "amd64" {
				other = "arm64"
			}
			if !strings.Contains(xmlStr, want) {
				t.Errorf("%s/%s: no component carries %s", arch, version, want)
			}
			if strings.Contains(xmlStr, `processorArchitecture="`+other+`"`) {
				t.Errorf("%s/%s: answer file leaks a %s component", arch, version, other)
			}
			wantDriver := `\viostor\` + dir + `\` + driverLeaf
			if !strings.Contains(xmlStr, wantDriver) {
				t.Errorf("%s/%s: driver path %q missing from answer file", arch, version, wantDriver)
			}
			if !strings.Contains(xmlStr, "<Name>"+winAdminUser+"</Name>") {
				t.Errorf("%s/%s: local account %q missing", arch, version, winAdminUser)
			}
			if !strings.Contains(xmlStr, "BypassSecureBootCheck") {
				t.Errorf("%s/%s: Secure Boot bypass missing (OVMF vars are not enrolled)", arch, version)
			}
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

// TestGuestSetupARM64PS1 checks the arm64 first-logon script stays inside what
// arm64 guests can actually do: no virtio-win guest-tools (no ARM64 installer
// exists), no VirtioFS service (the ARM64 viofs driver is test-signed), but
// OpenSSH still enabled — and no backticks that would break the raw string.
func TestGuestSetupARM64PS1(t *testing.T) {
	s := guestSetupARM64PS1()
	if strings.Contains(s, "virtio-win-guest-tools.exe") {
		t.Error("arm64 setup script runs the guest-tools installer, which has no ARM64 build")
	}
	if strings.Contains(s, "Start-Service -Name 'VirtioFsSvc'") {
		t.Error("arm64 setup script starts VirtioFsSvc, which cannot load (viofs is test-signed on ARM64)")
	}
	if !strings.Contains(s, "OpenSSH.Server") {
		t.Error("arm64 setup script does not enable OpenSSH")
	}
	if strings.Contains(s, "`") {
		t.Error("arm64 setup script contains a backtick (would break the Go raw string literal)")
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
