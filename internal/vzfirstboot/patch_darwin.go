//go:build darwin

package vzfirstboot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// attachRetries covers the transient "Resource temporarily unavailable" the
// disk-images driver returns right after the restore released the image.
const (
	attachRetries = 3
	attachBackoff = 3 * time.Second
)

// Patch prepares a restored macOS disk image for headless first boot. It is
// best-effort by contract: on failure the caller keeps a usable VM that boots
// into Setup Assistant, so every error path must leave the image in that
// state rather than half-patched.
func Patch(ctx context.Context, diskPath string, opts Options) (*Result, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	password := opts.Password
	if password == "" {
		p, err := GeneratePassword(16)
		if err != nil {
			return nil, err
		}
		password = p
	}
	script, err := renderFirstBootScript(opts, password)
	if err != nil {
		return nil, err
	}

	attached, err := attachImage(ctx, diskPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = detachImage(context.WithoutCancel(ctx), attached) }()

	mnt, err := os.MkdirTemp(filepath.Dir(diskPath), "mnt-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(mnt) }()

	// noowners lets a non-root user write to a foreign APFS volume. The price
	// is wrong ownership, fixed below.
	if err := mountData(ctx, attached, mnt); err != nil {
		return nil, err
	}
	mounted := true
	defer func() {
		if mounted {
			_, _ = run(context.WithoutCancel(ctx), "umount", mnt)
		}
	}()

	sudoers := renderSudoers(opts.User)
	written, err := writePayload(mnt, script, sudoers)
	if err != nil {
		// The Setup Assistant marker is written first, so a partial payload
		// would leave the guest at a login window with no account. Undo it
		// while the volume is still mounted.
		for _, f := range payloadFiles(script, sudoers) {
			if f.Keep {
				continue
			}
			abs := filepath.Join(mnt, f.Path)
			_ = os.Chmod(abs, 0o600) // 0400 markers deny even owner writes
			_ = os.Remove(abs)
		}
		return nil, err
	}

	// Authorize the screen-sharing agent while the volume is still mounted.
	// Enabling the service alone leaves the guest listening but refusing every
	// session, and the alternative remedy needs a GUI session a headless guest
	// cannot offer.
	if err := applyPrivacyGrants(ctx, mnt, opts); err != nil {
		if !errors.Is(err, errNoTCCDB) {
			return nil, err
		}
		fmt.Fprintf(os.Stderr, "warning: could not authorize Screen Sharing in the guest (%v); "+
			"enable it from the guest's System Settings if you need its screen\n", err)
	}

	if out, err := run(ctx, "umount", mnt); err != nil {
		return nil, fmt.Errorf("unmount guest data volume: %w\n%s", err, out)
	}
	mounted = false

	// launchd only loads root-owned daemon plists, and a noowners-mounted
	// volume records the writer's identity instead. Fixing that needs a
	// privileged mount, hence one batched sudo call.
	if err := fixOwnership(ctx, attached, written); err != nil {
		// A half-patched guest is worse than an unpatched one: the Setup
		// Assistant marker is already in place, so the guest would boot to a
		// login window with no account. Undo the payload.
		if rollbackErr := rollbackPayload(context.WithoutCancel(ctx), attached, mnt, written, opts); rollbackErr != nil {
			return nil, fmt.Errorf("%w (and rolling the guest changes back failed: %v — the guest may boot to a login window with no usable account; delete and recreate it)", err, rollbackErr)
		}
		return nil, err
	}
	return &Result{Password: password}, nil
}

// writePayload writes the first-boot payload into the mounted guest volume.
func writePayload(mnt, script, sudoers string) ([]payloadFile, error) {
	files := payloadFiles(script, sudoers)
	for _, f := range files {
		abs := filepath.Join(mnt, f.Path)
		if f.Dir {
			if err := os.MkdirAll(abs, f.Mode); err != nil { //nolint:gosec // guest-side permissions mirror macOS defaults
				return nil, fmt.Errorf("create %s in guest: %w", f.Path, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil { //nolint:gosec // guest-side permissions mirror macOS defaults
			return nil, fmt.Errorf("create %s in guest: %w", filepath.Dir(f.Path), err)
		}
		// Re-patching an image finds read-only markers from a previous run;
		// remove first so the write cannot fail with EACCES.
		_ = os.Remove(abs)
		if err := os.WriteFile(abs, f.Data, f.Mode); err != nil { //nolint:gosec // modes are per-file constants above
			return nil, fmt.Errorf("write %s in guest: %w", f.Path, err)
		}
	}
	return files, nil
}

// rollbackPayload removes everything the patch wrote, so a failed patch
// leaves the guest as the restore left it: booting into Setup Assistant.
func rollbackPayload(ctx context.Context, attached attachedDisk, mnt string, files []payloadFile, opts Options) error {
	if err := mountData(ctx, attached, mnt); err != nil {
		return err
	}
	defer func() { _, _ = run(ctx, "umount", mnt) }()

	var errs []error
	// Undo the privacy grants too: a failed patch must leave the guest's
	// database as it was found.
	if err := dropPrivacyGrants(ctx, mnt, opts); err != nil && !errors.Is(err, errNoTCCDB) {
		errs = append(errs, err)
	}
	for _, f := range files {
		if f.Keep {
			continue
		}
		abs := filepath.Join(mnt, f.Path)
		// 0400 markers deny even owner writes; loosen before unlinking.
		_ = os.Chmod(abs, 0o600)
		if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// mountData mounts the guest's APFS Data volume without ownership, which is
// what allows a non-root user to write to it.
func mountData(ctx context.Context, attached attachedDisk, mnt string) error {
	if out, err := run(ctx, "mount", "-t", "apfs", "-o", "noowners", "/dev/"+attached.Data, mnt); err != nil {
		return fmt.Errorf("mount guest data volume /dev/%s: %w\n%s", attached.Data, err, out)
	}
	return nil
}

// fixOwnership re-mounts the guest volume with ownership honoured and sets
// root:wheel plus the intended modes on everything vee wrote. It runs as a
// single sudo invocation so the user is prompted at most once.
func fixOwnership(ctx context.Context, attached attachedDisk, files []payloadFile) error {
	mnt, err := os.MkdirTemp("", "vee-owners-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(mnt) }()

	var sb strings.Builder
	// set -e aborts before any chown if the mount failed, so the chowns can
	// never land on the empty host directory.
	fmt.Fprintf(&sb, "set -e\nmount -t apfs %s %s\n", shellQuote("/dev/"+attached.Data), shellQuote(mnt))
	fmt.Fprintf(&sb, "trap 'umount %s' EXIT\n", shellQuote(mnt))
	for _, f := range files {
		target := shellQuote(filepath.Join(mnt, f.Path))
		fmt.Fprintf(&sb, "chown 0:0 %s\n", target)
		fmt.Fprintf(&sb, "chmod %o %s\n", f.Mode.Perm(), target)
	}

	// Explain the prompt before it appears: an unexplained sudo request in the
	// middle of a long restore is alarming.
	fmt.Fprintln(os.Stderr, "vee needs sudo once to set root:wheel on the guest's first-boot launch daemon "+
		"(launchd refuses to load daemons that are not root-owned).")
	//nolint:gosec // fixed sudo/sh invocation; the script is built from vee-derived paths
	cmd := exec.CommandContext(ctx, "sudo", "sh", "-c", sb.String())
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("set guest file ownership: %w — launchd only loads root-owned daemons, so this step needs sudo; re-run `vee create`, or pass --skip-first-boot and provision the guest at its display", err)
	}
	return nil
}

// attachImage attaches the raw disk image without mounting anything and
// resolves the guest's APFS Data volume.
//
// hdiutil is used rather than the `diskutil image` verb: the latter only
// exists on macOS 26+, and vee's macOS guest disks are always raw images.
func attachImage(ctx context.Context, diskPath string) (attachedDisk, error) {
	var lastErr error
	for attempt := range attachRetries {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return attachedDisk{}, ctx.Err()
			case <-time.After(attachBackoff):
			}
		}
		out, stderr, err := output(ctx, "hdiutil", "attach",
			"-imagekey", "diskimage-class=CRawDiskImage", "-nomount", "-plist", diskPath)
		if err != nil {
			lastErr = fmt.Errorf("hdiutil attach %s: %w%s", diskPath, err, detail(stderr))
			// Only the busy case is worth retrying; anything else is
			// deterministic and retrying just wastes the user's time.
			if !strings.Contains(stderr, "Resource temporarily unavailable") {
				return attachedDisk{}, lastErr
			}
			continue
		}
		root, err := parsePlist([]byte(out))
		if err != nil {
			// The image is attached but its layout is unknown; release it
			// rather than leaking the attachment until the host reboots.
			_ = detachScrapedFrom(ctx, out)
			return attachedDisk{}, fmt.Errorf("parse hdiutil attach output: %w", err)
		}
		got := devicesFromAttach(root)
		if got.Data == "" {
			data, dataErr := resolveDataVolume(ctx, got.Container)
			if dataErr != nil {
				if detachErr := detachImage(ctx, got); detachErr != nil {
					_ = detachScrapedFrom(ctx, out)
				}
				return attachedDisk{}, dataErr
			}
			got.Data = data
		}
		return got, nil
	}
	return attachedDisk{}, lastErr
}

// resolveDataVolume asks diskutil which volume of the attached container
// holds the guest's writable state. The apfs verb exists on every macOS
// version vee supports.
func resolveDataVolume(ctx context.Context, containerDev string) (string, error) {
	if containerDev == "" {
		return "", fmt.Errorf("no APFS container in the guest disk image — is the guest actually installed?")
	}
	out, stderr, err := output(ctx, "diskutil", "apfs", "list", "-plist")
	if err != nil {
		return "", fmt.Errorf("diskutil apfs list: %w%s", err, detail(stderr))
	}
	root, err := parsePlist([]byte(out))
	if err != nil {
		return "", fmt.Errorf("parse diskutil apfs list output: %w", err)
	}
	return dataVolumeForContainer(root, containerDev)
}

// detachImage releases an attached image.
func detachImage(ctx context.Context, attached attachedDisk) error {
	dev := attached.WholeDisk
	if dev == "" {
		dev = attached.Container
	}
	if dev == "" {
		dev = attached.Data
	}
	if dev == "" {
		return fmt.Errorf("no device node to detach")
	}
	_, err := run(ctx, "hdiutil", "detach", "/dev/"+dev)
	return err
}

// detachScrapedFrom is the last-resort release path for an attach whose
// plist could not be parsed: it scrapes device nodes out of the raw output
// and detaches the shortest one (the whole disk).
func detachScrapedFrom(ctx context.Context, attachOutput string) error {
	shortest := ""
	for _, field := range strings.Fields(attachOutput) {
		trimmed := strings.Trim(field, "<>")
		idx := strings.Index(trimmed, "/dev/disk")
		if idx < 0 {
			continue
		}
		dev := strings.TrimPrefix(trimmed[idx:], "/dev/")
		if shortest == "" || len(dev) < len(shortest) {
			shortest = dev
		}
	}
	if shortest == "" {
		return fmt.Errorf("no device node found in hdiutil output")
	}
	_, err := run(ctx, "hdiutil", "detach", "/dev/"+shortest)
	return err
}

// run executes a host tool and returns its combined output.
func run(ctx context.Context, name string, args ...string) (string, error) {
	//nolint:gosec // callers pass fixed tool names with vee-derived arguments
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

// output runs a host tool with stdout and stderr captured separately, so
// machine-readable plists stay parseable while diagnostics stay reportable.
func output(ctx context.Context, name string, args ...string) (stdout, stderr string, err error) {
	//nolint:gosec // callers pass fixed tool names with vee-derived arguments
	cmd := exec.CommandContext(ctx, name, args...)
	// Force the C locale: error text is matched against English strings.
	cmd.Env = append(os.Environ(), "LANG=C", "LC_ALL=C")
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	out, err := cmd.Output()
	return string(out), errBuf.String(), err
}

// detail renders captured stderr for inclusion in an error message.
func detail(stderr string) string {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return ""
	}
	return ": " + stderr
}
