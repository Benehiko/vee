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
	"sync/atomic"
	"time"

	"github.com/Code-Hex/vz/v3"

	"github.com/Benehiko/vee/internal/vzhelper"
)

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

	// State observation must be registered before Start so no transition is
	// missed (the channel is buffered by the bindings).
	stateCh := machine.StateChangedNotify()

	if err := machine.Start(); err != nil {
		return fmt.Errorf("start virtual machine: %w", err)
	}
	fmt.Printf("vee-vz-helper: %s started (%d cpus, %d bytes memory)\n", spec.Name, spec.CPUs, spec.MemoryBytes)

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

	// state shared with control connections: stopRequested distinguishes
	// host-requested stops from guest-initiated shutdowns; outcome carries
	// the terminal reason ("guest"|"host"|"error") and must be stored before
	// shutdownDone is closed; activeWaiters counts wait-shutdown responders
	// still writing so the process does not exit under them.
	var stopRequested atomic.Bool
	var outcome atomic.Value
	shutdownDone := make(chan struct{})
	var activeWaiters atomic.Int64

	go serveControl(ln, machine, &stopRequested, &outcome, shutdownDone, &activeWaiters)

	finish := func(reason string, res *vzhelper.Result) {
		_ = vzhelper.WriteResult(vmDir, res)
		outcome.Store(reason)
		close(shutdownDone)
		// Give in-flight wait-shutdown responders a bounded window to flush
		// their response before the process exits.
		deadline := time.Now().Add(2 * time.Second)
		for activeWaiters.Load() > 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
	}

	for state := range stateCh {
		switch state {
		case vz.VirtualMachineStateStopped:
			if stopRequested.Load() {
				finish(vzhelper.ReasonHost, &vzhelper.Result{StopRequested: true})
				fmt.Println("vee-vz-helper: stopped on host request")
			} else {
				finish(vzhelper.ReasonGuest, &vzhelper.Result{StopRequested: false})
				fmt.Println("vee-vz-helper: guest initiated shutdown")
			}
			return nil
		case vz.VirtualMachineStateError:
			err := fmt.Errorf("virtual machine entered error state")
			finish(vzhelper.ReasonError, &vzhelper.Result{StopRequested: stopRequested.Load(), Error: err.Error()})
			return err
		default:
			// Transitional states (starting, stopping, …) need no action.
		}
	}
	return nil
}

// buildConfig translates the machine spec into a Virtualization.framework
// configuration for a macOS guest.
func buildConfig(spec *vzhelper.MachineSpec) (*vz.VirtualMachineConfiguration, error) {
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

	storage := make([]vz.StorageDeviceConfiguration, 0, len(spec.Disks))
	for _, d := range spec.Disks {
		attachment, err := vz.NewDiskImageStorageDeviceAttachment(d.Path, d.ReadOnly)
		if err != nil {
			return nil, fmt.Errorf("disk %s: %w", d.Path, err)
		}
		blk, err := vz.NewVirtioBlockDeviceConfiguration(attachment)
		if err != nil {
			return nil, fmt.Errorf("disk %s: %w", d.Path, err)
		}
		storage = append(storage, blk)
	}
	config.SetStorageDevicesVirtualMachineConfiguration(storage)

	nat, err := vz.NewNATNetworkDeviceAttachment()
	if err != nil {
		return nil, fmt.Errorf("NAT attachment: %w", err)
	}
	nic, err := vz.NewVirtioNetworkDeviceConfiguration(nat)
	if err != nil {
		return nil, fmt.Errorf("network device: %w", err)
	}
	hwAddr, err := net.ParseMAC(spec.MAC)
	if err != nil {
		return nil, fmt.Errorf("parse mac %q: %w", spec.MAC, err)
	}
	mac, err := vz.NewMACAddress(hwAddr)
	if err != nil {
		return nil, fmt.Errorf("mac address: %w", err)
	}
	nic.SetMACAddress(mac)
	config.SetNetworkDevicesVirtualMachineConfiguration([]*vz.VirtioNetworkDeviceConfiguration{nic})

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

// serveControl accepts control-socket connections for the VM's lifetime.
func serveControl(ln net.Listener, machine *vz.VirtualMachine, stopRequested *atomic.Bool, outcome *atomic.Value, shutdownDone <-chan struct{}, activeWaiters *atomic.Int64) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return // listener closed on shutdown
		}
		go handleConn(conn, machine, stopRequested, outcome, shutdownDone, activeWaiters)
	}
}

// handleConn processes newline-delimited JSON requests on one connection.
func handleConn(conn net.Conn, machine *vz.VirtualMachine, stopRequested *atomic.Bool, outcome *atomic.Value, shutdownDone <-chan struct{}, activeWaiters *atomic.Int64) {
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
			resp = vzhelper.Response{OK: true, State: stateString(machine.State())}
		case vzhelper.OpStop:
			stopRequested.Store(true)
			if ok, err := machine.RequestStop(); err != nil || !ok {
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
			activeWaiters.Add(1)
			<-shutdownDone
			reason, _ := outcome.Load().(string)
			resp = vzhelper.Response{OK: true, Reason: reason, Guest: reason == vzhelper.ReasonGuest}
			_ = enc.Encode(&resp)
			activeWaiters.Add(-1)
			return
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
