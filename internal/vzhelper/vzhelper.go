// Package vzhelper defines the contract between vee and the vee-vz-helper
// binary (cmd/vee-vz-helper): the machine-spec file the manager writes into
// the VM directory, the newline-delimited JSON protocol spoken over the
// helper's unix control socket, and the result file the helper writes when
// the VM stops. The package is imported by both sides and must stay free of
// Virtualization.framework (cgo) dependencies so the manager builds on every
// platform.
package vzhelper

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
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
)

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
	Name        string `json:"name"`
	CPUs        uint   `json:"cpus"`
	MemoryBytes uint64 `json:"memory_bytes"`
	// MAC is the NIC hardware address (NAT attachment). Required: guest IP
	// discovery matches it against the host DHCP leases.
	MAC   string     `json:"mac"`
	Disks []DiskSpec `json:"disks"`
	// AuxiliaryStorage is the absolute path to the VZMacAuxiliaryStorage
	// (NVRAM analog) created at restore time.
	AuxiliaryStorage string `json:"auxiliary_storage"`
	// HardwareModel and MachineIdentifier are the opaque
	// Virtualization.framework blobs bound to the installed guest.
	HardwareModel     []byte      `json:"hardware_model"`
	MachineIdentifier []byte      `json:"machine_identifier"`
	Display           DisplaySpec `json:"display"`
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
	if s.AuxiliaryStorage == "" {
		return fmt.Errorf("vz machine spec: auxiliary_storage is required")
	}
	if _, err := os.Stat(s.AuxiliaryStorage); err != nil {
		return fmt.Errorf("vz machine spec: auxiliary storage: %w", err)
	}
	if len(s.HardwareModel) == 0 || len(s.MachineIdentifier) == 0 {
		return fmt.Errorf("vz machine spec: hardware_model and machine_identifier blobs are required (produced by the macOS restore, or importable from a macosvm.json)")
	}
	if s.MAC == "" {
		return fmt.Errorf("vz machine spec: mac is required")
	}
	return nil
}

// SpecPath returns the machine-spec path inside a VM directory.
func SpecPath(vmDir string) string { return filepath.Join(vmDir, SpecFileName) }

// ControlSocketPath returns the control-socket path inside a VM directory.
func ControlSocketPath(vmDir string) string { return filepath.Join(vmDir, ControlSocketName) }

// ResultPath returns the result-file path inside a VM directory.
func ResultPath(vmDir string) string { return filepath.Join(vmDir, ResultFileName) }

// LogPath returns the helper-log path inside a VM directory.
func LogPath(vmDir string) string { return filepath.Join(vmDir, LogFileName) }

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
	if spec.Display == (DisplaySpec{}) {
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
