//go:build darwin && arm64 && cgo

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Code-Hex/vz/v3"

	"github.com/Benehiko/vee/internal/vzhelper"
)

// runRestore installs macOS from an IPSW onto a raw disk image via
// VZMacOSInstaller and records the resulting platform artifacts (hardware
// model, machine identifier, minimums) in <vm-dir>/vz-restore.json for vee
// to persist into the VM config. The disk must already exist (a sparse file
// is fine); the auxiliary storage is created here, bound to the hardware
// model the restore image requires.
func runRestore(vmDir, ipswPath, diskPath, auxPath string, cpus uint, memoryBytes uint64) error {
	if diskPath == "" || auxPath == "" {
		return fmt.Errorf("--restore requires --restore-disk and --restore-aux")
	}
	if _, err := os.Stat(diskPath); err != nil {
		return fmt.Errorf("restore disk: %w", err)
	}

	img, err := vz.LoadMacOSRestoreImageFromPath(ipswPath)
	if err != nil {
		return fmt.Errorf("load restore image %s: %w", ipswPath, err)
	}
	req := img.MostFeaturefulSupportedConfiguration()
	if req == nil || req.HardwareModel() == nil || !req.HardwareModel().Supported() {
		return fmt.Errorf("this host cannot restore %s (macOS %s): no supported hardware model — the image may be newer than the host macOS", ipswPath, img.OperatingSystemVersion())
	}
	hardwareModel := req.HardwareModel()
	if min := req.MinimumSupportedCPUCount(); uint64(cpus) < min {
		cpus = uint(min)
	}
	if min := req.MinimumSupportedMemorySize(); memoryBytes < min {
		memoryBytes = min
	}

	machineID, err := vz.NewMacMachineIdentifier()
	if err != nil {
		return fmt.Errorf("machine identifier: %w", err)
	}
	// A fresh restore must not inherit a stale aux from a previous attempt.
	_ = os.Remove(auxPath)
	aux, err := vz.NewMacAuxiliaryStorage(auxPath, vz.WithCreatingMacAuxiliaryStorage(hardwareModel))
	if err != nil {
		return fmt.Errorf("create auxiliary storage: %w", err)
	}

	config, err := restoreConfig(hardwareModel, machineID, aux, diskPath, cpus, memoryBytes)
	if err != nil {
		return err
	}
	if ok, err := config.Validate(); !ok || err != nil {
		return fmt.Errorf("restore configuration rejected: %w", err)
	}
	machine, err := vz.NewVirtualMachine(config)
	if err != nil {
		return fmt.Errorf("create virtual machine: %w", err)
	}

	installer, err := vz.NewMacOSInstaller(machine, ipswPath)
	if err != nil {
		return fmt.Errorf("create installer: %w", err)
	}

	fmt.Printf("vee-vz-helper: restoring macOS %s (build %s) — this takes a while\n",
		img.OperatingSystemVersion(), img.BuildVersion())
	progressDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		last := -1
		for {
			select {
			case <-progressDone:
				return
			case <-ticker.C:
				pct := int(installer.FractionCompleted() * 100)
				if pct != last {
					fmt.Printf("vee-vz-helper: restore progress %d%%\n", pct)
					last = pct
				}
			}
		}
	}()
	// SIGINT/SIGTERM cancel the installer cleanly instead of leaving an
	// orphaned restore writing to the disk image.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err = installer.Install(ctx)
	stop()
	close(progressDone)
	if err != nil {
		return fmt.Errorf("install: %w", err)
	}

	if err := shutDownRestoreVM(machine); err != nil {
		fmt.Fprintln(os.Stderr, "vee-vz-helper: warning:", err)
	}

	osv := img.OperatingSystemVersion()
	if err := vzhelper.WriteRestoreResult(vmDir, &vzhelper.RestoreResult{
		HardwareModel:     hardwareModel.DataRepresentation(),
		MachineIdentifier: machineID.DataRepresentation(),
		MinCPUs:           req.MinimumSupportedCPUCount(),
		MinMemoryBytes:    req.MinimumSupportedMemorySize(),
		OSVersion:         osv.String(),
		Build:             img.BuildVersion(),
	}); err != nil {
		return fmt.Errorf("write restore result: %w", err)
	}
	fmt.Println("vee-vz-helper: restore complete")
	return nil
}

// shutDownRestoreVM stops the VM the installer used and waits for the
// framework to finish tearing it down. Without this the caller's next start
// races that teardown: the auxiliary storage stays locked, and a start that
// gets past the lock lands in an error state.
func shutDownRestoreVM(machine *vz.VirtualMachine) error {
	if machine.State() == vz.VirtualMachineStateStopped {
		return nil
	}
	if machine.CanStop() {
		if err := machine.Stop(); err != nil {
			return fmt.Errorf("stop the restore VM: %w", err)
		}
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if machine.State() == vz.VirtualMachineStateStopped {
			// The lock is released a moment after the state settles.
			time.Sleep(2 * time.Second)
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("the restore VM did not stop within 30s (state %v); the next start may need a retry", machine.State())
}

// restoreConfig builds the VM configuration the installer runs against —
// the same shape as a normal boot (mandatory graphics device included), with
// the platform assembled from the restore image's requirements.
func restoreConfig(hardwareModel *vz.MacHardwareModel, machineID *vz.MacMachineIdentifier, aux *vz.MacAuxiliaryStorage, diskPath string, cpus uint, memoryBytes uint64) (*vz.VirtualMachineConfiguration, error) {
	bootLoader, err := vz.NewMacOSBootLoader()
	if err != nil {
		return nil, fmt.Errorf("boot loader: %w", err)
	}
	config, err := vz.NewVirtualMachineConfiguration(bootLoader, cpus, memoryBytes)
	if err != nil {
		return nil, fmt.Errorf("machine configuration: %w", err)
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

	graphics, err := vz.NewMacGraphicsDeviceConfiguration()
	if err != nil {
		return nil, fmt.Errorf("graphics device: %w", err)
	}
	display, err := vz.NewMacGraphicsDisplayConfiguration(
		vzhelper.DefaultDisplay.WidthPx, vzhelper.DefaultDisplay.HeightPx, vzhelper.DefaultDisplay.PPI)
	if err != nil {
		return nil, fmt.Errorf("graphics display: %w", err)
	}
	graphics.SetDisplays(display)
	config.SetGraphicsDevicesVirtualMachineConfiguration([]vz.GraphicsDeviceConfiguration{graphics})

	attachment, err := vz.NewDiskImageStorageDeviceAttachment(diskPath, false)
	if err != nil {
		return nil, fmt.Errorf("disk %s: %w", diskPath, err)
	}
	blk, err := vz.NewVirtioBlockDeviceConfiguration(attachment)
	if err != nil {
		return nil, fmt.Errorf("disk %s: %w", diskPath, err)
	}
	config.SetStorageDevicesVirtualMachineConfiguration([]vz.StorageDeviceConfiguration{blk})

	nat, err := vz.NewNATNetworkDeviceAttachment()
	if err != nil {
		return nil, fmt.Errorf("NAT attachment: %w", err)
	}
	nic, err := vz.NewVirtioNetworkDeviceConfiguration(nat)
	if err != nil {
		return nil, fmt.Errorf("network device: %w", err)
	}
	config.SetNetworkDevicesVirtualMachineConfiguration([]*vz.VirtioNetworkDeviceConfiguration{nic})

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
