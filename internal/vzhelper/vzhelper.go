// Package vzhelper defines the contract between vee and the vee-vz-helper
// binary (cmd/vee-vz-helper): the machine-spec file the manager writes into
// the VM directory, the newline-delimited JSON protocol spoken over the
// helper's unix control socket, and the result file the helper writes when
// the VM stops. The package is imported by both sides and must stay free of
// Virtualization.framework (cgo) dependencies so the manager builds on every
// platform.
package vzhelper

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Entitlements is the plist the helper must be codesigned with
// (com.apple.security.virtualization; ad-hoc signatures suffice). Embedded so
// vee can re-sign a helper whose signature was disturbed, mirroring the
// qemubin hardening pattern.
//
//go:embed vz.entitlements
var Entitlements []byte

// Well-known file names inside a VM directory. The helper is pointed at the
// directory (--vm-dir) and derives everything else from these.
const (
	// SpecFileName is the machine spec the manager writes before spawning
	// the helper.
	SpecFileName = "vz-machine.json"
	// ControlSocketName is the unix socket the helper serves ops on for the
	// VM's lifetime. Its appearance is the start-confirmation gate.
	ControlSocketName = "vz-control.sock"
	// ResultFileName is written by the helper when the VM stops, recording
	// whether the stop was requested by the host or initiated by the guest.
	ResultFileName = "vz-result.json"
	// LogFileName receives the helper's stdout/stderr (the vz analog of
	// qemu.log).
	LogFileName = "vz-helper.log"
	// EFIVariableStoreName is the per-VM EFI variable store (NVRAM) backing a
	// Linux guest's VZEFIBootLoader. The helper creates it on first boot.
	EFIVariableStoreName = "efi-vars.fd"
	// SerialLogName receives a Linux guest's virtio console output — the same
	// name the QEMU backend uses, so tooling finds it in either case.
	SerialLogName = "serial.log"
)

// Guest platforms a machine spec can describe. The zero value resolves to
// PlatformMacOS so spec files written before the field existed keep working.
const (
	// PlatformMacOS runs a macOS guest (VZMacPlatformConfiguration + macOS
	// boot loader) — requires the restore artifacts below.
	PlatformMacOS = "macos"
	// PlatformLinux runs a Linux guest (VZGenericPlatformConfiguration +
	// VZEFIBootLoader) from an EFI-bootable raw disk image (issue #127).
	PlatformLinux = "linux"
)

// ProtocolVersion identifies the control protocol this package defines,
// bumped only on incompatible changes (issue #61). Helpers that predate
// versioning (status/stop/wait-shutdown only) answer OpVersion with an
// "unknown op" error and are effectively version 0.
const ProtocolVersion = 1

// Control protocol ops.
const (
	// OpStatus returns the VM's current run state.
	OpStatus = "status"
	// OpStop asks the guest to shut down cleanly (VZVirtualMachine
	// requestStop — the ACPI-powerdown analog).
	OpStop = "stop"
	// OpWaitShutdown blocks until the VM stops, then reports whether the
	// guest initiated the shutdown. The manager uses it the way it uses QMP
	// SHUTDOWN events for QEMU VMs.
	OpWaitShutdown = "wait-shutdown"
	// OpVersion returns the helper's protocol version and build version, so
	// the manager can detect a mismatched vee / vee-vz-helper pair.
	OpVersion = "version"
	// OpVsockConnect bridges a guest-listening vsock port to a host unix
	// socket: the helper listens on Response.Path and opens a fresh
	// host→guest vsock connection to Request.Port for every connection
	// accepted there (the Virtualization.framework analog of connecting to a
	// vhost-vsock CID:port). Idempotent per port. Requires MachineSpec.Vsock.
	OpVsockConnect = "vsock-connect"
	// OpVsockListen accepts guest-initiated vsock connections to Request.Port
	// and forwards each one to the host unix socket at Request.Path (which
	// the caller must be listening on). Sending it again for the same port
	// retargets the forward. Requires MachineSpec.Vsock.
	OpVsockListen = "vsock-listen"
)

// Terminal reasons reported on the OpWaitShutdown response.
const (
	// ReasonGuest: the guest powered itself off (nobody sent OpStop).
	ReasonGuest = "guest"
	// ReasonHost: the host asked for the stop via OpStop.
	ReasonHost = "host"
	// ReasonError: the VM hit an internal Virtualization.framework error —
	// the crash analog. Watchers must NOT record this as a deliberate guest
	// shutdown (the daemon's autostart recovery keys off that distinction).
	ReasonError = "error"
)

// Request is a single control-socket command. One JSON object per line.
type Request struct {
	Op string `json:"op"`
	// Port is the guest vsock port for OpVsockConnect / OpVsockListen.
	Port uint32 `json:"port,omitempty"`
	// Path is the host unix socket OpVsockListen forwards guest connections
	// to. Must be absolute.
	Path string `json:"path,omitempty"`
}

// Response answers a Request. One JSON object per line.
type Response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	// State is the VM run state for OpStatus (e.g. "running", "stopped").
	State string `json:"state,omitempty"`
	// Reason is set on the OpWaitShutdown response: ReasonGuest, ReasonHost
	// or ReasonError.
	Reason string `json:"reason,omitempty"`
	// Guest mirrors Reason == ReasonGuest, kept for line-protocol
	// readability.
	Guest bool `json:"guest,omitempty"`
	// Path is the host unix socket the helper serves the bridge on, set on
	// the OpVsockConnect response.
	Path string `json:"path,omitempty"`
	// Protocol and Version are set on the OpVersion response: the protocol
	// version the helper speaks and its build version.
	Protocol int    `json:"protocol,omitempty"`
	Version  string `json:"version,omitempty"`
}

// Result is persisted to ResultFileName when the VM stops, so state can be
// reconstructed after the helper exits (e.g. by stale-VM cleanup).
type Result struct {
	// StopRequested is true when the host asked for the stop (OpStop or a
	// helper-side shutdown); false means the guest powered itself off.
	StopRequested bool `json:"stop_requested"`
	// Error carries the failure when the VM stopped due to an internal
	// Virtualization.framework error.
	Error string `json:"error,omitempty"`
}

// DisplaySpec sizes the mandatory macOS guest display. A macOS guest must
// always carry a graphics device — even headless — or it hangs in the boot
// loader.
type DisplaySpec struct {
	WidthPx  int64 `json:"width_px"`
	HeightPx int64 `json:"height_px"`
	PPI      int64 `json:"ppi"`
}

// DiskSpec is one virtio-blk attachment. Paths are absolute; the image must
// be raw (Virtualization.framework does not read qcow2).
type DiskSpec struct {
	Path     string `json:"path"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

// MachineSpec fully describes the VM the helper must run. []byte fields
// marshal as base64 — the same encoding macosvm.json uses for its blobs.
type MachineSpec struct {
	Name string `json:"name"`
	// Platform selects the guest platform: PlatformMacOS or PlatformLinux.
	// Empty resolves to PlatformMacOS (specs written before Linux support).
	Platform    string `json:"platform,omitempty"`
	CPUs        uint   `json:"cpus"`
	MemoryBytes uint64 `json:"memory_bytes"`
	// MAC is the NIC hardware address (NAT attachment). Required: guest IP
	// discovery matches it against the host DHCP leases.
	MAC   string     `json:"mac"`
	Disks []DiskSpec `json:"disks"`
	// AuxiliaryStorage is the absolute path to the VZMacAuxiliaryStorage
	// (NVRAM analog) created at restore time. macOS guests only.
	AuxiliaryStorage string `json:"auxiliary_storage,omitempty"`
	// HardwareModel and MachineIdentifier are the opaque
	// Virtualization.framework blobs bound to the installed guest. macOS
	// guests only.
	HardwareModel     []byte      `json:"hardware_model,omitempty"`
	MachineIdentifier []byte      `json:"machine_identifier,omitempty"`
	Display           DisplaySpec `json:"display"`
	// EFIVariableStore is the absolute path of the NVRAM file backing a Linux
	// guest's EFI boot loader. The helper creates the file when it does not
	// exist yet, so it must not be required to pre-exist. Linux guests only.
	EFIVariableStore string `json:"efi_variable_store,omitempty"`
	// SerialLog, when set, attaches a virtio console whose output is written
	// to this file (truncated per boot) — the Linux-guest analog of QEMU's
	// serial.log. macOS guests have no console device and leave it empty.
	SerialLog string `json:"serial_log,omitempty"`
	// Vsock attaches a virtio-vsock (VZVirtioSocketDevice) so the host and
	// guest share a private channel that needs no NAT networking; the
	// OpVsockConnect / OpVsockListen control ops drive it. Optional — the
	// QEMU-backend analog is the vhost-vsock device.
	Vsock bool `json:"vsock,omitempty"`
}

// PlatformName resolves the spec's guest platform, defaulting to macOS for
// specs written before the Platform field existed.
func (s *MachineSpec) PlatformName() string {
	if s.Platform == "" {
		return PlatformMacOS
	}
	return s.Platform
}

// DefaultDisplay matches macosvm's default screen so imported guests keep
// their resolution.
var DefaultDisplay = DisplaySpec{WidthPx: 1920, HeightPx: 1200, PPI: 80}

// Validate rejects specs the helper could not start, with actionable errors.
func (s *MachineSpec) Validate() error {
	if s.CPUs == 0 {
		return fmt.Errorf("vz machine spec: cpus must be > 0")
	}
	if s.MemoryBytes == 0 {
		return fmt.Errorf("vz machine spec: memory_bytes must be > 0")
	}
	if len(s.Disks) == 0 {
		return fmt.Errorf("vz machine spec: at least one disk is required")
	}
	for _, d := range s.Disks {
		if _, err := os.Stat(d.Path); err != nil {
			return fmt.Errorf("vz machine spec: disk image: %w", err)
		}
	}
	if s.MAC == "" {
		return fmt.Errorf("vz machine spec: mac is required")
	}
	switch s.PlatformName() {
	case PlatformMacOS:
		if s.AuxiliaryStorage == "" {
			return fmt.Errorf("vz machine spec: auxiliary_storage is required")
		}
		if _, err := os.Stat(s.AuxiliaryStorage); err != nil {
			return fmt.Errorf("vz machine spec: auxiliary storage: %w", err)
		}
		if len(s.HardwareModel) == 0 || len(s.MachineIdentifier) == 0 {
			return fmt.Errorf("vz machine spec: hardware_model and machine_identifier blobs are required (produced by the macOS restore, or importable from a macosvm.json)")
		}
	case PlatformLinux:
		// The variable store must be named but need not exist: the helper
		// creates it on the guest's first boot.
		if s.EFIVariableStore == "" {
			return fmt.Errorf("vz machine spec: efi_variable_store is required for a linux guest")
		}
		if len(s.HardwareModel) != 0 || len(s.MachineIdentifier) != 0 || s.AuxiliaryStorage != "" {
			return fmt.Errorf("vz machine spec: hardware_model / machine_identifier / auxiliary_storage are macOS restore artifacts — a linux guest must not carry them")
		}
	default:
		return fmt.Errorf("vz machine spec: unknown platform %q (valid: %q, %q)", s.Platform, PlatformMacOS, PlatformLinux)
	}
	return nil
}

// VsockBridgeGlob matches the per-port vsock bridge sockets the helper
// creates inside a VM directory (see VsockBridgePath). The helper removes
// stale matches at startup, like the control socket.
const VsockBridgeGlob = "vz-vsock-*.sock"

// VsockBridgePath returns the unix socket the helper bridges a guest vsock
// port on (the OpVsockConnect response path).
func VsockBridgePath(vmDir string, port uint32) string {
	return filepath.Join(vmDir, fmt.Sprintf("vz-vsock-%d.sock", port))
}

// SpecPath returns the machine-spec path inside a VM directory.
func SpecPath(vmDir string) string { return filepath.Join(vmDir, SpecFileName) }

// ControlSocketPath returns the control-socket path inside a VM directory.
func ControlSocketPath(vmDir string) string { return filepath.Join(vmDir, ControlSocketName) }

// ResultPath returns the result-file path inside a VM directory.
func ResultPath(vmDir string) string { return filepath.Join(vmDir, ResultFileName) }

// LogPath returns the helper-log path inside a VM directory.
func LogPath(vmDir string) string { return filepath.Join(vmDir, LogFileName) }

// EFIVariableStorePath returns the Linux-guest EFI variable store path inside
// a VM directory.
func EFIVariableStorePath(vmDir string) string { return filepath.Join(vmDir, EFIVariableStoreName) }

// SerialLogPath returns the Linux-guest console log path inside a VM
// directory.
func SerialLogPath(vmDir string) string { return filepath.Join(vmDir, SerialLogName) }

// WriteSpec atomically persists the machine spec into the VM directory.
func WriteSpec(vmDir string, spec *MachineSpec) error {
	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return err
	}
	tmp := SpecPath(vmDir) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, SpecPath(vmDir))
}

// LoadSpec reads and validates the machine spec from the VM directory.
func LoadSpec(vmDir string) (*MachineSpec, error) {
	data, err := os.ReadFile(SpecPath(vmDir)) //nolint:gosec // path derived from vee-managed VM directory
	if err != nil {
		return nil, err
	}
	var spec MachineSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("parse %s: %w", SpecFileName, err)
	}
	// Only a macOS guest needs the display defaulted: it must always carry a
	// graphics device or it hangs in the boot loader. Linux guests run
	// headless (their console goes to SerialLog).
	if spec.PlatformName() == PlatformMacOS && spec.Display == (DisplaySpec{}) {
		spec.Display = DefaultDisplay
	}
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	return &spec, nil
}

// WriteResult persists the stop outcome. Best-effort callers may ignore the
// returned error.
func WriteResult(vmDir string, res *Result) error {
	data, err := json.Marshal(res)
	if err != nil {
		return err
	}
	return os.WriteFile(ResultPath(vmDir), data, 0o600)
}

// LoadResult reads the stop outcome written by the helper, if any.
func LoadResult(vmDir string) (*Result, error) {
	data, err := os.ReadFile(ResultPath(vmDir)) //nolint:gosec // path derived from vee-managed VM directory
	if err != nil {
		return nil, err
	}
	var res Result
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// RestoreResultFileName is written by `vee-vz-helper --restore` next to the
// restored images: the artifacts vee must persist into the VM config.
const RestoreResultFileName = "vz-restore.json"

// RestoreResult carries the Virtualization.framework artifacts produced by an
// IPSW restore. Blob fields marshal as base64 (macosvm.json-compatible).
type RestoreResult struct {
	HardwareModel     []byte `json:"hardware_model"`
	MachineIdentifier []byte `json:"machine_identifier"`
	// Minimums the restore image's configuration requires; vee raises the VM
	// config to at least these.
	MinCPUs        uint64 `json:"min_cpus"`
	MinMemoryBytes uint64 `json:"min_memory_bytes"`
	// OSVersion/Build identify what was restored (e.g. "15.5" / "24F74").
	OSVersion string `json:"os_version,omitempty"`
	Build     string `json:"build,omitempty"`
}

// WriteRestoreResult persists the restore artifacts into a VM directory.
func WriteRestoreResult(vmDir string, res *RestoreResult) error {
	data, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(vmDir, RestoreResultFileName), data, 0o600)
}

// LoadRestoreResult reads the restore artifacts written by the helper.
func LoadRestoreResult(vmDir string) (*RestoreResult, error) {
	data, err := os.ReadFile(filepath.Join(vmDir, RestoreResultFileName)) //nolint:gosec // vee-managed VM directory
	if err != nil {
		return nil, err
	}
	var res RestoreResult
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, fmt.Errorf("parse %s: %w", RestoreResultFileName, err)
	}
	if len(res.HardwareModel) == 0 || len(res.MachineIdentifier) == 0 {
		return nil, fmt.Errorf("%s is missing the hardware model or machine identifier blob", RestoreResultFileName)
	}
	return &res, nil
}

// HelperBinary is the name of the Virtualization.framework helper.
const HelperBinary = "vee-vz-helper"

// ResolveHelper locates the helper binary: explicit override, the directory
// of the running vee binary (release tarballs ship them side by side), the
// vee-managed bin dir, then PATH.
//
// The resolved binary is also healed before it is returned — a helper
// downloaded with a browser carries macOS's quarantine flag, and Gatekeeper
// kills quarantined ad-hoc binaries on exec. Hardening here rather than at
// the call sites means every path that runs the helper (start, IPSW
// restore-URL query, restore) is covered.
func ResolveHelper() (string, error) {
	path, err := FindHelper()
	if err != nil {
		return "", err
	}
	return path, harden(path)
}

// FindHelper locates the helper binary without touching it: explicit override,
// the directory of the running vee binary (release tarballs ship them side by
// side), the vee-managed bin dir, then PATH.
//
// Callers that are about to *run* the helper want ResolveHelper, which also
// heals it. This one exists for callers that only want to know which binary
// would be used — reporting a version must not modify anything on disk.
func FindHelper() (string, error) {
	if p := os.Getenv("VEE_VZ_HELPER"); p != "" {
		if _, err := os.Stat(p); err != nil { //nolint:gosec // deliberate operator-provided override
			return "", fmt.Errorf("VEE_VZ_HELPER: %w", err)
		}
		return p, nil
	}
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), HelperBinary))
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".vee", "bin", HelperBinary))
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	if p, err := exec.LookPath(HelperBinary); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("%s not found (looked in $VEE_VZ_HELPER, next to the vee binary, ~/.vee/bin, $PATH) — install it from the darwin-arm64 release tarball or build it with `make vz-helper`", HelperBinary)
}

// LatestRestoreImageURL asks the helper for the newest macOS restore-image
// URL this HOST supports (the Virtualization.framework answer — more
// accurate than any global "latest", which a host on an older macOS may not
// be able to restore). The helper is required because the query is a cgo
// framework call and the vee binary builds with CGO_ENABLED=0.
func LatestRestoreImageURL(ctx context.Context, helperPath string) (string, error) {
	//nolint:gosec // helperPath comes from ResolveHelper / operator override
	out, err := exec.CommandContext(ctx, helperPath, "--print-restore-url").Output()
	if err != nil {
		// Surface the helper's stderr (the actual diagnostic — e.g. the
		// framework's network error) instead of a bare "exit status 1".
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("%s --print-restore-url: %w: %s", HelperBinary, err, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("%s --print-restore-url: %w", HelperBinary, err)
	}
	url := strings.TrimSpace(string(out))
	if !strings.HasPrefix(url, "https://") {
		return "", fmt.Errorf("unexpected restore-url output %q", url)
	}
	return url, nil
}

// ParseMemoryBytes converts vee memory strings ("16G", "4096M", bare MiB
// count "4096") to bytes, mirroring QEMU -m semantics used elsewhere in vee.
func ParseMemoryBytes(s string) (uint64, error) {
	in := strings.TrimSpace(s)
	if in == "" {
		return 0, fmt.Errorf("empty memory size")
	}
	mult := uint64(1024 * 1024) // QEMU -m default unit is MiB
	switch c := in[len(in)-1]; c {
	case 'k', 'K':
		mult = 1024
		in = in[:len(in)-1]
	case 'm', 'M':
		mult = 1024 * 1024
		in = in[:len(in)-1]
	case 'g', 'G':
		mult = 1024 * 1024 * 1024
		in = in[:len(in)-1]
	case 't', 'T':
		mult = 1024 * 1024 * 1024 * 1024
		in = in[:len(in)-1]
	}
	n, err := strconv.ParseUint(strings.TrimSpace(in), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse memory size %q: %w", s, err)
	}
	return n * mult, nil
}
