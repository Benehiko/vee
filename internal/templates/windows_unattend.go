package templates

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Benehiko/vee/internal/images"
	"github.com/Benehiko/vee/internal/utils"
	"github.com/Benehiko/vee/provider"
	"go.uber.org/zap"
)

// Windows unattended-install support.
//
// vee's Windows template installs from a UUP-dump ISO onto a virtio system
// disk. Stock Windows Setup cannot see a virtio disk (no in-box driver) and is
// interactive, so a hands-free install needs three things wired together:
//
//  1. the virtio-win driver ISO, so Setup's WinPE phase can load the viostor
//     storage driver and the guest can later load the network + virtiofs
//     drivers;
//  2. an Autounattend.xml answer file that injects that storage driver in
//     WinPE, wipes and partitions the disk, creates a local admin account, and
//     skips OOBE;
//  3. a first-logon step that installs WinFsp + the virtio-win guest tools so
//     the virtiofs share mounts as a drive letter.
//
// Items 2 and 3 (plus the WinFsp MSI and a small setup script) are packed into
// a tiny extra ISO — the "unattend" ISO. Windows Setup scans every attached
// removable volume for Autounattend.xml, so no boot-order coordination is
// needed.

const (
	// virtioWinURL is the Fedora-hosted stable virtio-win driver ISO. It ships
	// signed drivers for every supported Windows version plus the guest-tools
	// installer (which includes viofs, the virtiofs client).
	virtioWinURL = "https://fedorapeople.org/groups/virt/virtio-win/direct-downloads/stable-virtio/virtio-win.iso"
	virtioWinISO = "virtio-win.iso"

	// winfspURL is a pinned WinFsp release MSI. WinFsp is the user-mode
	// filesystem framework virtiofs builds on; it is not part of virtio-win and
	// must be installed first. Pinned rather than "latest" for reproducibility.
	winfspURL = "https://github.com/winfsp/winfsp/releases/download/v2.0/winfsp-2.0.23075.msi"
	winfspMSI = "winfsp-2.0.23075.msi"

	// winUnattendVolID is the volume label of the generated unattend ISO. The
	// guest setup script locates its payload by this label, so it must not
	// collide with the Windows install media label (WIN_INSTALL).
	winUnattendVolID = "WIN_UNATTEND"

	// winAdminUser / winAdminPass are the local account the unattended install
	// creates and auto-logs-in. This VM is a disposable test box on user-mode
	// NAT, so a fixed well-known credential is acceptable and keeps `vee`
	// output predictable; document it in the template help.
	winAdminUser = "vee"
	winAdminPass = "vee"
)

// virtioWinDriverDir maps a WindowsVersion to the per-OS subdirectory used
// inside the virtio-win ISO (e.g. "w11", "w10", "2k22", "2k25"). Setup loads
// the storage driver from <drive>\viostor\<dir>\amd64.
var virtioWinDriverDir = map[images.WindowsVersion]string{
	images.Windows11:         "w11",
	images.Windows10:         "w10",
	images.WindowsServer2025: "2k25",
	images.WindowsServer2022: "2k22",
}

// ensureCachedDownload fetches url into <isoCache>/<name> if not already
// present and returns the absolute path. Downloads go direct (not through the
// pacman mirror proxy) since these are one-off large binaries.
func ensureCachedDownload(ctx context.Context, p provider.Provider, url, name string) (string, error) {
	dst := filepath.Join(p.Config().ISOCachePath, name)
	if _, err := os.Stat(dst); err == nil {
		p.Logger().Info("skipping download", zap.String("file", dst), zap.String("reason", "already downloaded"))
		return dst, nil
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", fmt.Errorf("create cache dir: %w", err)
	}

	p.Logger().Info("downloading", zap.String("url", url), zap.String("dest", dst))
	if err := utils.DownloadToFile(ctx, url, dst); err != nil {
		return "", fmt.Errorf("download %s: %w", name, err)
	}
	return dst, nil
}

// autounattendXML renders the Windows answer file for the given version and
// virtiofs tag. driverDir is the virtio-win per-OS folder (e.g. "w11").
//
// The WinPE pass points DriverPaths at the whole virtio-win CD (E: at WinPE
// time) so every signed .inf is available; Windows picks the matching viostor
// automatically. The disk is fully wiped and repartitioned (EFI + MSR +
// Windows) so the install is deterministic. FirstLogonCommands hand off to the
// setup script on the unattend CD.
func autounattendXML(version images.WindowsVersion, driverDir, tag string) string {
	_ = version
	// runSetup invokes the guest setup script from whichever drive the
	// unattend ISO mounted as. cmd searches drives at first logon.
	const tmpl = `<?xml version="1.0" encoding="utf-8"?>
<unattend xmlns="urn:schemas-microsoft-com:unattend" xmlns:wcm="http://schemas.microsoft.com/WMIConfig/2002/State" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
  <settings pass="windowsPE">
    <!-- Locale for WinPE so Setup does not stop on the language/keyboard page. -->
    <component name="Microsoft-Windows-International-Core-WinPE" processorArchitecture="amd64"
               publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <SetupUILanguage>
        <UILanguage>en-US</UILanguage>
      </SetupUILanguage>
      <InputLocale>0409:00000409</InputLocale>
      <SystemLocale>en-US</SystemLocale>
      <UILanguage>en-US</UILanguage>
      <UserLocale>en-US</UserLocale>
    </component>
    <component name="Microsoft-Windows-PnpCustomizationsWinPE" processorArchitecture="amd64"
               publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <DriverPaths>
        <PathAndCredentials wcm:action="add" wcm:keyValue="1">
          <Path>D:\viostor\{{DRIVERDIR}}\amd64</Path>
        </PathAndCredentials>
        <PathAndCredentials wcm:action="add" wcm:keyValue="2">
          <Path>E:\viostor\{{DRIVERDIR}}\amd64</Path>
        </PathAndCredentials>
        <PathAndCredentials wcm:action="add" wcm:keyValue="3">
          <Path>D:\NetKVM\{{DRIVERDIR}}\amd64</Path>
        </PathAndCredentials>
        <PathAndCredentials wcm:action="add" wcm:keyValue="4">
          <Path>E:\NetKVM\{{DRIVERDIR}}\amd64</Path>
        </PathAndCredentials>
      </DriverPaths>
    </component>
    <component name="Microsoft-Windows-Setup" processorArchitecture="amd64"
               publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <RunSynchronous>
        <!-- Bypass Windows 11 hardware checks (Secure Boot keys are not
             enrolled in the OVMF vars this VM boots with; TPM 2.0 is real).
             Harmless on Windows 10. cmd /c so WinPE resolves the executable. -->
        <RunSynchronousCommand wcm:action="add">
          <Order>1</Order>
          <Path>cmd /c reg add HKLM\SYSTEM\Setup\LabConfig /v BypassTPMCheck /t REG_DWORD /d 1 /f</Path>
        </RunSynchronousCommand>
        <RunSynchronousCommand wcm:action="add">
          <Order>2</Order>
          <Path>cmd /c reg add HKLM\SYSTEM\Setup\LabConfig /v BypassSecureBootCheck /t REG_DWORD /d 1 /f</Path>
        </RunSynchronousCommand>
        <RunSynchronousCommand wcm:action="add">
          <Order>3</Order>
          <Path>cmd /c reg add HKLM\SYSTEM\Setup\LabConfig /v BypassRAMCheck /t REG_DWORD /d 1 /f</Path>
        </RunSynchronousCommand>
        <RunSynchronousCommand wcm:action="add">
          <Order>4</Order>
          <Path>cmd /c reg add HKLM\SYSTEM\Setup\LabConfig /v BypassCPUCheck /t REG_DWORD /d 1 /f</Path>
        </RunSynchronousCommand>
      </RunSynchronous>
      <DiskConfiguration>
        <Disk wcm:action="add">
          <DiskID>0</DiskID>
          <WillWipeDisk>true</WillWipeDisk>
          <CreatePartitions>
            <CreatePartition wcm:action="add">
              <Order>1</Order>
              <Type>EFI</Type>
              <Size>260</Size>
            </CreatePartition>
            <CreatePartition wcm:action="add">
              <Order>2</Order>
              <Type>MSR</Type>
              <Size>16</Size>
            </CreatePartition>
            <CreatePartition wcm:action="add">
              <Order>3</Order>
              <Type>Primary</Type>
              <Extend>true</Extend>
            </CreatePartition>
          </CreatePartitions>
          <ModifyPartitions>
            <ModifyPartition wcm:action="add">
              <Order>1</Order>
              <PartitionID>1</PartitionID>
              <Label>System</Label>
              <Format>FAT32</Format>
            </ModifyPartition>
            <ModifyPartition wcm:action="add">
              <Order>2</Order>
              <PartitionID>3</PartitionID>
              <Label>Windows</Label>
              <Letter>C</Letter>
              <Format>NTFS</Format>
            </ModifyPartition>
          </ModifyPartitions>
        </Disk>
      </DiskConfiguration>
      <ImageInstall>
        <OSImage>
          <InstallTo>
            <DiskID>0</DiskID>
            <PartitionID>3</PartitionID>
          </InstallTo>
          <InstallFrom>
            <MetaData wcm:action="add">
              <Key>/IMAGE/INDEX</Key>
              <Value>{{IMAGEINDEX}}</Value>
            </MetaData>
          </InstallFrom>
        </OSImage>
      </ImageInstall>
      <UserData>
        <ProductKey>
          <Key>{{PRODUCTKEY}}</Key>
          <WillShowUI>OnError</WillShowUI>
        </ProductKey>
        <AcceptEula>true</AcceptEula>
      </UserData>
    </component>
  </settings>

  <settings pass="specialize">
    <component name="Microsoft-Windows-Shell-Setup" processorArchitecture="amd64"
               publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <ComputerName>VEE-WIN</ComputerName>
    </component>
  </settings>

  <settings pass="oobeSystem">
    <!-- Locale for OOBE so the region/keyboard pages auto-skip. -->
    <component name="Microsoft-Windows-International-Core" processorArchitecture="amd64"
               publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <InputLocale>0409:00000409</InputLocale>
      <SystemLocale>en-US</SystemLocale>
      <UILanguage>en-US</UILanguage>
      <UserLocale>en-US</UserLocale>
    </component>
    <component name="Microsoft-Windows-Shell-Setup" processorArchitecture="amd64"
               publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <OOBE>
        <HideEULAPage>true</HideEULAPage>
        <HideLocalAccountScreen>true</HideLocalAccountScreen>
        <HideOnlineAccountScreens>true</HideOnlineAccountScreens>
        <HideWirelessSetupInOOBE>true</HideWirelessSetupInOOBE>
        <ProtectYourPC>3</ProtectYourPC>
        <SkipMachineOOBE>true</SkipMachineOOBE>
        <SkipUserOOBE>true</SkipUserOOBE>
      </OOBE>
      <UserAccounts>
        <LocalAccounts>
          <LocalAccount wcm:action="add">
            <Name>{{USER}}</Name>
            <Group>Administrators</Group>
            <Password>
              <Value>{{PASS}}</Value>
              <PlainText>true</PlainText>
            </Password>
          </LocalAccount>
        </LocalAccounts>
      </UserAccounts>
      <AutoLogon>
        <Enabled>true</Enabled>
        <Username>{{USER}}</Username>
        <Password>
          <Value>{{PASS}}</Value>
          <PlainText>true</PlainText>
        </Password>
        <LogonCount>1</LogonCount>
      </AutoLogon>
      <FirstLogonCommands>
        <SynchronousCommand wcm:action="add">
          <Order>1</Order>
          <CommandLine>powershell -NoProfile -ExecutionPolicy Bypass -Command "$d=(Get-Volume -FileSystemLabel '{{VOLID}}').DriveLetter; Start-Process powershell -ArgumentList '-NoProfile','-ExecutionPolicy','Bypass','-File',(\"$($d):\setup-guest.ps1\") -Wait"</CommandLine>
          <Description>Install WinFsp + virtio-win guest tools (virtiofs)</Description>
        </SynchronousCommand>
      </FirstLogonCommands>
    </component>
  </settings>
</unattend>
`
	// Image index + a generic (KMS client / default) product key so Setup does
	// not stop to ask which edition to install. Index 1 is Home on the retail
	// consumer media for both Windows 10 and 11.
	imageIndex := "1"
	productKey := winProductKey[version]
	if productKey == "" {
		productKey = winProductKey[images.Windows10]
	}
	r := strings.NewReplacer(
		"{{DRIVERDIR}}", driverDir,
		"{{USER}}", winAdminUser,
		"{{PASS}}", winAdminPass,
		"{{VOLID}}", winUnattendVolID,
		"{{IMAGEINDEX}}", imageIndex,
		"{{PRODUCTKEY}}", productKey,
	)
	_ = tag
	return r.Replace(tmpl)
}

// winProductKey maps a Windows version to a generic edition key so Setup
// installs unattended without prompting for a key. These are Microsoft's
// public KMS client setup keys — they select the edition, they do not
// activate.
var winProductKey = map[images.WindowsVersion]string{
	images.Windows10:         "YTMG3-N6DKC-DKB77-7M9GH-8HVX7", // Win10 Home
	images.Windows11:         "YTMG3-N6DKC-DKB77-7M9GH-8HVX7", // Win11 Home
	images.WindowsServer2025: "TX9XD-98N7V-6WMQ6-BX7FG-H8Q99",
	images.WindowsServer2022: "VDYBN-27WPP-V4HQT-9VMD4-VMK7H",
}

// guestSetupPS1 renders the first-logon PowerShell script. It installs WinFsp
// silently, then runs the virtio-win guest-tools installer (which installs the
// viofs driver and starts the VirtioFS service). tag is the virtiofs mount tag
// the share was created with; the script logs it for the operator (virtiofs on
// Windows exposes the share as a drive letter once the service starts, not by
// tag, so tag is informational here).
func guestSetupPS1(tag string) string {
	const tmpl = `$ErrorActionPreference = 'Continue'
$log = "$env:SystemDrive\vee-guest-setup.log"
function Log($m) { "$([DateTime]::Now.ToString('s')) $m" | Tee-Object -FilePath $log -Append }

Log "vee guest setup starting (virtiofs tag: {{TAG}})"

# Locate the unattend volume (this script's drive) and the virtio-win volume.
$unattend = (Get-Volume -FileSystemLabel '{{VOLID}}' -ErrorAction SilentlyContinue).DriveLetter
$virtio   = (Get-Volume | Where-Object { Test-Path ("$($_.DriveLetter):\virtio-win-guest-tools.exe") } | Select-Object -First 1).DriveLetter
if (-not $virtio) {
  $virtio = (Get-Volume | Where-Object { Test-Path ("$($_.DriveLetter):\guest-agent") } | Select-Object -First 1).DriveLetter
}
Log "unattend drive: $unattend  virtio-win drive: $virtio"

# 1. WinFsp (required before the virtiofs service can start).
$msi = "$($unattend):\{{WINFSP}}"
if (Test-Path $msi) {
  Log "installing WinFsp from $msi"
  Start-Process msiexec.exe -ArgumentList @('/i', $msi, '/qn', '/norestart', 'INSTALLLEVEL=1000') -Wait
} else {
  Log "WARNING: WinFsp MSI not found at $msi"
}

# 2. virtio-win guest tools (installs viofs driver + VirtioFsSvc, plus NIC etc.).
$gt = "$($virtio):\virtio-win-guest-tools.exe"
if (Test-Path $gt) {
  Log "installing virtio-win guest tools from $gt"
  Start-Process $gt -ArgumentList "/install","/quiet","/passive","/norestart" -Wait
} else {
  Log "WARNING: virtio-win-guest-tools.exe not found on $virtio"
}

# 3. Ensure the VirtioFS service is set to autostart and started, so the share
#    mounts on every boot.
$svc = Get-Service -Name 'VirtioFsSvc' -ErrorAction SilentlyContinue
if ($svc) {
  Set-Service -Name 'VirtioFsSvc' -StartupType Automatic
  try { Start-Service -Name 'VirtioFsSvc' } catch { Log "VirtioFsSvc start deferred to reboot: $_" }
  Log "VirtioFsSvc configured (status: $((Get-Service VirtioFsSvc).Status))"
} else {
  Log "WARNING: VirtioFsSvc not present after guest-tools install"
}

# 4. Enable OpenSSH server for headless access (best-effort).
try {
  Add-WindowsCapability -Online -Name 'OpenSSH.Server~~~~0.0.1.0' -ErrorAction Stop | Out-Null
  Set-Service -Name sshd -StartupType Automatic
  Start-Service sshd
  New-NetFirewallRule -Name sshd -DisplayName 'OpenSSH Server' -Enabled True -Direction Inbound -Protocol TCP -Action Allow -LocalPort 22 -ErrorAction SilentlyContinue | Out-Null
  Log "OpenSSH server enabled"
} catch { Log "OpenSSH enable skipped: $_" }

Log "vee guest setup complete; rebooting to mount virtiofs share"
Start-Sleep -Seconds 3
Restart-Computer -Force
`
	r := strings.NewReplacer(
		"{{TAG}}", tag,
		"{{VOLID}}", winUnattendVolID,
		"{{WINFSP}}", winfspMSI,
	)
	return r.Replace(tmpl)
}

// buildExtrasISO builds the single "extras" ISO that carries everything Setup
// and first-logon need beyond the Windows install media: the whole virtio-win
// driver tree (so WinPE loads viostor and the guest tools installer is present)
// plus Autounattend.xml, setup-guest.ps1 and the WinFsp MSI at the root.
//
// It is ONE ISO on purpose. Windows' boot manager reboot-loops on q35 when more
// than two optical drives are attached, so the drivers and the answer file
// cannot be two separate CDROMs alongside the install media — they are merged
// here. The volume is labelled winUnattendVolID so Setup finds Autounattend.xml
// and the first-logon command locates setup-guest.ps1 by label.
//
// The merge runs in a container (mounting the virtio-win ISO read-only and
// running genisoimage) so the host needs no loopback-mount privileges.
func buildExtrasISO(ctx context.Context, p provider.Provider, outPath, virtioISOPath, autounattend, setupScript, winfspMSIPath string) error {
	runtime, err := findWindowsContainerRuntime()
	if err != nil {
		return err
	}

	stage, err := os.MkdirTemp("", "vee-extras-")
	if err != nil {
		return fmt.Errorf("create extras staging dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()

	if err := os.WriteFile(filepath.Join(stage, "Autounattend.xml"), []byte(autounattend), 0o644); err != nil {
		return fmt.Errorf("write Autounattend.xml: %w", err)
	}
	if err := os.WriteFile(filepath.Join(stage, "setup-guest.ps1"), []byte(setupScript), 0o644); err != nil {
		return fmt.Errorf("write setup-guest.ps1: %w", err)
	}
	if winfspMSIPath != "" {
		data, readErr := os.ReadFile(winfspMSIPath)
		if readErr != nil {
			return fmt.Errorf("read WinFsp MSI: %w", readErr)
		}
		if err := os.WriteFile(filepath.Join(stage, winfspMSI), data, 0o644); err != nil {
			return fmt.Errorf("stage WinFsp MSI: %w", err)
		}
	}

	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("create extras iso dir: %w", err)
	}

	outDir := filepath.Dir(outPath)
	outName := filepath.Base(outPath)
	buildScript := `set -e
apk add --no-cache cdrkit 7zip >/dev/null 2>&1
mkdir -p /iso
# Unpack the virtio-win ISO (read-only bind) into the staging tree.
7z x -y -o/iso /virtio.iso >/dev/null
# Overlay the unattend files at the ISO root.
cp -a /extra/. /iso/
genisoimage -J -joliet-long -r -V ` + winUnattendVolID + ` -o /out/` + outName + ` /iso >/dev/null 2>&1
`

	args := []string{
		"run", "--rm",
		"-v", virtioISOPath + ":/virtio.iso:ro",
		"-v", stage + ":/extra:ro",
		"-v", outDir + ":/out",
		"alpine:latest",
		"sh", "-c", buildScript,
	}
	cmd := exec.CommandContext(ctx, runtime, args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build extras ISO: %w", err)
	}
	if _, err := os.Stat(outPath); err != nil {
		return fmt.Errorf("extras ISO not produced at %s", outPath)
	}
	p.Logger().Info("built windows extras ISO", zap.String("path", outPath))
	return nil
}
