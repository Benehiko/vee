package templates

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Benehiko/vee/provider"
	"go.uber.org/zap"
)

// oscdimgURL is Microsoft's standalone oscdimg.exe on the public symbol server.
// oscdimg is the only tool that authors WinPE-bootable Windows media correctly
// (genisoimage/xorriso rebuilds reboot-loop WinPE); it runs fine under wine.
const (
	oscdimgURL  = "https://msdl.microsoft.com/download/symbols/oscdimg.exe/688CABB065000/oscdimg.exe"
	oscdimgName = "oscdimg.exe"

	// noPromptEFI is the path, inside standard Windows install media, of the EFI
	// boot image that does NOT print "Press any key to boot from CD".
	noPromptEFI = `efi/microsoft/boot/efisys_noprompt.bin`
)

// findWindowsContainerRuntime returns nerdctl or docker, whichever is on PATH.
func findWindowsContainerRuntime() (string, error) {
	for _, name := range []string{"nerdctl", "docker"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("a container runtime (nerdctl or docker) is required")
}

// ensureNoPromptISO returns a path to Windows install media that boots without
// the "Press any key to boot from CD" prompt. If wine + a container runtime are
// available it rebuilds srcISO with oscdimg and the prompt-free EFI boot image,
// caching the result next to srcISO. If the toolchain is missing it logs a
// warning and returns srcISO unchanged (the install will then need a manual key
// press at the boot prompt).
func ensureNoPromptISO(ctx context.Context, p provider.Provider, srcISO string) (string, error) {
	dst := strings.TrimSuffix(srcISO, filepath.Ext(srcISO)) + "-noprompt.iso"
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	}

	wine, wineErr := exec.LookPath("wine")
	_, udisksErr := exec.LookPath("udisksctl")
	if wineErr != nil || udisksErr != nil {
		p.Logger().Warn("cannot build no-prompt install media (need wine + udisksctl); "+
			"the install will stop at the 'press any key to boot from CD' prompt",
			zap.Bool("wine", wineErr == nil), zap.Bool("udisksctl", udisksErr == nil))
		return srcISO, nil
	}

	oscdimg, err := ensureCachedDownload(ctx, p, oscdimgURL, oscdimgName)
	if err != nil {
		p.Logger().Warn("could not fetch oscdimg; using prompting install media", zap.Error(err))
		return srcISO, nil
	}

	// oscdimg needs to read the source tree. Windows media is UDF, which the
	// container ISO extractors (7z/bsdtar) cannot read — they only see the
	// ISO9660 stub. Loop-mount it read-only via udisksctl (no root needed) and
	// point oscdimg at the mount.
	srcTree, umount, err := loopMountISO(ctx, srcISO)
	if err != nil {
		p.Logger().Warn("could not mount source ISO; using prompting install media", zap.Error(err))
		return srcISO, nil
	}
	defer umount()

	if _, err := os.Stat(filepath.Join(srcTree, noPromptEFI)); err != nil {
		p.Logger().Warn("source media has no efisys_noprompt.bin; using prompting install media")
		return srcISO, nil
	}

	// Run oscdimg under wine, mapping the source mount as S: and the output dir
	// as T:. Use a dedicated wine HOME (not the ISO cache — an overlapping HOME
	// and WINEPREFIX corrupts the prefix) and a minimal environment: no
	// inherited HTTP(S)_PROXY, which hangs wineboot, but keep XDG_RUNTIME_DIR,
	// which wine needs.
	wineHome := filepath.Join(p.Config().ISOCachePath, ".winehome")
	prefix := filepath.Join(wineHome, "prefix")
	xdg := filepath.Join(wineHome, "xdg")
	_ = os.MkdirAll(xdg, 0o700)
	wineEnv := []string{
		"HOME=" + wineHome,
		"PATH=/usr/bin:/bin",
		"WINEPREFIX=" + prefix,
		"WINEDEBUG=-all",
		"WINEDLLOVERRIDES=mscoree=d;mshtml=d",
		"WINEARCH=win64",
		"XDG_RUNTIME_DIR=" + xdg,
		"DISPLAY=",
	}

	// Initialise the prefix once (idempotent; ~a few seconds when already done).
	if _, err := os.Stat(filepath.Join(prefix, "drive_c", "windows")); err != nil {
		boot := exec.CommandContext(ctx, wine, "wineboot", "-u")
		boot.Env = wineEnv
		boot.Stdout = os.Stderr
		boot.Stderr = os.Stderr
		if err := boot.Run(); err != nil {
			p.Logger().Warn("wine prefix init failed; using prompting install media", zap.Error(err))
			return srcISO, nil
		}
	}

	if err := os.MkdirAll(filepath.Join(prefix, "dosdevices"), 0o755); err != nil {
		return "", err
	}
	_ = os.Remove(filepath.Join(prefix, "dosdevices", "s:"))
	_ = os.Remove(filepath.Join(prefix, "dosdevices", "t:"))
	if err := os.Symlink(srcTree, filepath.Join(prefix, "dosdevices", "s:")); err != nil {
		return "", err
	}
	if err := os.Symlink(filepath.Dir(dst), filepath.Join(prefix, "dosdevices", "t:")); err != nil {
		return "", err
	}

	label := isoVolumeLabel(srcTree)
	bootData := `-bootdata:1#pEF,e,bs:\` + filepath.FromSlash(noPromptEFI)
	oscArgs := []string{
		"-m", "-o", "-u2", "-udfver102",
		"-l" + label,
		bootData,
		`s:\`,
		`t:\` + filepath.Base(dst),
	}
	cmd := exec.CommandContext(ctx, wine, append([]string{oscdimg}, oscArgs...)...)
	cmd.Env = wineEnv
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	p.Logger().Info("building no-prompt install media with oscdimg", zap.String("dst", dst))
	if err := cmd.Run(); err != nil {
		p.Logger().Warn("oscdimg failed; using prompting install media", zap.Error(err))
		return srcISO, nil
	}
	if _, err := os.Stat(dst); err != nil {
		p.Logger().Warn("oscdimg produced no output; using prompting install media")
		return srcISO, nil
	}
	return dst, nil
}

// loopMountISO loop-mounts iso read-only via udisksctl (no root needed) and
// returns the mount point plus a cleanup func that unmounts and detaches the
// loop device. UDF Windows media can only be read through a real mount; the
// userspace ISO extractors see only the ISO9660 stub.
func loopMountISO(ctx context.Context, iso string) (string, func(), error) {
	out, err := exec.CommandContext(ctx, "udisksctl", "loop-setup", "-r", "-f", iso).CombinedOutput()
	if err != nil {
		return "", nil, fmt.Errorf("udisksctl loop-setup: %w: %s", err, out)
	}
	// Output: "Mapped file <iso> as /dev/loopN."
	loopDev := ""
	for _, f := range strings.Fields(string(out)) {
		if strings.HasPrefix(f, "/dev/loop") {
			loopDev = strings.TrimRight(f, ".")
			break
		}
	}
	if loopDev == "" {
		return "", nil, fmt.Errorf("could not parse loop device from: %s", out)
	}

	detach := func() {
		_ = exec.Command("udisksctl", "unmount", "-b", loopDev).Run()
		_ = exec.Command("udisksctl", "loop-delete", "-b", loopDev).Run()
	}

	mo, err := exec.CommandContext(ctx, "udisksctl", "mount", "-b", loopDev).CombinedOutput()
	if err != nil {
		detach()
		return "", nil, fmt.Errorf("udisksctl mount: %w: %s", err, mo)
	}
	// Output: "Mounted <dev> at <path>"
	mnt := ""
	if i := strings.LastIndex(string(mo), " at "); i >= 0 {
		mnt = strings.TrimSpace(string(mo)[i+4:])
	}
	if mnt == "" {
		detach()
		return "", nil, fmt.Errorf("could not parse mount point from: %s", mo)
	}
	return mnt, detach, nil
}

// isoVolumeLabel reads the volume label the extracted media should keep. It
// falls back to a generic label; the label only affects cosmetics, not boot.
func isoVolumeLabel(srcTree string) string {
	// The extracted tree has no volume descriptor; use a stable default. Windows
	// setup does not require a specific label.
	_ = srcTree
	return "WIN_INSTALL"
}
