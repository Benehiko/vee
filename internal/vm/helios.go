package vm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

// EncodedPowerShell returns a cmd.exe-safe command line that runs script in
// Windows PowerShell via -EncodedCommand (base64 of UTF-16LE). Windows' sshd
// hands exec requests to cmd.exe, whose quoting rules mangle PowerShell; the
// encoded form contains only [A-Za-z0-9+/=], so nothing needs quoting.
func EncodedPowerShell(script string) string {
	u := utf16.Encode([]rune(script))
	buf := make([]byte, 2*len(u))
	for i, r := range u {
		binary.LittleEndian.PutUint16(buf[2*i:], r)
	}
	return "powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -EncodedCommand " +
		base64.StdEncoding.EncodeToString(buf)
}

// HeliosStatus is the guest's Helios provisioning state, read from
// %ProgramData%\Helios\provisioning-status.json (the file WinBoat polls).
// Status is one of waiting, test-signing-restart-required,
// driver-restart-required, finished, failed — or "not-started" when the
// installer has not written the file yet.
type HeliosStatus struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// Final reports whether provisioning reached an end state.
func (s HeliosStatus) Final() bool { return s.Status == "finished" || s.Status == "failed" }

const heliosStatusPS1 = `$p = Join-Path $env:ProgramData 'Helios\provisioning-status.json'
if (Test-Path -LiteralPath $p) { Get-Content -LiteralPath $p -Raw } else { '{"status":"not-started"}' }
`

// ReadHeliosStatus returns the guest's Helios provisioning state.
func (m *Manager) ReadHeliosStatus(ctx context.Context, name string) (HeliosStatus, error) {
	cfg, err := m.LoadConfig(name)
	if err != nil {
		return HeliosStatus{}, fmt.Errorf("VM %q not found: %w", name, err)
	}
	if cfg.GPU.Mode != GPUHelios {
		return HeliosStatus{}, fmt.Errorf("VM %q is not a Helios guest (gpu mode %q)", name, cfg.GPU.Mode)
	}
	out, err := m.GuestRun(ctx, name, EncodedPowerShell(heliosStatusPS1), 30*time.Second)
	if err != nil {
		return HeliosStatus{}, err
	}
	return parseHeliosStatus(out)
}

func parseHeliosStatus(out []byte) (HeliosStatus, error) {
	// PowerShell may emit a UTF-8 BOM or progress noise before the JSON.
	out = bytes.TrimPrefix(bytes.TrimSpace(out), []byte("\xef\xbb\xbf"))
	if i := bytes.IndexByte(out, '{'); i > 0 {
		out = out[i:]
	}
	var s HeliosStatus
	if err := json.Unmarshal(out, &s); err != nil {
		return HeliosStatus{}, fmt.Errorf("parse Helios provisioning status %q: %w", out, err)
	}
	if s.Status == "" {
		return HeliosStatus{}, fmt.Errorf("helios provisioning status has no status field: %q", out)
	}
	return s, nil
}

// WaitHelios polls the guest's Helios provisioning state until it is final
// or timeout passes. SSH failures are retried: provisioning reboots the
// guest (test signing, then driver activation), so the guest is unreachable
// for stretches. onChange is called whenever the observed status changes.
func (m *Manager) WaitHelios(ctx context.Context, name string, timeout time.Duration, onChange func(HeliosStatus)) (HeliosStatus, error) {
	deadline := time.Now().Add(timeout)
	var last HeliosStatus
	var lastErr error
	for {
		s, err := m.ReadHeliosStatus(ctx, name)
		switch {
		case err == nil:
			if s != last && onChange != nil {
				onChange(s)
			}
			last = s
			if s.Final() {
				return s, nil
			}
		case strings.Contains(err.Error(), "is not a Helios guest"), strings.Contains(err.Error(), "not found"):
			return HeliosStatus{}, err
		default:
			lastErr = err
		}
		if time.Now().After(deadline) {
			if lastErr != nil && last.Status == "" {
				return last, fmt.Errorf("helios provisioning on %q not finished after %s (last error: %w)", name, timeout, lastErr)
			}
			return last, fmt.Errorf("helios provisioning on %q not finished after %s (status: %s)", name, timeout, last.Status)
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
}

// heliosVerifyPS1 runs Helios' own Verify-Helios.ps1 -RunSmokeTests. It runs
// as a child process with OS-level stdout/stderr redirection: Windows
// PowerShell 5.1's in-language redirection (*>, 2>) wraps a native probe's
// stderr lines as error records, and Verify-Helios.ps1 runs with
// ErrorActionPreference=Stop, so a probe's diagnostic chatter on stderr
// would abort an otherwise passing run. The
// smoke probes create D3D11/D3D12 devices, Vulkan instances and WGL contexts,
// and Verify-Helios refuses to run them in session 0 — which is where an SSH
// session lives. So the probe runs as an Interactive-logon scheduled task in
// the auto-logged-on desktop session of the same account, and this script
// waits for it, then prints its log between markers plus the exit code.
const heliosVerifyPS1 = `$ErrorActionPreference = 'Stop'
$verify = Join-Path $env:ProgramData 'Helios\Verify-Helios.ps1'
if (-not (Test-Path -LiteralPath $verify)) { Write-Output 'VEE-HELIOS-ERROR: Verify-Helios.ps1 not found - Helios is not installed (check: vee helios status)'; exit 0 }
$dir = Join-Path $env:ProgramData 'vee\helios'
New-Item -ItemType Directory -Force -Path $dir | Out-Null
$out = Join-Path $dir 'verify.log'
$rc = Join-Path $dir 'verify.rc'
$runner = Join-Path $dir 'verify-run.ps1'
Remove-Item -LiteralPath $out, $rc -ErrorAction SilentlyContinue
Set-Content -LiteralPath $runner -Encoding UTF8 -Value @'
$dir = Join-Path $env:ProgramData 'vee\helios'
$out = Join-Path $dir 'verify.stdout'
$err = Join-Path $dir 'verify.stderr'
$code = 1
try {
  $verifyArgs = @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', (Join-Path $env:ProgramData 'Helios\Verify-Helios.ps1'), '-RunSmokeTests')
  $p = Start-Process -FilePath powershell.exe -ArgumentList $verifyArgs -NoNewWindow -Wait -PassThru -RedirectStandardOutput $out -RedirectStandardError $err
  $code = $p.ExitCode
} catch {
  $_ | Out-String | Out-File -FilePath $err -Append
}
Get-Content -LiteralPath $out, $err -ErrorAction SilentlyContinue | Set-Content -LiteralPath (Join-Path $dir 'verify.log')
Set-Content -LiteralPath (Join-Path $dir 'verify.rc') -Value $code
'@
$task = 'VeeHeliosVerify'
$action = New-ScheduledTaskAction -Execute 'powershell.exe' -Argument "-NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File $runner"
$principal = New-ScheduledTaskPrincipal -UserId $env:USERNAME -LogonType Interactive -RunLevel Highest
Register-ScheduledTask -TaskName $task -Action $action -Principal $principal -Force | Out-Null
Start-ScheduledTask -TaskName $task
$deadline = (Get-Date).AddSeconds({{TIMEOUT}})
while (-not (Test-Path -LiteralPath $rc) -and (Get-Date) -lt $deadline) { Start-Sleep -Seconds 2 }
Unregister-ScheduledTask -TaskName $task -Confirm:$false -ErrorAction SilentlyContinue
Write-Output 'VEE-HELIOS-LOG-BEGIN'
if (Test-Path -LiteralPath $out) { Get-Content -LiteralPath $out }
Write-Output 'VEE-HELIOS-LOG-END'
if (Test-Path -LiteralPath $rc) { Write-Output ('VEE-HELIOS-RC=' + (Get-Content -LiteralPath $rc -Raw).Trim()) } else { Write-Output 'VEE-HELIOS-RC=timeout' }
`

// ErrHeliosVerifyFailed is returned (wrapped) when the smoke tests ran and
// reported a failure, as opposed to vee being unable to run them.
var ErrHeliosVerifyFailed = errors.New("helios verification failed")

// VerifyHelios runs Helios' smoke tests in the guest's desktop session and
// returns their log. The error wraps ErrHeliosVerifyFailed when the tests ran
// and failed.
func (m *Manager) VerifyHelios(ctx context.Context, name string, timeout time.Duration) (string, error) {
	cfg, err := m.LoadConfig(name)
	if err != nil {
		return "", fmt.Errorf("VM %q not found: %w", name, err)
	}
	if cfg.GPU.Mode != GPUHelios {
		return "", fmt.Errorf("VM %q is not a Helios guest (gpu mode %q)", name, cfg.GPU.Mode)
	}
	secs := int(timeout.Seconds())
	if secs < 30 {
		secs = 30
	}
	script := strings.Replace(heliosVerifyPS1, "{{TIMEOUT}}", strconv.Itoa(secs), 1)
	// SSH budget: the in-guest wait plus headroom for task setup and output.
	out, err := m.GuestRun(ctx, name, EncodedPowerShell(script), time.Duration(secs+60)*time.Second)
	if err != nil {
		return "", err
	}
	return parseHeliosVerify(string(out))
}

func parseHeliosVerify(out string) (string, error) {
	out = strings.ReplaceAll(out, "\r\n", "\n")
	if i := strings.Index(out, "VEE-HELIOS-ERROR: "); i >= 0 {
		msg, _, _ := strings.Cut(out[i+len("VEE-HELIOS-ERROR: "):], "\n")
		return "", errors.New(strings.TrimSpace(msg))
	}
	var log string
	if _, rest, ok := strings.Cut(out, "VEE-HELIOS-LOG-BEGIN\n"); ok {
		log, _, _ = strings.Cut(rest, "VEE-HELIOS-LOG-END")
	}
	_, rc, ok := strings.Cut(out, "VEE-HELIOS-RC=")
	if !ok {
		return log, fmt.Errorf("unexpected verify output (no exit code marker):\n%s", out)
	}
	rc, _, _ = strings.Cut(rc, "\n")
	switch strings.TrimSpace(rc) {
	case "0":
		return log, nil
	case "timeout":
		return log, fmt.Errorf("smoke tests did not finish in time — is the guest logged in to its desktop? (the probes need the interactive session)")
	default:
		return log, fmt.Errorf("%w (exit code %s)", ErrHeliosVerifyFailed, strings.TrimSpace(rc))
	}
}
