package vm

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestEncodedPowerShell(t *testing.T) {
	script := "Write-Output 'héllo \"x\"'"
	cmd := EncodedPowerShell(script)
	const prefix = "powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -EncodedCommand "
	if !strings.HasPrefix(cmd, prefix) {
		t.Fatalf("unexpected prefix: %q", cmd)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(cmd, prefix))
	if err != nil {
		t.Fatal(err)
	}
	u := make([]uint16, len(raw)/2)
	for i := range u {
		u[i] = uint16(raw[2*i]) | uint16(raw[2*i+1])<<8
	}
	if got := string(utf16.Decode(u)); got != script {
		t.Errorf("round trip = %q, want %q", got, script)
	}
}

func TestParseHeliosStatus(t *testing.T) {
	s, err := parseHeliosStatus([]byte("\xef\xbb\xbf{\"status\":\"failed\",\"message\":\"boom\"}\r\n"))
	if err != nil || s.Status != "failed" || s.Message != "boom" || !s.Final() {
		t.Fatalf("got %+v, %v", s, err)
	}
	s, err = parseHeliosStatus([]byte(`{"status":"driver-restart-required"}`))
	if err != nil || s.Final() {
		t.Fatalf("got %+v, %v", s, err)
	}
	if _, err := parseHeliosStatus([]byte("garbage")); err == nil {
		t.Error("garbage parsed")
	}
	if _, err := parseHeliosStatus([]byte(`{}`)); err == nil {
		t.Error("empty status accepted")
	}
}

func TestParseHeliosVerify(t *testing.T) {
	ok := "VEE-HELIOS-LOG-BEGIN\r\nRunning D3D12 smoke probe...\r\nhealthy\r\nVEE-HELIOS-LOG-END\r\nVEE-HELIOS-RC=0\r\n"
	log, err := parseHeliosVerify(ok)
	if err != nil || !strings.Contains(log, "D3D12") {
		t.Fatalf("ok: log=%q err=%v", log, err)
	}
	_, err = parseHeliosVerify("VEE-HELIOS-LOG-BEGIN\nx\nVEE-HELIOS-LOG-END\nVEE-HELIOS-RC=1\n")
	if !errors.Is(err, ErrHeliosVerifyFailed) {
		t.Errorf("rc=1: err = %v, want ErrHeliosVerifyFailed", err)
	}
	_, err = parseHeliosVerify("VEE-HELIOS-LOG-BEGIN\nVEE-HELIOS-LOG-END\nVEE-HELIOS-RC=timeout\n")
	if err == nil || errors.Is(err, ErrHeliosVerifyFailed) {
		t.Errorf("timeout: err = %v", err)
	}
	_, err = parseHeliosVerify("VEE-HELIOS-ERROR: Verify-Helios.ps1 not found\n")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("error marker: err = %v", err)
	}
}

func TestHeliosVerifyScriptRunsInteractive(t *testing.T) {
	// The smoke probes refuse session 0; the task must use an interactive logon.
	if !strings.Contains(heliosVerifyPS1, "-LogonType Interactive") {
		t.Error("verify task is not an interactive-logon task")
	}
	// In-language redirection turns probe stderr into terminating errors.
	if strings.Contains(heliosVerifyPS1, "-RunSmokeTests *>") || !strings.Contains(heliosVerifyPS1, "-RedirectStandardError") {
		t.Error("verify must run Verify-Helios.ps1 as a child process with OS-level redirection")
	}
	if !strings.Contains(heliosVerifyPS1, "{{TIMEOUT}}") {
		t.Error("timeout placeholder missing")
	}
}
