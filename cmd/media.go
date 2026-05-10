package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/Benehiko/vee/internal/media"
	"golang.org/x/term"
)

// parseMediaSpec parses one --media argument into a media.Source.
//
// Supported forms:
//
//	hostdir:/host/path@/guest/path[:ro]
//	nfs://server/export@/guest/path[:ro]
//	smb://[user@]server/share@/guest/path[:ro]
//	block:/dev/disk/by-id/...@/guest/path[:fstype]
//	usb:VENDOR:PRODUCT@/guest/path[:fstype]
//	usb:bus=N,addr=M@/guest/path[:fstype]
//
// The optional ":ro" or ":<fstype>" suffix applies to whatever the kind needs;
// see each branch for the exact meaning.
func parseMediaSpec(spec string) (media.Source, error) {
	atIdx := strings.LastIndex(spec, "@")
	if atIdx < 0 {
		return media.Source{}, fmt.Errorf("--media: missing @<guest-path> in %q", spec)
	}
	head, tail := spec[:atIdx], spec[atIdx+1:]
	if head == "" || tail == "" {
		return media.Source{}, fmt.Errorf("--media: empty source or guest path in %q", spec)
	}

	guestPath, suffix := tail, ""
	if i := strings.LastIndex(tail, ":"); i > 0 {
		guestPath, suffix = tail[:i], tail[i+1:]
	}
	if !strings.HasPrefix(guestPath, "/") {
		return media.Source{}, fmt.Errorf("--media: guest path must be absolute, got %q", guestPath)
	}

	switch {
	case strings.HasPrefix(head, "hostdir:"):
		hostDir := strings.TrimPrefix(head, "hostdir:")
		if hostDir == "" {
			return media.Source{}, fmt.Errorf("--media: hostdir: missing host path in %q", spec)
		}
		return media.Source{
			Kind:      media.KindHostDir,
			HostDir:   hostDir,
			GuestPath: guestPath,
			ReadOnly:  suffix == "ro",
		}, nil

	case strings.HasPrefix(head, "nfs://"):
		server, export, ok := strings.Cut(strings.TrimPrefix(head, "nfs://"), "/")
		if !ok || server == "" || export == "" {
			return media.Source{}, fmt.Errorf("--media: nfs needs server/export, got %q", spec)
		}
		return media.Source{
			Kind:      media.KindNFS,
			GuestPath: guestPath,
			ReadOnly:  suffix == "ro",
			NFS: &media.NFSSource{
				Server: server,
				Export: "/" + export,
			},
		}, nil

	case strings.HasPrefix(head, "smb://"):
		rest := strings.TrimPrefix(head, "smb://")
		var user string
		if at := strings.Index(rest, "@"); at >= 0 {
			user = rest[:at]
			rest = rest[at+1:]
		}
		server, share, ok := strings.Cut(rest, "/")
		if !ok || server == "" || share == "" {
			return media.Source{}, fmt.Errorf("--media: smb needs server/share, got %q", spec)
		}
		return media.Source{
			Kind:      media.KindSMB,
			GuestPath: guestPath,
			ReadOnly:  suffix == "ro",
			SMB: &media.SMBSource{
				Server:   server,
				Share:    share,
				Username: user,
			},
		}, nil

	case strings.HasPrefix(head, "block:"):
		devPath := strings.TrimPrefix(head, "block:")
		if devPath == "" {
			return media.Source{}, fmt.Errorf("--media: block: missing device path in %q", spec)
		}
		fsType := suffix
		if fsType == "ro" {
			fsType = ""
		}
		return media.Source{
			Kind:      media.KindBlock,
			GuestPath: guestPath,
			Block: &media.BlockSource{
				DevPath: devPath,
				FSType:  fsType,
			},
		}, nil

	case strings.HasPrefix(head, "usb:"):
		body := strings.TrimPrefix(head, "usb:")
		usb := &media.USBSource{}
		switch {
		case strings.Contains(body, "bus=") || strings.Contains(body, "addr="):
			for kv := range strings.SplitSeq(body, ",") {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					continue
				}
				switch strings.TrimSpace(k) {
				case "bus":
					usb.HostBus = v
				case "addr":
					usb.HostAddr = v
				}
			}
		default:
			vendor, product, found := strings.Cut(body, ":")
			if !found || vendor == "" || product == "" {
				return media.Source{}, fmt.Errorf("--media: usb needs VENDOR:PRODUCT or bus=N,addr=M, got %q", spec)
			}
			usb.VendorID = vendor
			usb.ProductID = product
		}
		if suffix != "" && suffix != "ro" {
			usb.MountFSType = suffix
		}
		return media.Source{
			Kind:      media.KindUSB,
			GuestPath: guestPath,
			USB:       usb,
		}, nil
	}

	return media.Source{}, fmt.Errorf("--media: unknown kind in %q (expected hostdir:, nfs://, smb://, block:, usb:)", spec)
}

// parseMediaSpecs parses every --media argument; returns the first error encountered.
func parseMediaSpecs(specs []string) ([]media.Source, error) {
	sources := make([]media.Source, 0, len(specs))
	for _, s := range specs {
		src, err := parseMediaSpec(s)
		if err != nil {
			return nil, err
		}
		sources = append(sources, src)
	}
	return sources, nil
}

// collectMediaSecrets walks every Source, calls Plan to discover required
// prompts, and reads each value from the terminal (masking for Secret prompts).
//
// Returns a map keyed by PendingPrompt.Key suitable for re-invoking Plan.
// Secrets are not persisted anywhere by this function — the caller passes them
// straight to template/build, which writes them into the cloud-init cidata ISO
// (consumed on first boot, then discarded).
func collectMediaSecrets(sources []media.Source) (map[string]string, error) {
	secrets := map[string]string{}
	stdin := bufio.NewReader(os.Stdin)
	for _, src := range sources {
		_, prompts, err := src.Plan(media.Ubuntu, secrets)
		if err != nil {
			return nil, fmt.Errorf("plan %s: %w", src.Kind, err)
		}
		for _, pp := range prompts {
			if _, done := secrets[pp.Key]; done {
				continue
			}
			fmt.Fprintf(os.Stderr, "%s: ", pp.Prompt)
			if pp.Secret {
				pw, readErr := term.ReadPassword(int(os.Stdin.Fd()))
				fmt.Fprintln(os.Stderr)
				if readErr != nil {
					return nil, readErr
				}
				secrets[pp.Key] = string(pw)
				continue
			}
			line, readErr := stdin.ReadString('\n')
			if readErr != nil {
				return nil, readErr
			}
			secrets[pp.Key] = strings.TrimRight(line, "\r\n")
		}
	}
	return secrets, nil
}
