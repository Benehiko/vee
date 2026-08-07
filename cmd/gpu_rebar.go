package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Benehiko/vee/internal/gpu"
)

// Boot-time Resizable BAR management. A BAR cannot be resized while a driver
// is bound, and some GPUs (e.g. RDNA3) need a cold boot after any runtime
// unbind — so the resize happens in an initramfs early hook, after PCI
// enumeration and before the MODULES list (vfio_pci, amdgpu) loads. Only
// mkinitcpio hosts are supported for now.

const (
	rebarHookPath    = "/etc/initcpio/hooks/vee-rebar"
	rebarInstallPath = "/etc/initcpio/install/vee-rebar"
	mkinitcpioConf   = "/etc/mkinitcpio.conf"
)

// rebarHookScript runs in early userspace. init_functions provides err().
// It reads the conf embedded by the install script and applies each resize.
const rebarHookScript = `#!/usr/bin/ash
# Written by ` + "`vee gpu rebar`" + `. Resizes GPU BARs in early userspace —
# after PCI enumeration, before any driver (vfio_pci from MODULES, amdgpu)
# can bind. A BAR cannot be resized once a driver is bound, and some cards
# cannot survive a runtime unbind/rebind cycle, so boot is the only safe
# point. Managed by vee; edit /etc/vee/rebar.conf via "vee gpu rebar".
run_earlyhook() {
    [ -r /etc/vee/rebar.conf ] || return 0
    while read -r addr bar exp _; do
        case "$addr" in ''|'#'*) continue ;; esac
        f="/sys/bus/pci/devices/$addr/resource${bar}_resize"
        if [ -w "$f" ]; then
            echo "$exp" > "$f" 2>/dev/null || err "vee-rebar: resize $addr BAR$bar failed"
        else
            err "vee-rebar: $f not writable or missing"
        fi
    done < /etc/vee/rebar.conf
}
`

const rebarInstallScript = `#!/bin/bash
# Written by ` + "`vee gpu rebar`" + `.
build() {
    add_runscript
    add_file /etc/vee/rebar.conf
}

help() {
    cat <<HELPEOF
Resizes GPU PCI BARs (Resizable BAR) in early userspace, before any driver
binds, per /etc/vee/rebar.conf. Needed for VFIO passthrough GPUs: the native
driver never probes them, so their BARs keep the firmware-default size, and
guests (e.g. compute stacks that map all VRAM CPU-visible) are stuck with it.
Managed by "vee gpu rebar".
HELPEOF
}
`

var (
	rebarSize   string
	rebarBar    int
	rebarRemove bool
)

var gpuRebarCmd = &cobra.Command{
	Use:   "rebar <pci-addr>",
	Short: "Manage boot-time Resizable BAR for a passthrough GPU (requires sudo)",
	Long: `Show, install, or remove a boot-time BAR resize for a PCI device.

Without flags, shows the BAR's current and supported sizes. With --size, an
initramfs early hook is installed that resizes the BAR on every boot before
any driver binds — the only safe point for cards that cannot survive a
runtime unbind/rebind (e.g. RDNA3 GPUs, which need a cold boot afterwards).

Why: a VFIO-bound GPU keeps its firmware-default BAR (typically 256M) since
the native driver never probes it, and the guest is stuck with that size.
Compute stacks that map all VRAM CPU-visible (e.g. tinygrad's AMD backend)
need BAR0 to cover the whole VRAM.

Currently supports mkinitcpio-based hosts (Arch). Changes take effect after
the next boot; use a cold boot for GPUs with runtime-reset quirks.`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completePCIAddresses,
	RunE: func(cmd *cobra.Command, args []string) error {
		addr := args[0]
		switch {
		case rebarRemove:
			return rebarUninstall(cmd.Context(), addr, rebarBar)
		case rebarSize != "":
			return rebarInstall(cmd.Context(), addr, rebarBar, rebarSize)
		default:
			return rebarShow(addr, rebarBar)
		}
	},
}

func rebarShow(addr string, bar int) error {
	cur, err := gpu.BARSizeBytes(addr, bar)
	if err != nil {
		return err
	}
	mask, err := gpu.RebarSupported(addr, bar)
	if err != nil {
		return err
	}
	fmt.Printf("BAR%d current size: %s\n", bar, gpu.FormatBytes(cur))
	fmt.Printf("BAR%d supported sizes: %s\n", bar, gpu.FormatRebarSizes(mask))
	entries, _ := readRebarConf()
	for _, e := range entries {
		if e.Bar == bar && strings.HasSuffix(e.Addr, strings.TrimPrefix(addr, "0000:")) {
			fmt.Printf("Boot-time resize installed: %s (reboot to apply if not active)\n",
				gpu.FormatBytes(e.SizeBytes()))
			return nil
		}
	}
	fmt.Printf("No boot-time resize installed — run: vee gpu rebar %s --size <size>\n", addr)
	return nil
}

func rebarInstall(ctx context.Context, addr string, bar int, size string) error {
	exp, err := gpu.ParseRebarSize(size)
	if err != nil {
		return err
	}
	mask, err := gpu.RebarSupported(addr, bar)
	if err != nil {
		return err
	}
	//nolint:gosec // exp comes from ParseRebarSize, bounded well below 64
	if mask&(1<<uint(exp)) == 0 {
		return fmt.Errorf("size %s not supported by BAR%d of %s — supported: %s",
			size, bar, addr, gpu.FormatRebarSizes(mask))
	}
	if _, err := exec.LookPath("mkinitcpio"); err != nil {
		return fmt.Errorf("vee gpu rebar currently requires mkinitcpio (Arch) — " +
			"on other initramfs systems, resize the BAR from an early-boot script before vfio-pci binds")
	}

	entries, err := readRebarConf()
	if err != nil {
		return err
	}
	entries = gpu.MergeRebarEntry(entries, gpu.RebarEntry{
		Addr: addr, Bar: bar, Exp: exp,
	})

	if out, err := exec.CommandContext(ctx, "sudo", "mkdir", "-p", "/etc/vee").CombinedOutput(); err != nil {
		return fmt.Errorf("create /etc/vee: %w\n%s", err, out)
	}
	for path, content := range map[string]string{
		gpu.RebarConfPath: gpu.RenderRebarConf(entries),
		rebarHookPath:     rebarHookScript,
		rebarInstallPath:  rebarInstallScript,
	} {
		if err := sudoWrite(path, content); err != nil {
			return err
		}
		fmt.Println("Written:", path)
	}
	if err := ensureMkinitcpioHook(true); err != nil {
		return err
	}
	if err := regenInitramfs(ctx); err != nil {
		return err
	}
	fmt.Printf("Boot-time resize installed: %s BAR%d -> %s. Reboot to apply "+
		"(cold boot for GPUs with reset quirks).\n", addr, bar, gpu.FormatBytes(gpu.RebarEntry{Exp: exp}.SizeBytes()))
	return nil
}

func rebarUninstall(ctx context.Context, addr string, bar int) error {
	entries, err := readRebarConf()
	if err != nil {
		return err
	}
	entries, found := gpu.RemoveRebarEntry(entries, addr, bar)
	if !found {
		return fmt.Errorf("no boot-time resize installed for %s BAR%d", addr, bar)
	}
	if len(entries) > 0 {
		if err := sudoWrite(gpu.RebarConfPath, gpu.RenderRebarConf(entries)); err != nil {
			return err
		}
	} else {
		//nolint:gosec // fixed argument list of vee-owned constant paths
		if out, err := exec.CommandContext(ctx, "sudo", "rm", "-f",
			gpu.RebarConfPath, rebarHookPath, rebarInstallPath).CombinedOutput(); err != nil {
			return fmt.Errorf("remove rebar files: %w\n%s", err, out)
		}
		if err := ensureMkinitcpioHook(false); err != nil {
			return err
		}
	}
	if err := regenInitramfs(ctx); err != nil {
		return err
	}
	fmt.Printf("Boot-time resize removed for %s BAR%d. Reboot to restore the firmware-default size.\n", addr, bar)
	return nil
}

func readRebarConf() ([]gpu.RebarEntry, error) {
	raw, err := os.ReadFile(gpu.RebarConfPath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return gpu.ParseRebarConf(string(raw))
}

// hooksLineRe matches the (uncommented) HOOKS array line of mkinitcpio.conf.
var hooksLineRe = regexp.MustCompile(`(?m)^HOOKS=\((.*)\)`)

// ensureMkinitcpioHook adds or removes vee-rebar in the mkinitcpio HOOKS
// array. Early hooks all run before MODULES load, so position within the
// array does not matter for correctness; it is inserted after udev by
// convention.
func ensureMkinitcpioHook(want bool) error {
	raw, err := os.ReadFile(mkinitcpioConf)
	if err != nil {
		return fmt.Errorf("read %s: %w", mkinitcpioConf, err)
	}
	m := hooksLineRe.FindStringSubmatch(string(raw))
	if m == nil {
		return fmt.Errorf("no HOOKS=(...) line found in %s", mkinitcpioConf)
	}
	hooks := strings.Fields(m[1])
	has := false
	for _, h := range hooks {
		if h == "vee-rebar" {
			has = true
			break
		}
	}
	if has == want {
		return nil
	}
	var next []string
	if want {
		inserted := false
		for _, h := range hooks {
			next = append(next, h)
			if h == "udev" {
				next = append(next, "vee-rebar")
				inserted = true
			}
		}
		if !inserted {
			next = append([]string{"vee-rebar"}, next...)
		}
	} else {
		for _, h := range hooks {
			if h != "vee-rebar" {
				next = append(next, h)
			}
		}
	}
	updated := hooksLineRe.ReplaceAllString(string(raw), "HOOKS=("+strings.Join(next, " ")+")")
	if err := sudoWrite(mkinitcpioConf, updated); err != nil {
		return err
	}
	fmt.Printf("Updated HOOKS in %s\n", mkinitcpioConf)
	return nil
}

func regenInitramfs(ctx context.Context) error {
	fmt.Println("Regenerating initramfs (mkinitcpio -P)...")
	//nolint:gosec // fixed argument list; interactive install step.
	out, err := exec.CommandContext(ctx, "sudo", "mkinitcpio", "-P").CombinedOutput()
	if err != nil {
		return fmt.Errorf("mkinitcpio -P: %w\n%s", err, out)
	}
	return nil
}

// sudoWrite writes content to a root-owned path via sudo tee.
func sudoWrite(path, content string) error {
	writeCmd, err := sudoWriteCmd(path, content)
	if err != nil {
		return err
	}
	if out, err := writeCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("write %s: %w\n%s", path, err, out)
	}
	return nil
}

func init() {
	gpuRebarCmd.Flags().StringVar(&rebarSize, "size", "", "Target BAR size (power of two, e.g. 16G) — installs the boot-time resize")
	gpuRebarCmd.Flags().IntVar(&rebarBar, "bar", 0, "BAR index to resize")
	gpuRebarCmd.Flags().BoolVar(&rebarRemove, "remove", false, "Remove the boot-time resize for this device/BAR")
	gpuCmd.AddCommand(gpuRebarCmd)
}
