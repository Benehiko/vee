package templates

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/Benehiko/vee/internal/images"
	"github.com/Benehiko/vee/internal/platform"
	"github.com/Benehiko/vee/internal/qemu"
	"github.com/Benehiko/vee/internal/vm"
	"github.com/Benehiko/vee/internal/vzfirstboot"
	"github.com/Benehiko/vee/internal/vzhelper"
	"github.com/Benehiko/vee/provider"
)

// MacOSOptions selects how the macOS guest's images come to exist.
type MacOSOptions struct {
	// IPSW is "latest" (default), an https URL, or a local .ipsw path. Used
	// by the restore path; ignored when MacosvmDir is set.
	IPSW string
	// MacosvmDir imports an already-restored macosvm bundle (macosvm.json +
	// disk/aux images) instead of restoring from an IPSW.
	MacosvmDir string
	// DiskSize sizes the fresh raw disk for the restore path (default 64G).
	DiskSize string
	// Memory/CPUs — zero means template defaults (raised to the restore
	// image's minimums).
	Memory string
	CPUs   int
	// User is the admin account the first-boot patch creates (default
	// "vee"). Ignored when SkipFirstBoot is set.
	User string
	// SSHPublicKeys are authorized for that account.
	SSHPublicKeys []string
	// Password sets the account's login password; generated when empty.
	Password string
	// Hostname sets the guest's LocalHostName.
	Hostname string
	// SkipFirstBoot leaves the restored guest untouched, so it boots into
	// Setup Assistant and must be provisioned by hand at its display.
	SkipFirstBoot bool
}

// NewMacOSConfig creates a macOS guest for the vz backend (issue #51):
// either restores macOS from an IPSW via vee-vz-helper (default) or imports
// a macosvm bundle. Apple Silicon macOS hosts only. Note Apple's SLA allows
// at most two concurrently running macOS VMs, on Apple hardware only.
func NewMacOSConfig(ctx context.Context, p provider.Provider, name string, opts MacOSOptions) (*vm.VMConfig, error) {
	if !platform.IsMacOS() || platform.HostArch() != "arm64" {
		return nil, fmt.Errorf("the macos template requires an Apple Silicon macOS host (got %s/%s)",
			platform.HostOS(), platform.HostArch())
	}

	// Validate the first-boot payload up front: a bad account name must not
	// surface only after a multi-gigabyte pull and a long restore.
	fbOpts, err := firstBootOptions(name, opts)
	if err != nil {
		return nil, err
	}

	memory := opts.Memory
	if memory == "" {
		memory = "8G"
	}
	cpus := opts.CPUs
	if cpus <= 0 {
		cpus = 4
	}

	vmDir := filepath.Join(p.Config().StoragePath, name)
	// The restore/import rewrites disk and aux images BEFORE Manager.Create
	// runs any of its checks, so an existing VM must be refused here — a
	// name collision would silently destroy an installed guest (even a
	// running one).
	if _, err := os.Stat(filepath.Join(vmDir, "vm.yaml")); err == nil {
		return nil, fmt.Errorf("VM %q already exists — delete it first (vee delete %s) or pass --reinstall", name, name)
	}
	// Anything under vmDir from here on was created by this call; clean it
	// up when the restore/import fails or is interrupted so tens of GB of
	// partial images don't linger invisibly (no vm.yaml = not in vee list).
	freshDir := true
	if _, err := os.Stat(vmDir); err == nil {
		freshDir = false
	}
	cleanup := func(err error) error {
		if freshDir {
			_ = os.RemoveAll(vmDir)
		}
		return err
	}

	storageDir := filepath.Join(vmDir, "storage")
	if err := os.MkdirAll(storageDir, 0o750); err != nil {
		return nil, err
	}
	diskPath := filepath.Join(storageDir, "disk.img")
	auxPath := filepath.Join(storageDir, "aux.img")

	cfg := &vm.VMConfig{
		Name:     name,
		Template: "macos",
		Backend:  "vz",
		Memory:   memory,
		CPUs:     cpus,
		Disks:    []vm.DiskConfig{{Path: diskPath, Format: "raw"}},
		NIC: vm.NICConfig{
			// The guest IP is resolved from the host DHCP leases by MAC, so
			// pin it at create time.
			MAC: qemu.DeterministicMAC(name),
		},
		// The disk arrives fully installed (restore or import) — never run
		// the install-ISO state machine.
		SkipInstall: true,
	}

	if opts.MacosvmDir != "" {
		macCfg, err := importMacosvmBundle(opts.MacosvmDir, diskPath, auxPath)
		if err != nil {
			return nil, cleanup(err)
		}
		cfg.MacOS = macCfg
		// An imported bundle is already set up by whoever restored it; do not
		// rewrite its accounts or daemons.
		return cfg, nil
	}

	macCfg, minCPUs, minMem, err := restoreMacOS(ctx, p, name, opts.IPSW, opts.DiskSize, diskPath, auxPath, cpus, memory)
	if err != nil {
		return nil, cleanup(err)
	}
	cfg.MacOS = macCfg
	// Persist the image minimums so later consumers (create-time overrides,
	// config edits, start) can clamp to them too, then raise this config.
	cfg.MacOS.MinCPUs = minCPUs
	cfg.MacOS.MinMemoryBytes = minMem
	ClampMacOSMinimums(cfg)

	if err := patchFirstBoot(ctx, cfg, vmDir, diskPath, opts, fbOpts); err != nil {
		return nil, cleanup(err)
	}
	return cfg, nil
}

// firstBootOptions builds and validates the guest payload options. The
// hostname defaults to the VM name, matching every other template.
func firstBootOptions(name string, opts MacOSOptions) (vzfirstboot.Options, error) {
	user := opts.User
	if user == "" {
		user = "vee"
	}
	hostname := opts.Hostname
	if hostname == "" {
		hostname = name
	}
	password := opts.Password
	if password == "" {
		// Typed at the guest's login window and at Screen Sharing prompts;
		// keep it memorable. Override with --password.
		password = user
	}
	fb := vzfirstboot.Options{
		User:                user,
		Password:            password,
		SSHPublicKeys:       opts.SSHPublicKeys,
		Hostname:            hostname,
		EnableScreenSharing: true,
	}
	if opts.SkipFirstBoot || opts.MacosvmDir != "" {
		return fb, nil
	}
	if err := fb.Validate(); err != nil {
		return fb, err
	}
	return fb, nil
}

// patchFirstBoot provisions the restored guest so it is reachable without a
// GUI session. A fresh restore otherwise boots into Setup Assistant, where
// nothing vee drives (SSH, IP resolution, health checks) is available.
//
// The patch needs sudo for one step — launchd only loads root-owned daemons —
// so a refusal is reported as a warning with the manual fallback rather than
// discarding a completed restore.
func patchFirstBoot(ctx context.Context, cfg *vm.VMConfig, vmDir, diskPath string, opts MacOSOptions, fbOpts vzfirstboot.Options) error {
	if opts.SkipFirstBoot {
		fmt.Println("Skipping first-boot provisioning: the guest will boot into Setup Assistant (use `vee view` to complete it).")
		return nil
	}
	fmt.Println("Provisioning the guest for headless first boot (skips Setup Assistant, creates the admin account, enables Remote Login and Screen Sharing)...")
	res, err := vzfirstboot.Patch(ctx, diskPath, fbOpts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: first-boot provisioning failed: %v\n"+
			"The VM is installed and startable, but it will boot into Setup Assistant — "+
			"complete it at the guest's display, then enable Remote Login manually.\n", err)
		return nil
	}

	cfg.SSHUser = fbOpts.User
	// Print the password before persisting it: if the write fails, this is
	// the only record of a credential that already exists in the guest.
	fmt.Printf("Guest admin account %q created. GUI login password: %s\n", fbOpts.User, res.Password)
	if err := writeGuestCredentials(vmDir, fbOpts.User, res.Password); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not record the guest password in the VM directory (%v) — note it down now.\n", err)
		return nil
	}
	fmt.Printf("Also saved to %s\n", filepath.Join(vmDir, guestCredentialsFile))
	return nil
}

// guestCredentialsFile records the generated guest login password inside the
// VM directory. SSH uses vee's key; this is for the GUI login window.
const guestCredentialsFile = "macos-credentials.txt" //nolint:gosec // a file name, not a credential

func writeGuestCredentials(vmDir, user, password string) error {
	body := fmt.Sprintf("# Written by vee. GUI login for this macOS guest; SSH uses your vee key.\nuser: %s\npassword: %s\n", user, password)
	return os.WriteFile(filepath.Join(vmDir, guestCredentialsFile), []byte(body), 0o600)
}

// ClampMacOSMinimums raises a macOS guest's CPU/memory to the restored
// image's recorded minimums. A guest configured below them will not boot,
// and the failure would only surface at start — long after an expensive
// restore.
func ClampMacOSMinimums(cfg *vm.VMConfig) {
	if cfg.MacOS == nil {
		return
	}
	if min := cfg.MacOS.MinCPUs; min > 0 && (cfg.CPUs <= 0 || uint64(cfg.CPUs) < min) { //nolint:gosec // cpus checked non-negative
		cfg.CPUs = int(min) //nolint:gosec // VM CPU counts are tiny
	}
	if min := cfg.MacOS.MinMemoryBytes; min > 0 {
		if cur, err := vzhelper.ParseMemoryBytes(cfg.Memory); err != nil || cur < min {
			cfg.Memory = strconv.FormatUint(min>>20, 10) + "M"
		}
	}
}

// restoreMacOS pulls the IPSW (cache-aware), creates a sparse raw disk and
// runs `vee-vz-helper --restore`, streaming its progress to stdout. Returns
// the macos config section plus the image's CPU/memory minimums.
func restoreMacOS(ctx context.Context, p provider.Provider, name, ipsw, diskSize, diskPath, auxPath string, cpus int, memory string) (*vm.MacOSConfig, uint64, uint64, error) {
	// Resolve the helper BEFORE any multi-GB download: a missing helper
	// must fail in seconds, not after a multi-gigabyte pull.
	helperPath, err := vzhelper.ResolveHelper()
	if err != nil {
		return nil, 0, 0, err
	}

	ipswPath := ipsw
	if _, err := os.Stat(ipsw); err != nil || ipsw == "" {
		// Not a local file: treat as "latest" or URL and pull into the cache.
		img, err := images.NewMacOSImage(p, ipsw)
		if err != nil {
			return nil, 0, 0, err
		}
		fmt.Println("Pulling the macOS restore image (15-20 GB; cached for reuse)...")
		if err := img.Download(ctx); err != nil {
			return nil, 0, 0, fmt.Errorf("pull macOS restore image: %w", err)
		}
		ipswPath = img.AbsolutePath()
	}

	if diskSize == "" {
		diskSize = "64G"
	}
	diskBytes, err := vzhelper.ParseMemoryBytes(diskSize)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("disk size: %w", err)
	}
	if err := createSparseFile(diskPath, int64(diskBytes)); err != nil { //nolint:gosec // sizes far below int64 max
		return nil, 0, 0, err
	}

	memBytes, err := vzhelper.ParseMemoryBytes(memory)
	if err != nil {
		return nil, 0, 0, err
	}

	vmDir := filepath.Dir(filepath.Dir(diskPath))
	//nolint:gosec // helperPath from ResolveHelper; remaining args are vee-derived paths/numbers
	cmd := exec.CommandContext(ctx, helperPath,
		"--vm-dir", vmDir,
		"--restore", ipswPath,
		"--restore-disk", diskPath,
		"--restore-aux", auxPath,
		"--restore-cpus", strconv.Itoa(cpus),
		"--restore-memory", strconv.FormatUint(memBytes, 10),
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, 0, 0, fmt.Errorf("macOS restore failed (VM %q): %w", name, err)
	}

	res, err := vzhelper.LoadRestoreResult(vmDir)
	if err != nil {
		return nil, 0, 0, err
	}
	return &vm.MacOSConfig{
		AuxiliaryStorage:  auxPath,
		HardwareModel:     res.HardwareModel,
		MachineIdentifier: res.MachineIdentifier,
	}, res.MinCPUs, res.MinMemoryBytes, nil
}

// macosvmManifest is the subset of macosvm.json vee needs: the platform
// blobs (the same base64 encoding vee's own config uses) and the image file
// names.
type macosvmManifest struct {
	HardwareModel []byte           `json:"hardwareModel"`
	MachineID     []byte           `json:"machineId"`
	Storage       []macosvmStorage `json:"storage"`
}

type macosvmStorage struct {
	Type string `json:"type"`
	File string `json:"file"`
}

// importMacosvmBundle copies a macosvm VM's images into the vee VM dir and
// lifts the platform blobs out of its manifest.
func importMacosvmBundle(dir, diskPath, auxPath string) (*vm.MacOSConfig, error) {
	data, err := os.ReadFile(filepath.Join(dir, "macosvm.json")) //nolint:gosec // operator-provided import directory
	if err != nil {
		return nil, fmt.Errorf("read macosvm manifest: %w", err)
	}
	var manifest macosvmManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse macosvm.json: %w", err)
	}
	if len(manifest.HardwareModel) == 0 || len(manifest.MachineID) == 0 {
		return nil, fmt.Errorf("macosvm.json is missing hardwareModel or machineId")
	}

	srcDisk, srcAux := "", ""
	for _, s := range manifest.Storage {
		switch s.Type {
		case "disk":
			srcDisk = s.File
		case "aux":
			srcAux = s.File
		}
	}
	if srcDisk == "" || srcAux == "" {
		return nil, fmt.Errorf("macosvm.json does not declare both a disk and an aux storage entry")
	}
	for _, c := range []struct{ src, dst string }{
		{filepath.Join(dir, srcDisk), diskPath},
		{filepath.Join(dir, srcAux), auxPath},
	} {
		fmt.Printf("Importing %s -> %s\n", c.src, c.dst)
		if err := copySparseAware(c.src, c.dst); err != nil {
			return nil, fmt.Errorf("import %s: %w", c.src, err)
		}
	}

	return &vm.MacOSConfig{
		AuxiliaryStorage:  auxPath,
		HardwareModel:     manifest.HardwareModel,
		MachineIdentifier: manifest.MachineID,
	}, nil
}

// createSparseFile makes (or grows) a sparse raw disk image. An existing
// file is left untouched so re-running create after a failed restore does
// not wipe partial state silently — the restore rewrites it anyway.
func createSparseFile(path string, size int64) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // vee-managed storage path
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() >= size {
		return nil
	}
	return f.Truncate(size)
}

// copySparseAware clones a file, preserving sparseness where the platform
// supports it (APFS clonefile via cp -c on macOS; falls back to io copy).
func copySparseAware(src, dst string) error {
	if platform.IsMacOS() {
		//nolint:noctx,gosec // constructor path without ctx; paths are vee-derived + operator import dir
		if err := exec.Command("cp", "-c", src, dst).Run(); err == nil {
			return nil
		}
		// clonefile fails across filesystems; fall through to a plain copy.
	}
	in, err := os.Open(src) //nolint:gosec // operator-provided import path
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // vee-managed storage path
	if err != nil {
		return err
	}
	if _, err := out.ReadFrom(in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
