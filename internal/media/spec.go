package media

import (
	"fmt"
	"strings"
)

// ParseSpec parses one media spec string into a Source. This is the shared
// syntax behind the CLI's --media flag and the MCP server's media parameter.
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
func ParseSpec(spec string) (Source, error) {
	atIdx := strings.LastIndex(spec, "@")
	if atIdx < 0 {
		return Source{}, fmt.Errorf("media spec: missing @<guest-path> in %q", spec)
	}
	head, tail := spec[:atIdx], spec[atIdx+1:]
	if head == "" || tail == "" {
		return Source{}, fmt.Errorf("media spec: empty source or guest path in %q", spec)
	}

	guestPath, suffix := tail, ""
	if i := strings.LastIndex(tail, ":"); i > 0 {
		guestPath, suffix = tail[:i], tail[i+1:]
	}
	if !strings.HasPrefix(guestPath, "/") {
		return Source{}, fmt.Errorf("media spec: guest path must be absolute, got %q", guestPath)
	}

	switch {
	case strings.HasPrefix(head, "hostdir:"):
		hostDir := strings.TrimPrefix(head, "hostdir:")
		if hostDir == "" {
			return Source{}, fmt.Errorf("media spec: hostdir: missing host path in %q", spec)
		}
		return Source{
			Kind:      KindHostDir,
			HostDir:   hostDir,
			GuestPath: guestPath,
			ReadOnly:  suffix == "ro",
		}, nil

	case strings.HasPrefix(head, "nfs://"):
		server, export, ok := strings.Cut(strings.TrimPrefix(head, "nfs://"), "/")
		if !ok || server == "" || export == "" {
			return Source{}, fmt.Errorf("media spec: nfs needs server/export, got %q", spec)
		}
		return Source{
			Kind:      KindNFS,
			GuestPath: guestPath,
			ReadOnly:  suffix == "ro",
			NFS: &NFSSource{
				Server: server,
				Export: "/" + export,
			},
		}, nil

	case strings.HasPrefix(head, "smb://"):
		rest := strings.TrimPrefix(head, "smb://")
		var user string
		if u, r, ok := strings.Cut(rest, "@"); ok {
			user = u
			rest = r
		}
		server, share, ok := strings.Cut(rest, "/")
		if !ok || server == "" || share == "" {
			return Source{}, fmt.Errorf("media spec: smb needs server/share, got %q", spec)
		}
		return Source{
			Kind:      KindSMB,
			GuestPath: guestPath,
			ReadOnly:  suffix == "ro",
			SMB: &SMBSource{
				Server:   server,
				Share:    share,
				Username: user,
			},
		}, nil

	case strings.HasPrefix(head, "block:"):
		devPath := strings.TrimPrefix(head, "block:")
		if devPath == "" {
			return Source{}, fmt.Errorf("media spec: block: missing device path in %q", spec)
		}
		fsType := suffix
		if fsType == "ro" {
			fsType = ""
		}
		return Source{
			Kind:      KindBlock,
			GuestPath: guestPath,
			Block: &BlockSource{
				DevPath: devPath,
				FSType:  fsType,
			},
		}, nil

	case strings.HasPrefix(head, "usb:"):
		body := strings.TrimPrefix(head, "usb:")
		usb := &USBSource{}
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
				return Source{}, fmt.Errorf("media spec: usb needs VENDOR:PRODUCT or bus=N,addr=M, got %q", spec)
			}
			usb.VendorID = vendor
			usb.ProductID = product
		}
		if suffix != "" && suffix != "ro" {
			usb.MountFSType = suffix
		}
		return Source{
			Kind:      KindUSB,
			GuestPath: guestPath,
			USB:       usb,
		}, nil
	}

	return Source{}, fmt.Errorf("media spec: unknown kind in %q (expected hostdir:, nfs://, smb://, block:, usb:)", spec)
}

// ParseSpecs parses every spec; returns the first error encountered.
func ParseSpecs(specs []string) ([]Source, error) {
	sources := make([]Source, 0, len(specs))
	for _, s := range specs {
		src, err := ParseSpec(s)
		if err != nil {
			return nil, err
		}
		sources = append(sources, src)
	}
	return sources, nil
}
