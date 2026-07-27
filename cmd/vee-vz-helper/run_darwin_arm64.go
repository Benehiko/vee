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
	"sync"
	"sync/atomic"

	"github.com/Code-Hex/vz/v3"

	"github.com/Benehiko/vee/internal/vzhelper"
)

func run() int {
	vmDir := flag.String("vm-dir", "", "VM directory containing "+vzhelper.SpecFileName)
	flag.Parse()
	if *vmDir == "" {
		fmt.Fprintln(os.Stderr, "usage: vee-vz-helper --vm-dir <dir>")
		return 2
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

	// A previous crashed helper may have left the socket behind.
	sockPath := vzhelper.ControlSocketPath(vmDir)
	_ = os.Remove(sockPath)
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", sockPath)
	if err != nil {
		return fmt.Errorf("listen on control socket: %w", err)
	}
	defer func() {
		_ = ln.Close()
		_ = os.Remove(sockPath)
	}()
	// Stale result from the previous run must not be mistaken for this one's.
	_ = os.Remove(vzhelper.ResultPath(vmDir))

	// State observation must be registered before Start so no transition is
	// missed.
	stateCh := machine.StateChangedNotify()

	if err := machine.Start(); err != nil {
		return fmt.Errorf("start virtual machine: %w", err)
	}
	fmt.Printf("vee-vz-helper: %s started (%d cpus, %d bytes memory)\n", spec.Name, spec.CPUs, spec.MemoryBytes)

	// stopRequested distinguishes host-requested stops (OpStop) from
	// guest-initiated shutdowns; shutdownDone broadcasts the terminal state
	// to OpWaitShutdown waiters.
	var stopRequested atomic.Bool
	shutdownDone := make(chan struct{})
	var shutdownOnce sync.Once

	go serveControl(ln, machine, &stopRequested, shutdownDone)

	for state := range stateCh {
		switch state {
		case vz.VirtualMachineStateStopped:
			guest := !stopRequested.Load()
			_ = vzhelper.WriteResult(vmDir, &vzhelper.Result{StopRequested: !guest})
			shutdownOnce.Do(func() { close(shutdownDone) })
			if guest {
				fmt.Println("vee-vz-helper: guest initiated shutdown")
			} else {
				fmt.Println("vee-vz-helper: stopped on host request")
			}
			return nil
		case vz.VirtualMachineStateError:
			shutdownOnce.Do(func() { close(shutdownDone) })
			return fmt.Errorf("virtual machine entered error state")
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
func serveControl(ln net.Listener, machine *vz.VirtualMachine, stopRequested *atomic.Bool, shutdownDone <-chan struct{}) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return // listener closed on shutdown
		}
		go handleConn(conn, machine, stopRequested, shutdownDone)
	}
}

// handleConn processes newline-delimited JSON requests on one connection.
func handleConn(conn net.Conn, machine *vz.VirtualMachine, stopRequested *atomic.Bool, shutdownDone <-chan struct{}) {
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
			<-shutdownDone
			resp = vzhelper.Response{OK: true, Guest: !stopRequested.Load()}
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
