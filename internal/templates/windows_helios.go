package templates

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Benehiko/vee/internal/vm"
	"github.com/Benehiko/vee/provider"
)

// HeliosOptions turns the windows template into a WinBoat Helios guest: a
// Windows VM whose GPU is virtio-gpu + Venus with the Helios WDDM driver
// (https://github.com/winboat-org/helios) providing D3D11/D3D12, Vulkan and
// OpenGL on the host GPU. Helios is pre-release; treat these VMs as
// experimental.
type HeliosOptions struct {
	// SetupExe is the host path to HeliosSetup.exe — the self-contained
	// installer produced by Helios' "Windows graphics and compute bundle"
	// workflow (helios-windows-x64-*.zip). Baked into the extras ISO and run
	// silently by the first-logon script.
	SetupExe string
	// QemuBinary is the qemu-helios fork's qemu-system-x86_64 (an installed
	// build, so its modules and firmware data resolve).
	QemuBinary string
	// HostMem sizes the virtio-gpu host memory window ("VRAM"); empty picks
	// qemu.DefaultVenusHostMem.
	HostMem string
	// VNC overrides the VNC address; empty derives a stable loopback port
	// from the VM name.
	VNC string
	// RenderNode pins Venus to one host GPU (e.g. /dev/dri/renderD129).
	RenderNode string
	// BootstrapVGA attaches a standard VGA adapter for the Windows install
	// only — Helios' own launcher does the same (HELIOS_BOOTSTRAP_DISPLAY)
	// when booting install media. Off by default: QEMU's VNC server shows the
	// first display device, so while the VGA is attached it, not the Helios
	// scanout, is what VNC shows; and vee installs Helios in the same QEMU
	// session as Windows. Use it when the install screen stays blank on the
	// virtio-gpu framebuffer. Stripped on the first start after the install.
	BootstrapVGA bool
}

// heliosBootstrapVGADevice is the install-only standard VGA adapter, at the
// same fixed slot Helios' launcher uses so it enumerates ahead of the
// virtio-gpu.
const heliosBootstrapVGADevice = "VGA,id=bootstrap-gpu,bus=pcie.0,addr=0x1"

// heliosSetupName is the installer's file name on the extras ISO and in the
// guest; heliosWatchName is vee's reboot-orchestration script beside it.
const (
	heliosSetupName = "HeliosSetup.exe"
	heliosWatchName = "vee-helios-watch.ps1"
)

func (h *HeliosOptions) validate() error {
	if h.SetupExe == "" {
		return fmt.Errorf("helios: --helios-setup is required (path to HeliosSetup.exe from the Helios Windows bundle)")
	}
	if !strings.EqualFold(filepath.Ext(h.SetupExe), ".exe") {
		return fmt.Errorf("helios: --helios-setup %q is not an .exe — unzip helios-windows-x64-*.zip and pass HeliosSetup.exe", h.SetupExe)
	}
	fi, err := os.Stat(h.SetupExe)
	if err != nil {
		return fmt.Errorf("helios: --helios-setup: %w", err)
	}
	if fi.IsDir() {
		return fmt.Errorf("helios: --helios-setup %q is a directory", h.SetupExe)
	}
	if h.QemuBinary == "" {
		return fmt.Errorf("helios: --qemu-binary is required (the qemu-helios fork's qemu-system-x86_64; stock QEMU cannot drive the Helios display path)")
	}
	if _, err := os.Stat(h.QemuBinary); err != nil {
		return fmt.Errorf("helios: --qemu-binary: %w", err)
	}
	return nil
}

// heliosVNCAddr derives a stable loopback VNC address from the VM name, so
// several Helios guests do not all fight over TCP 5900. It reuses the
// per-name SSH port slot (2200–2299) and maps it to VNC displays 10–109
// (TCP 5910–6009).
func heliosVNCAddr(name string) string {
	display := deterministicSSHPort(name) - 2200 + 10
	return "127.0.0.1:" + strconv.Itoa(display)
}

// applyHelios rewrites an x86_64 windows template config for Helios:
//
//   - Secure Boot off: Helios is test-signed (CI signs every bundle with a
//     throwaway key), Windows refuses test signing while Secure Boot is on,
//     and the installer never weakens Secure Boot itself. The plain OVMF code
//     replaces the secboot build and the secure-pflash global goes. The TPM
//     stays, and the answer file's LabConfig keys already bypass the Windows
//     11 Secure Boot check.
//   - GPU mode helios (virtio-gpu-gl-pci + Venus, egl-headless + VNC) on the
//     per-VM qemu-helios binary.
//   - No SPICE: egl-headless and SPICE are separate display paths, and the
//     Helios scanout is only visible on the egl-headless/VNC one. The install
//     itself is watched over the same VNC display — the device does plain 2D
//     scanout via OVMF's GOP and Windows' basic display until the driver is
//     installed.
func applyHelios(cfg *vm.VMConfig, conf *provider.Config, h *HeliosOptions) {
	if conf.OVMFCodePath != "" {
		cfg.UEFI.CodePath = conf.OVMFCodePath
	}
	cfg.Globals = nil
	cfg.SPICE = nil
	cfg.QemuBinary = h.QemuBinary
	vnc := h.VNC
	if vnc == "" {
		vnc = heliosVNCAddr(cfg.Name)
	}
	cfg.GPU = vm.GPUConfig{
		Mode:       vm.GPUHelios,
		HostMem:    h.HostMem,
		VNC:        vnc,
		RenderNode: h.RenderNode,
	}
	if h.BootstrapVGA {
		cfg.InstallDevices = append(cfg.InstallDevices, heliosBootstrapVGADevice)
	}
	// Games want more than the 8G/4 vCPU office default. --memory/--cpus
	// overrides still apply on top.
	cfg.Memory = "16G"
	cfg.CPUs, cfg.Cores = 8, 8
}

// heliosSetupPS1 is spliced into the first-logon script when the VM is a
// Helios guest. It runs after the virtio-win guest tools (which bind viogpudo
// to the virtio-gpu; Helios' -Automatic flow replaces it) and before the
// script's closing reboot.
//
// HeliosSetup.exe --silent --automatic enables test signing, registers its
// own at-startup resume task, and reports progress in
// %ProgramData%\Helios\provisioning-status.json — but it leaves the reboots
// to the caller (WinBoat polls that file). The vee watch task is that
// caller: see heliosWatchPS1.
const heliosSetupPS1 = `
# 5. WinBoat Helios vGPU driver (test-signed; Secure Boot is off on this VM).
# Persistent auto-logon first: the answer file auto-logs on once, and the
# provisioning reboots below use that up. Helios' smoke tests (and any game
# under test) need an interactive desktop session after every boot.
$wl = 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon'
Set-ItemProperty -Path $wl -Name AutoAdminLogon -Value '1'
Set-ItemProperty -Path $wl -Name DefaultUserName -Value '{{USER}}'
Set-ItemProperty -Path $wl -Name DefaultPassword -Value '{{PASS}}'
Remove-ItemProperty -Path $wl -Name AutoLogonCount -ErrorAction SilentlyContinue
Log "persistent auto-logon enabled for {{USER}}"
$heliosDir = "$env:ProgramData\vee\helios"
$heliosSrc = "$($unattend):\{{SETUP}}"
if (Test-Path $heliosSrc) {
  New-Item -ItemType Directory -Force -Path $heliosDir | Out-Null
  Copy-Item -Force $heliosSrc "$heliosDir\{{SETUP}}"
  Copy-Item -Force "$($unattend):\{{WATCH}}" "$heliosDir\{{WATCH}}"
  $act = New-ScheduledTaskAction -Execute 'powershell.exe' -Argument "-NoProfile -ExecutionPolicy Bypass -File $heliosDir\{{WATCH}}"
  $trg = New-ScheduledTaskTrigger -AtStartup
  Register-ScheduledTask -TaskName 'VeeHeliosWatch' -Action $act -Trigger $trg -User 'SYSTEM' -RunLevel Highest -Force | Out-Null
  Log "installing Helios from $heliosSrc"
  $p = Start-Process "$heliosDir\{{SETUP}}" -ArgumentList @('--silent', '--automatic', '--log', "$heliosDir\setup.log") -Wait -PassThru
  Log "HeliosSetup exit code: $($p.ExitCode) (3010 = reboot required)"
  $status = "$env:ProgramData\Helios\provisioning-status.json"
  if (Test-Path $status) {
    $s = (Get-Content $status -Raw | ConvertFrom-Json).status
    Log "Helios provisioning status: $s"
    Set-Content -Path "$heliosDir\handled-status" -Value $s
  }
} else {
  Log "WARNING: Helios installer not found at $heliosSrc"
}
`

// heliosWatchPS1 runs at every startup (as SYSTEM) until Helios reports a
// final state. It reboots once per distinct "*-restart-required" status:
// Helios' own resume task needs a boot to move from one state to the next,
// and the status file still holds the previous value until that task runs,
// so rebooting on a status already handled would loop forever. The reboot
// count is capped for the same reason.
const heliosWatchPS1 = `$ErrorActionPreference = 'Continue'
$dir = "$env:ProgramData\vee\helios"
$log = "$env:SystemDrive\vee-helios.log"
function Log($m) { "$([DateTime]::Now.ToString('s')) $m" | Out-File -FilePath $log -Append -Encoding utf8 }
$status = "$env:ProgramData\Helios\provisioning-status.json"
$handled = "$dir\handled-status"
$countFile = "$dir\reboot-count"
$count = 0
if (Test-Path $countFile) { $count = [int](Get-Content $countFile -Raw) }
function Done($why) {
  Log $why
  Unregister-ScheduledTask -TaskName 'VeeHeliosWatch' -Confirm:$false -ErrorAction SilentlyContinue
  exit 0
}
Log "watch start (reboots so far: $count)"
for ($i = 0; $i -lt 180; $i++) {
  Start-Sleep -Seconds 10
  if (-not (Test-Path $status)) { continue }
  try { $j = Get-Content $status -Raw | ConvertFrom-Json } catch { continue }
  $s = [string]$j.status
  if ($s -eq 'finished') { Done "Helios provisioning finished" }
  if ($s -eq 'failed') { Done "Helios provisioning FAILED: $($j.message)" }
  if ($s -like '*restart-required') {
    $last = ''
    if (Test-Path $handled) { $last = (Get-Content $handled -Raw).Trim() }
    if ($s -eq $last) { continue }
    if ($count -ge 4) { Done "giving up after $count reboots (status: $s)" }
    Set-Content -Path $handled -Value $s
    Set-Content -Path $countFile -Value ($count + 1)
    Log "status $s - rebooting"
    Restart-Computer -Force
    exit 0
  }
}
Log "watch timed out; will retry next boot"
`

func renderHeliosSetupPS1() string {
	return strings.NewReplacer(
		"{{SETUP}}", heliosSetupName,
		"{{WATCH}}", heliosWatchName,
		"{{USER}}", winAdminUser,
		"{{PASS}}", winAdminPass,
	).Replace(heliosSetupPS1)
}

// stageHelios copies HeliosSetup.exe and the watch script into the extras ISO
// staging tree.
func stageHelios(stage, setupPath string) error {
	data, err := os.ReadFile(setupPath) //nolint:gosec // G304: the user-supplied installer path is the point of --helios-setup.
	if err != nil {
		return fmt.Errorf("read Helios installer: %w", err)
	}
	//nolint:gosec // G703: destination is the program-controlled staging dir joined with a constant file name.
	if err := os.WriteFile(filepath.Join(stage, heliosSetupName), data, 0o600); err != nil {
		return fmt.Errorf("stage Helios installer: %w", err)
	}
	if err := os.WriteFile(filepath.Join(stage, heliosWatchName), []byte(heliosWatchPS1), 0o600); err != nil {
		return fmt.Errorf("stage Helios watch script: %w", err)
	}
	return nil
}
