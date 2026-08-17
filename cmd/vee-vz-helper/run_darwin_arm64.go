//go:build darwin && arm64 && cgo

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Code-Hex/vz/v3"

	"github.com/Benehiko/vee/internal/buildinfo"
	"github.com/Benehiko/vee/internal/vzhelper"
)

// The native display window (issue #139) runs NSApplication's event loop, and
// AppKit only functions on the process's main OS thread — locking from init
// is the documented way to keep the main goroutine there.
func init() {
	runtime.LockOSThread()
}

func run() int {
	vmDir := flag.String("vm-dir", "", "VM directory containing "+vzhelper.SpecFileName)
	printRestoreURL := flag.Bool("print-restore-url", false, "print the newest macOS restore-image (IPSW) URL this host supports and exit")
	restore := flag.String("restore", "", "restore a macOS guest from this IPSW into --vm-dir instead of running a VM")
	restoreDisk := flag.String("restore-disk", "", "raw disk image for --restore (must exist; sparse file is fine)")
	restoreAux := flag.String("restore-aux", "", "auxiliary storage path for --restore (created)")
	restoreCPUs := flag.Uint("restore-cpus", 4, "CPU count for the restore VM (raised to the image minimum)")
	restoreMemory := flag.Uint64("restore-memory", 8<<30, "memory bytes for the restore VM (raised to the image minimum)")
	flag.Parse()

	if *printRestoreURL {
		url, err := vz.GetLatestSupportedMacOSRestoreImageURL()
		if err != nil {
			fmt.Fprintln(os.Stderr, "vee-vz-helper:", err)
			return 1
		}
		fmt.Println(url)
		return 0
	}
	if *vmDir == "" {
		fmt.Fprintln(os.Stderr, "usage: vee-vz-helper --vm-dir <dir> [--restore <ipsw> --restore-disk <img> --restore-aux <img>]")
		return 2
	}
	if *restore != "" {
		if err := runRestore(*vmDir, *restore, *restoreDisk, *restoreAux, *restoreCPUs, *restoreMemory); err != nil {
			fmt.Fprintln(os.Stderr, "vee-vz-helper: restore:", err)
			return 1
		}
		return 0
	}
	if err := runVM(*vmDir); err != nil {
		fmt.Fprintln(os.Stderr, "vee-vz-helper:", err)
		// Record the failure so vee's stale-VM cleanup can surface it.
		_ = vzhelper.WriteResult(*vmDir, &vzhelper.Result{Error: err.Error()})
		return 1
	}
	return 0
}

func runVM(vmDir string) error {
	spec, err := vzhelper.LoadSpec(vmDir)
	if err != nil {
		return err
	}

	config, err := buildConfig(spec)
	if err != nil {
		return err
	}
	if ok, err := config.Validate(); !ok || err != nil {
		return fmt.Errorf("virtual machine configuration rejected: %w", err)
	}

	machine, err := vz.NewVirtualMachine(config)
	if err != nil {
		return fmt.Errorf("create virtual machine: %w", err)
	}

	// Stale artifacts from a previous run must not be mistaken for this
	// one's.
	sockPath := vzhelper.ControlSocketPath(vmDir)
	_ = os.Remove(sockPath)
	_ = os.Remove(vzhelper.ResultPath(vmDir))
	if stale, err := filepath.Glob(filepath.Join(vmDir, vzhelper.VsockBridgeGlob)); err == nil {
		for _, p := range stale {
			_ = os.Remove(p)
		}
	}

	// State observation must be registered before Start so no transition is
	// missed (the channel is buffered by the bindings).
	stateCh := machine.StateChangedNotify()

	// Recovery (issue #134): boot a macOS guest into recoveryOS. A start
	// option, not a config field — it applies to this start only. LoadSpec
	// validated it as macOS-only.
	var startOpts []vz.VirtualMachineStartOption
	if spec.Recovery {
		startOpts = append(startOpts, vz.WithStartUpFromMacOSRecovery(true))
	}

	if err := startWithLockRetry(machine, startOpts); err != nil {
		return err
	}
	mode := ""
	if spec.Recovery {
		mode = ", recoveryOS"
	}
	fmt.Printf("vee-vz-helper: %s started (%d cpus, %d bytes memory%s)\n", spec.Name, spec.CPUs, spec.MemoryBytes, mode)

	// The control socket is bound only AFTER a successful Start: its
	// appearance is vee's start-confirmation gate, so it must never exist
	// for a VM that failed to start.
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", sockPath)
	if err != nil {
		_ = machine.Stop()
		return fmt.Errorf("listen on control socket: %w", err)
	}
	defer func() {
		_ = ln.Close()
		_ = os.Remove(sockPath)
	}()

	// The socket device only exists on the running machine when the spec
	// asked for it; a nil device makes the vsock ops answer with a clear
	// error instead of a crash.
	var dev guestVsock
	if spec.Vsock {
		if devices := machine.SocketDevices(); len(devices) > 0 {
			dev = &vzSocketDevice{devices[0]}
		}
	}
	vsock := newVsockState(vmDir, dev)
	defer vsock.closeAll()

	// state shared with control connections: stopRequested distinguishes
	// host-requested stops from guest-initiated shutdowns; outcome carries
	// the terminal reason ("guest"|"host"|"error") and must be stored before
	// shutdownDone is closed; activeWaiters counts wait-shutdown responders
	// still writing so the process does not exit under them.
	cs := &controlState{
		machine:      machine,
		vsock:        vsock,
		platform:     spec.PlatformName(),
		display:      newDisplayGate(),
		shutdownDone: make(chan struct{}),
	}

	go serveControl(ln, cs)

	finish := func(reason string, res *vzhelper.Result) {
		_ = vzhelper.WriteResult(vmDir, res)
		cs.outcome.Store(reason)
		close(cs.shutdownDone)
		// Give in-flight wait-shutdown responders a bounded window to flush
		// their response before the process exits.
		deadline := time.Now().Add(2 * time.Second)
		for cs.activeWaiters.Load() > 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
	}

	// State watching runs off the main goroutine so the main thread stays
	// free to host the native display window (issue #139): presenting it runs
	// an AppKit event loop, which only functions on the main thread (locked
	// by init).
	watchDone := make(chan struct{})
	var watchErr error
	go func() {
		defer close(watchDone)
		watchErr = watchStates(stateCh, cs, finish)
	}()

	title := spec.Name
	if spec.Recovery {
		title += " (recoveryOS)"
	}
	for {
		select {
		case <-watchDone:
			return watchErr
		case <-cs.display.requests():
			// A request can race the VM stopping; a window presented over a
			// stopped machine would never be told to close.
			select {
			case <-watchDone:
				return watchErr
			default:
			}
			// Blocks the main thread in the window's event loop until the
			// user closes the window or the VM stops. Closing the window
			// leaves the VM running; the toolbar VM controller stays off so
			// the window cannot pause or force-stop a guest vee believes it
			// manages.
			if err := machine.StartGraphicApplication(
				float64(spec.Display.WidthPx), float64(spec.Display.HeightPx),
				vz.WithWindowTitle(title),
			); err != nil {
				fmt.Fprintln(os.Stderr, "vee-vz-helper: display window:", err)
			}
			cs.display.windowClosed()
		}
	}
}

// watchStates consumes VM state transitions until the machine reaches a
// terminal state, records the outcome via finish, and returns the terminal
// error (nil for a clean stop).
func watchStates(stateCh <-chan vz.VirtualMachineState, cs *controlState, finish func(string, *vzhelper.Result)) error {
	for state := range stateCh {
		switch state {
		case vz.VirtualMachineStateStopped:
			if cs.stopRequested.Load() {
				finish(vzhelper.ReasonHost, &vzhelper.Result{StopRequested: true})
				fmt.Println("vee-vz-helper: stopped on host request")
			} else {
				finish(vzhelper.ReasonGuest, &vzhelper.Result{StopRequested: false})
				fmt.Println("vee-vz-helper: guest initiated shutdown")
			}
			return nil
		case vz.VirtualMachineStateError:
			err := fmt.Errorf("virtual machine entered error state")
			finish(vzhelper.ReasonError, &vzhelper.Result{StopRequested: cs.stopRequested.Load(), Error: err.Error()})
			return err
		default:
			// Transitional states (starting, stopping, …) need no action.
		}
	}
	return nil
}

// vzSocketDevice adapts the framework's socket device (whose methods return
// concrete types) to the cgo-free guestVsock interface vsockState uses.
type vzSocketDevice struct {
	dev *vz.VirtioSocketDevice
}

func (d *vzSocketDevice) Connect(port uint32) (net.Conn, error)    { return d.dev.Connect(port) }
func (d *vzSocketDevice) Listen(port uint32) (net.Listener, error) { return d.dev.Listen(port) }

// startWithLockRetry starts the VM (with the given start options, e.g.
// recoveryOS), retrying while the auxiliary storage is still locked.
// Virtualization.framework releases that lock asynchronously when a previous
// VM object is torn down, so the first start after a restore routinely
// arrives a moment too early and fails with EAGAIN.
func startWithLockRetry(machine *vz.VirtualMachine, opts []vz.VirtualMachineStartOption) error {
	const (
		attempts = 6
		backoff  = 2 * time.Second
	)
	var err error
	for attempt := range attempts {
		if err = machine.Start(opts...); err == nil {
			return nil
		}
		if !strings.Contains(err.Error(), "lock auxiliary storage") {
			return fmt.Errorf("start virtual machine: %w", err)
		}
		if attempt < attempts-1 {
			fmt.Printf("vee-vz-helper: auxiliary storage still locked by a previous VM, retrying in %s\n", backoff)
			time.Sleep(backoff)
		}
	}
	return fmt.Errorf("start virtual machine: auxiliary storage stayed locked after %d attempts: %w", attempts, err)
}

// buildConfig translates the machine spec into a Virtualization.framework
// configuration, dispatching on the guest platform.
func buildConfig(spec *vzhelper.MachineSpec) (*vz.VirtualMachineConfiguration, error) {
	switch spec.PlatformName() {
	case vzhelper.PlatformLinux:
		return buildLinuxConfig(spec)
	default:
		// LoadSpec validated the platform; anything not linux is macOS.
		return buildMacConfig(spec)
	}
}

// buildMacConfig assembles a macOS guest: mac boot loader, the restored
// platform artifacts, and the mandatory graphics + input devices.
func buildMacConfig(spec *vzhelper.MachineSpec) (*vz.VirtualMachineConfiguration, error) {
	bootLoader, err := vz.NewMacOSBootLoader()
	if err != nil {
		return nil, fmt.Errorf("boot loader: %w", err)
	}
	config, err := vz.NewVirtualMachineConfiguration(bootLoader, spec.CPUs, spec.MemoryBytes)
	if err != nil {
		return nil, fmt.Errorf("machine configuration: %w", err)
	}

	// Platform: the hardware model + machine identifier blobs and auxiliary
	// storage bind this VM to its restored installation.
	hardwareModel, err := vz.NewMacHardwareModelWithData(spec.HardwareModel)
	if err != nil {
		return nil, fmt.Errorf("hardware model blob: %w", err)
	}
	if !hardwareModel.Supported() {
		return nil, fmt.Errorf("hardware model is not supported on this host (the guest was restored on/for a different macOS version or machine class)")
	}
	machineID, err := vz.NewMacMachineIdentifierWithData(spec.MachineIdentifier)
	if err != nil {
		return nil, fmt.Errorf("machine identifier blob: %w", err)
	}
	aux, err := vz.NewMacAuxiliaryStorage(spec.AuxiliaryStorage)
	if err != nil {
		return nil, fmt.Errorf("auxiliary storage: %w", err)
	}
	platformConfig, err := vz.NewMacPlatformConfiguration(
		vz.WithMacHardwareModel(hardwareModel),
		vz.WithMacMachineIdentifier(machineID),
		vz.WithMacAuxiliaryStorage(aux),
	)
	if err != nil {
		return nil, fmt.Errorf("platform configuration: %w", err)
	}
	config.SetPlatformVirtualMachineConfiguration(platformConfig)

	// A macOS guest must always carry a graphics device — even headless —
	// or it hangs in the boot loader.
	graphics, err := vz.NewMacGraphicsDeviceConfiguration()
	if err != nil {
		return nil, fmt.Errorf("graphics device: %w", err)
	}
	display, err := vz.NewMacGraphicsDisplayConfiguration(spec.Display.WidthPx, spec.Display.HeightPx, spec.Display.PPI)
	if err != nil {
		return nil, fmt.Errorf("graphics display: %w", err)
	}
	graphics.SetDisplays(display)
	config.SetGraphicsDevicesVirtualMachineConfiguration([]vz.GraphicsDeviceConfiguration{graphics})

	if err := attachCommonDevices(config, spec); err != nil {
		return nil, err
	}

	// Input devices so a future GUI/Screen Sharing session is usable.
	keyboard, err := vz.NewUSBKeyboardConfiguration()
	if err != nil {
		return nil, fmt.Errorf("keyboard: %w", err)
	}
	config.SetKeyboardsVirtualMachineConfiguration([]vz.KeyboardConfiguration{keyboard})
	pointing, err := vz.NewUSBScreenCoordinatePointingDeviceConfiguration()
	if err != nil {
		return nil, fmt.Errorf("pointing device: %w", err)
	}
	config.SetPointingDevicesVirtualMachineConfiguration([]vz.PointingDeviceConfiguration{pointing})

	return config, nil
}

// buildLinuxConfig assembles a Linux guest (issue #127): booted either
// directly from an external kernel image or via EFI from a whole-disk image,
// on a generic platform, headless, with a virtio console captured to the
// spec's serial log and a virtio entropy device. The disks, NIC and optional
// vsock device are the same attachments macOS guests get.
func buildLinuxConfig(spec *vzhelper.MachineSpec) (*vz.VirtualMachineConfiguration, error) {
	bootLoader, err := linuxBootLoader(spec)
	if err != nil {
		return nil, err
	}
	config, err := vz.NewVirtualMachineConfiguration(bootLoader, spec.CPUs, spec.MemoryBytes)
	if err != nil {
		return nil, fmt.Errorf("machine configuration: %w", err)
	}

	platformConfig, err := vz.NewGenericPlatformConfiguration()
	if err != nil {
		return nil, fmt.Errorf("platform configuration: %w", err)
	}
	config.SetPlatformVirtualMachineConfiguration(platformConfig)

	if err := attachCommonDevices(config, spec); err != nil {
		return nil, err
	}

	// virtio-rng: without an entropy source a Linux guest blocks early boot
	// on the kernel's entropy pool.
	entropy, err := vz.NewVirtioEntropyDeviceConfiguration()
	if err != nil {
		return nil, fmt.Errorf("entropy device: %w", err)
	}
	config.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{entropy})

	// The guest console is the only boot diagnostic a headless Linux guest
	// has; capture it to the serial log (truncated per boot, like QEMU's).
	if spec.SerialLog != "" {
		serial, err := vz.NewFileSerialPortAttachment(spec.SerialLog, false)
		if err != nil {
			return nil, fmt.Errorf("serial log %s: %w", spec.SerialLog, err)
		}
		console, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(serial)
		if err != nil {
			return nil, fmt.Errorf("console device: %w", err)
		}
		config.SetSerialPortsVirtualMachineConfiguration([]*vz.VirtioConsoleDeviceSerialPortConfiguration{console})
	}

	return config, nil
}

// linuxBootLoader selects a Linux guest's boot method (issue #129): direct
// kernel boot via VZLinuxBootLoader when the spec names a kernel image (with
// its command line and optional initrd — the -kernel/-append/-initrd analog),
// otherwise EFI boot from the disk's own bootloader. LoadSpec validated the
// two as mutually exclusive.
func linuxBootLoader(spec *vzhelper.MachineSpec) (vz.BootLoader, error) {
	if spec.Kernel != "" {
		var opts []vz.LinuxBootLoaderOption
		if spec.Cmdline != "" {
			opts = append(opts, vz.WithCommandLine(spec.Cmdline))
		}
		if spec.Initrd != "" {
			opts = append(opts, vz.WithInitrd(spec.Initrd))
		}
		bootLoader, err := vz.NewLinuxBootLoader(spec.Kernel, opts...)
		if err != nil {
			return nil, fmt.Errorf("linux boot loader (kernel %s): %w", spec.Kernel, err)
		}
		return bootLoader, nil
	}

	// The variable store (NVRAM) persists the guest's EFI boot entries across
	// boots. It is created on the guest's first boot and reused after that.
	var store *vz.EFIVariableStore
	var err error
	if _, statErr := os.Stat(spec.EFIVariableStore); statErr == nil {
		store, err = vz.NewEFIVariableStore(spec.EFIVariableStore)
	} else {
		store, err = vz.NewEFIVariableStore(spec.EFIVariableStore, vz.WithCreatingEFIVariableStore())
	}
	if err != nil {
		return nil, fmt.Errorf("EFI variable store %s: %w", spec.EFIVariableStore, err)
	}
	bootLoader, err := vz.NewEFIBootLoader(vz.WithEFIVariableStore(store))
	if err != nil {
		return nil, fmt.Errorf("EFI boot loader: %w", err)
	}
	return bootLoader, nil
}

// attachCommonDevices wires the platform-independent devices: virtio-blk
// disks, the NAT virtio-net NIC with the spec's MAC, and the optional
// virtio-vsock socket device.
func attachCommonDevices(config *vz.VirtualMachineConfiguration, spec *vzhelper.MachineSpec) error {
	storage := make([]vz.StorageDeviceConfiguration, 0, len(spec.Disks))
	for _, d := range spec.Disks {
		attachment, err := vz.NewDiskImageStorageDeviceAttachment(d.Path, d.ReadOnly)
		if err != nil {
			return fmt.Errorf("disk %s: %w", d.Path, err)
		}
		blk, err := vz.NewVirtioBlockDeviceConfiguration(attachment)
		if err != nil {
			return fmt.Errorf("disk %s: %w", d.Path, err)
		}
		storage = append(storage, blk)
	}
	config.SetStorageDevicesVirtualMachineConfiguration(storage)

	nat, err := vz.NewNATNetworkDeviceAttachment()
	if err != nil {
		return fmt.Errorf("NAT attachment: %w", err)
	}
	nic, err := vz.NewVirtioNetworkDeviceConfiguration(nat)
	if err != nil {
		return fmt.Errorf("network device: %w", err)
	}
	hwAddr, err := net.ParseMAC(spec.MAC)
	if err != nil {
		return fmt.Errorf("parse mac %q: %w", spec.MAC, err)
	}
	mac, err := vz.NewMACAddress(hwAddr)
	if err != nil {
		return fmt.Errorf("mac address: %w", err)
	}
	nic.SetMACAddress(mac)
	config.SetNetworkDevicesVirtualMachineConfiguration([]*vz.VirtioNetworkDeviceConfiguration{nic})

	// Optional virtio-vsock device: a private host↔guest channel that needs
	// no NAT networking, driven via the vsock control ops (issue #119).
	if spec.Vsock {
		vsockDev, err := vz.NewVirtioSocketDeviceConfiguration()
		if err != nil {
			return fmt.Errorf("vsock device: %w", err)
		}
		config.SetSocketDevicesVirtualMachineConfiguration([]vz.SocketDeviceConfiguration{vsockDev})
	}
	return nil
}

// controlState bundles everything control connections act on: the machine,
// the vsock bridges, and the shutdown bookkeeping runVM shares with them.
type controlState struct {
	machine *vz.VirtualMachine
	vsock   *vsockState
	// platform is the guest platform from the machine spec: the display op
	// is macOS-only (Linux guests carry no graphics device).
	platform string
	// display serializes native-display-window requests toward the main
	// goroutine (issue #139).
	display *displayGate
	// stopRequested distinguishes host-requested stops from guest-initiated
	// shutdowns.
	stopRequested atomic.Bool
	// outcome carries the terminal reason ("guest"|"host"|"error") and must
	// be stored before shutdownDone is closed.
	outcome      atomic.Value
	shutdownDone chan struct{}
	// activeWaiters counts wait-shutdown responders still writing so the
	// process does not exit under them.
	activeWaiters atomic.Int64
}

// serveControl accepts control-socket connections for the VM's lifetime.
func serveControl(ln net.Listener, cs *controlState) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return // listener closed on shutdown
		}
		go handleConn(conn, cs)
	}
}

// handleConn processes newline-delimited JSON requests on one connection.
func handleConn(conn net.Conn, cs *controlState) {
	defer func() { _ = conn.Close() }()
	dec := json.NewDecoder(conn)
	enc := json.NewEncoder(conn)
	for {
		var req vzhelper.Request
		if err := dec.Decode(&req); err != nil {
			return // client hung up (or sent garbage — nothing to answer)
		}
		var resp vzhelper.Response
		switch req.Op {
		case vzhelper.OpStatus:
			resp = vzhelper.Response{OK: true, State: stateString(cs.machine.State())}
		case vzhelper.OpStop:
			cs.stopRequested.Store(true)
			if ok, err := cs.machine.RequestStop(); err != nil || !ok {
				if err == nil {
					err = errors.New("guest did not acknowledge the stop request")
				}
				resp = vzhelper.Response{Error: err.Error()}
			} else {
				resp = vzhelper.Response{OK: true}
			}
		case vzhelper.OpWaitShutdown:
			// One-shot: respond with the terminal reason, then close the
			// connection. The waiter registration keeps runVM's exit drain
			// from cutting the response short.
			cs.activeWaiters.Add(1)
			<-cs.shutdownDone
			reason, _ := cs.outcome.Load().(string)
			resp = vzhelper.Response{OK: true, Reason: reason, Guest: reason == vzhelper.ReasonGuest}
			_ = enc.Encode(&resp)
			cs.activeWaiters.Add(-1)
			return
		case vzhelper.OpVersion:
			v, _, _ := buildinfo.Resolve(version, commit, date)
			resp = vzhelper.Response{OK: true, Protocol: vzhelper.ProtocolVersion, Version: v}
		case vzhelper.OpVsockConnect:
			if path, err := cs.vsock.connectBridge(req.Port); err != nil {
				resp = vzhelper.Response{Error: err.Error()}
			} else {
				resp = vzhelper.Response{OK: true, Path: path}
			}
		case vzhelper.OpVsockListen:
			if err := cs.vsock.listenForward(req.Port, req.Path); err != nil {
				resp = vzhelper.Response{Error: err.Error()}
			} else {
				resp = vzhelper.Response{OK: true}
			}
		case vzhelper.OpShowDisplay:
			// OK means "the window is being presented", not "it is visible":
			// the main goroutine blocks in the window's event loop for its
			// whole lifetime, so there is no later moment to answer from.
			if cs.platform != vzhelper.PlatformMacOS {
				resp = vzhelper.Response{Error: "this guest runs headless (no graphics device) — the native display window is available for macOS guests only"}
			} else if err := cs.display.request(); err != nil {
				resp = vzhelper.Response{Error: err.Error()}
			} else {
				resp = vzhelper.Response{OK: true}
			}
		default:
			resp = vzhelper.Response{Error: fmt.Sprintf("unknown op %q", req.Op)}
		}
		if err := enc.Encode(&resp); err != nil {
			return
		}
	}
}

// stateString maps Virtualization.framework states onto the strings the
// control protocol reports.
func stateString(s vz.VirtualMachineState) string {
	switch s {
	case vz.VirtualMachineStateRunning:
		return "running"
	case vz.VirtualMachineStateStopped:
		return "stopped"
	case vz.VirtualMachineStateStarting:
		return "starting"
	case vz.VirtualMachineStateStopping:
		return "stopping"
	case vz.VirtualMachineStatePaused, vz.VirtualMachineStatePausing:
		return "paused"
	case vz.VirtualMachineStateError:
		return "error"
	default:
		return fmt.Sprintf("state-%d", int(s))
	}
}
