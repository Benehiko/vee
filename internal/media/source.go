// Package media describes external storage sources that can be attached to a
// vee VM (host directories shared via virtiofs, NFS/SMB network mounts, raw
// block devices passed through, USB devices). A Source describes "what" to
// attach; Plan() returns a Patch of VMConfig fragments the caller merges into
// the final VMConfig and CloudInitConfig.
//
// Plan is pure: it does not mutate the caller's VMConfig and does not perform
// I/O. Secret values (e.g. SMB passwords) are not stored on Source; callers
// collect them via PendingPrompt and pass them back through the secrets map.
package media

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Benehiko/vee/internal/vm"
)

// Kind enumerates the supported storage backends.
type Kind string

const (
	KindHostDir Kind = "host-dir" // local host directory shared via virtiofs
	KindNFS     Kind = "nfs"      // NFSv4 server export
	KindSMB     Kind = "smb"      // SMB/CIFS share
	KindBlock   Kind = "block"    // raw host block device passthrough
	KindUSB     Kind = "usb"      // USB device passthrough
)

// Distro is mirrored here to avoid a cycle with internal/cloudinit; values are
// kept in lockstep with cloudinit.Distro string values.
type Distro string

const (
	Ubuntu Distro = "ubuntu"
	Arch   Distro = "arch"
	Fedora Distro = "fedora"
)

// Source describes a single media attachment.
//
// Only the field matching Kind is consulted. Unused sub-fields must be nil/zero.
type Source struct {
	Kind      Kind
	GuestPath string // absolute path inside the VM (e.g. /media/movies)
	ReadOnly  bool   // mount read-only when supported

	HostDir string       // KindHostDir
	NFS     *NFSSource   // KindNFS
	SMB     *SMBSource   // KindSMB
	Block   *BlockSource // KindBlock
	USB     *USBSource   // KindUSB
}

// NFSSource describes an NFSv4 export.
type NFSSource struct {
	Server  string // host or IP, e.g. "truenas.lan"
	Export  string // server-side export path, e.g. "/mnt/Data/Movies"
	Version string // NFS version, e.g. "4.2" (defaults to "4.2")
	Options string // extra comma-separated mount options
}

// SMBSource describes an SMB/CIFS share.
type SMBSource struct {
	Server   string // host or IP
	Share    string // share name (no leading slashes)
	Domain   string // optional Windows domain
	Username string // username; password is supplied via Plan secrets map
}

// BlockSource passes a host block device through to the guest as a virtio-blk
// disk. FSType lets the guest mount it; leave empty to skip auto-mount.
type BlockSource struct {
	DevPath string // /dev/disk/by-id/...
	Serial  string // optional; derived from DevPath if empty
	FSType  string // e.g. "ext4", "xfs". Empty → guest does not auto-mount.
}

// USBSource passes a host USB device through. Identify either by
// VendorID+ProductID (preferred — survives re-plug) or HostBus+HostAddr.
// If MountFSType is set, the guest writes a udev/systemd rule to auto-mount.
type USBSource struct {
	VendorID    string // hex without 0x prefix, e.g. "0951"
	ProductID   string // hex without 0x prefix, e.g. "1666"
	HostBus     string // alternative: USB bus number
	HostAddr    string // alternative: device address on bus
	MountFSType string // optional — auto-mount with this FS at GuestPath
}

// Patch is the set of VMConfig/CloudInitConfig fragments produced by a Source.
//
// Patches are additive: callers merge multiple patches into the target config
// in any order. Patch is also the unit of test assertion for Plan().
type Patch struct {
	VirtiofsMounts []vm.VirtiofsMount
	Disks          []vm.DiskConfig
	ExtraDevices   []string
	Packages       []string
	RunCmds        []string
	WriteFiles     []vm.CloudInitWriteFile
}

// Merge appends every slice in other onto p. Order is preserved.
func (p *Patch) Merge(other Patch) {
	p.VirtiofsMounts = append(p.VirtiofsMounts, other.VirtiofsMounts...)
	p.Disks = append(p.Disks, other.Disks...)
	p.ExtraDevices = append(p.ExtraDevices, other.ExtraDevices...)
	p.Packages = append(p.Packages, other.Packages...)
	p.RunCmds = append(p.RunCmds, other.RunCmds...)
	p.WriteFiles = append(p.WriteFiles, other.WriteFiles...)
}

// PendingPrompt is a secret the caller must collect from the user before Plan
// can produce a final Patch. The Key is stable per Source so callers can echo
// it back through the secrets map.
type PendingPrompt struct {
	Key    string // stable identifier, e.g. "smb-password:server/share"
	Prompt string // human-readable prompt label
	Secret bool   // true → mask input
}

// Plan returns the Patch for s plus any prompts the caller still owes.
//
// If pending prompts are returned the Patch is still partially populated with
// non-secret content; callers should re-invoke Plan with the prompts answered
// in secrets to obtain the final Patch. Keys in secrets correspond to
// PendingPrompt.Key.
func (s Source) Plan(distro Distro, secrets map[string]string) (Patch, []PendingPrompt, error) {
	if s.GuestPath == "" {
		return Patch{}, nil, fmt.Errorf("media: GuestPath is required")
	}
	if !strings.HasPrefix(s.GuestPath, "/") {
		return Patch{}, nil, fmt.Errorf("media: GuestPath must be absolute, got %q", s.GuestPath)
	}

	switch s.Kind {
	case KindHostDir:
		return planHostDir(s)
	case KindNFS:
		return planNFS(s, distro)
	case KindSMB:
		return planSMB(s, distro, secrets)
	case KindBlock:
		return planBlock(s)
	case KindUSB:
		return planUSB(s)
	default:
		return Patch{}, nil, fmt.Errorf("media: unknown Kind %q", s.Kind)
	}
}

// unitName escapes an absolute path into a systemd unit base name.
// Example: "/media/my movies" → "media-my\x20movies".
//
// Mirrors `systemd-escape --path` behavior for the characters we care about.
func unitName(absPath string) string {
	trimmed := strings.Trim(absPath, "/")
	if trimmed == "" {
		return "-"
	}
	// systemd-escape replaces '/' with '-' and uses backslash-escaped hex for
	// characters outside [A-Za-z0-9:_.\-]; for simplicity we only escape space.
	r := strings.NewReplacer(
		"/", "-",
		" ", `\x20`,
	)
	return r.Replace(trimmed)
}

func planHostDir(s Source) (Patch, []PendingPrompt, error) {
	if s.HostDir == "" {
		return Patch{}, nil, fmt.Errorf("media: HostDir is required for kind=host-dir")
	}
	tag := virtiofsTag(s.GuestPath)
	mountOpts := "defaults"
	if s.ReadOnly {
		mountOpts = "ro,defaults"
	}
	patch := Patch{
		VirtiofsMounts: []vm.VirtiofsMount{{SharedDir: s.HostDir, Tag: tag}},
		RunCmds: []string{
			fmt.Sprintf("mkdir -p %s", s.GuestPath),
			fmt.Sprintf("mount -t virtiofs %s %s -o %s", tag, s.GuestPath, mountOpts),
			fmt.Sprintf("grep -qF %q /etc/fstab || echo %q >> /etc/fstab", s.GuestPath,
				fmt.Sprintf("%s %s virtiofs %s 0 0", tag, s.GuestPath, mountOpts)),
		},
	}
	return patch, nil, nil
}

func virtiofsTag(guestPath string) string {
	// Strip leading slash; replace internal slashes and spaces.
	trimmed := strings.Trim(guestPath, "/")
	if trimmed == "" {
		return "share"
	}
	return strings.NewReplacer("/", "-", " ", "_").Replace(trimmed)
}

func planNFS(s Source, distro Distro) (Patch, []PendingPrompt, error) {
	if s.NFS == nil || s.NFS.Server == "" || s.NFS.Export == "" {
		return Patch{}, nil, fmt.Errorf("media: NFSSource Server and Export are required")
	}
	version := s.NFS.Version
	if version == "" {
		version = "4.2"
	}

	opts := []string{"_netdev", "nofail", "soft", "timeo=30", "retrans=2", "vers=" + version}
	if s.ReadOnly {
		opts = append(opts, "ro")
	}
	if s.NFS.Options != "" {
		opts = append(opts, s.NFS.Options)
	}
	mountOptions := strings.Join(opts, ",")

	unitBase := unitName(s.GuestPath)
	mountUnit := unitBase + ".mount"
	automountUnit := unitBase + ".automount"

	mountContent := fmt.Sprintf(`[Unit]
Description=NFS mount for %s
After=network-online.target
Wants=network-online.target

[Mount]
What=%s:%s
Where=%s
Type=nfs4
Options=%s

[Install]
WantedBy=multi-user.target
`, s.GuestPath, s.NFS.Server, s.NFS.Export, s.GuestPath, mountOptions)

	automountContent := fmt.Sprintf(`[Unit]
Description=Automount for %s
After=network-online.target
Wants=network-online.target

[Automount]
Where=%s
TimeoutIdleSec=600

[Install]
WantedBy=multi-user.target
`, s.GuestPath, s.GuestPath)

	patch := Patch{
		Packages: nfsPackages(distro),
		WriteFiles: []vm.CloudInitWriteFile{
			{
				Path:        "/etc/systemd/system/" + mountUnit,
				Content:     mountContent,
				Permissions: "0644",
			},
			{
				Path:        "/etc/systemd/system/" + automountUnit,
				Content:     automountContent,
				Permissions: "0644",
			},
		},
		RunCmds: []string{
			fmt.Sprintf("mkdir -p %s", s.GuestPath),
			fmt.Sprintf("systemctl daemon-reload && systemctl enable --now %s", automountUnit),
		},
	}
	return patch, nil, nil
}

func nfsPackages(distro Distro) []string {
	switch distro {
	case Ubuntu:
		return []string{"nfs-common"}
	case Arch:
		return []string{"nfs-utils"}
	case Fedora:
		return []string{"nfs-utils"}
	default:
		return []string{"nfs-common"}
	}
}

func planSMB(s Source, distro Distro, secrets map[string]string) (Patch, []PendingPrompt, error) {
	if s.SMB == nil || s.SMB.Server == "" || s.SMB.Share == "" {
		return Patch{}, nil, fmt.Errorf("media: SMBSource Server and Share are required")
	}
	user := s.SMB.Username
	if user == "" {
		user = "guest"
	}

	promptKey := fmt.Sprintf("smb-password:%s/%s", s.SMB.Server, s.SMB.Share)
	password, ok := secrets[promptKey]
	prompts := []PendingPrompt(nil)
	if !ok {
		prompts = []PendingPrompt{{
			Key:    promptKey,
			Prompt: fmt.Sprintf("SMB password for //%s/%s (user %s)", s.SMB.Server, s.SMB.Share, user),
			Secret: true,
		}}
	}

	// Build the credentials file content. If we don't have the password yet we
	// still emit the units so the caller sees the full shape, but the creds
	// file is left blank and re-rendered once Plan is called with the secret.
	credsContent := fmt.Sprintf("username=%s\npassword=%s\n", user, password)
	if s.SMB.Domain != "" {
		credsContent += "domain=" + s.SMB.Domain + "\n"
	}

	credsPath := fmt.Sprintf("/etc/cifs-credentials-%s", unitName(s.GuestPath))

	opts := []string{"_netdev", "nofail", "credentials=" + credsPath, "iocharset=utf8", "uid=0", "gid=0"}
	if s.ReadOnly {
		opts = append(opts, "ro")
	}
	mountOptions := strings.Join(opts, ",")

	unitBase := unitName(s.GuestPath)
	mountUnit := unitBase + ".mount"
	automountUnit := unitBase + ".automount"

	mountContent := fmt.Sprintf(`[Unit]
Description=SMB mount for %s
After=network-online.target
Wants=network-online.target

[Mount]
What=//%s/%s
Where=%s
Type=cifs
Options=%s

[Install]
WantedBy=multi-user.target
`, s.GuestPath, s.SMB.Server, s.SMB.Share, s.GuestPath, mountOptions)

	automountContent := fmt.Sprintf(`[Unit]
Description=Automount for %s
After=network-online.target
Wants=network-online.target

[Automount]
Where=%s
TimeoutIdleSec=600

[Install]
WantedBy=multi-user.target
`, s.GuestPath, s.GuestPath)

	patch := Patch{
		Packages: smbPackages(distro),
		WriteFiles: []vm.CloudInitWriteFile{
			{
				Path:        credsPath,
				Content:     credsContent,
				Permissions: "0600",
			},
			{
				Path:        "/etc/systemd/system/" + mountUnit,
				Content:     mountContent,
				Permissions: "0644",
			},
			{
				Path:        "/etc/systemd/system/" + automountUnit,
				Content:     automountContent,
				Permissions: "0644",
			},
		},
		RunCmds: []string{
			fmt.Sprintf("mkdir -p %s", s.GuestPath),
			fmt.Sprintf("systemctl daemon-reload && systemctl enable --now %s", automountUnit),
		},
	}
	return patch, prompts, nil
}

func smbPackages(distro Distro) []string {
	switch distro {
	case Ubuntu:
		return []string{"cifs-utils"}
	case Arch:
		return []string{"cifs-utils"}
	case Fedora:
		return []string{"cifs-utils"}
	default:
		return []string{"cifs-utils"}
	}
}

func planBlock(s Source) (Patch, []PendingPrompt, error) {
	if s.Block == nil || s.Block.DevPath == "" {
		return Patch{}, nil, fmt.Errorf("media: BlockSource DevPath is required")
	}
	serial := s.Block.Serial
	if serial == "" {
		serial = blockSerialFromPath(s.Block.DevPath)
	}

	disk := vm.DiskConfig{
		Path:        s.Block.DevPath,
		Format:      "raw",
		Interface:   "virtio",
		Media:       "disk",
		Cache:       "none",
		Passthrough: true,
		Serial:      serial,
		Readonly:    s.ReadOnly,
	}

	patch := Patch{Disks: []vm.DiskConfig{disk}}

	if s.Block.FSType != "" {
		opts := "defaults,nofail"
		if s.ReadOnly {
			opts = "ro,nofail"
		}
		// /dev/disk/by-id/virtio-<serial> is provided by udev for virtio-blk-pci
		// devices when the serial is set.
		guestDev := "/dev/disk/by-id/virtio-" + serial
		patch.RunCmds = []string{
			fmt.Sprintf("mkdir -p %s", s.GuestPath),
			fmt.Sprintf("grep -qF %q /etc/fstab || echo %q >> /etc/fstab", s.GuestPath,
				fmt.Sprintf("%s %s %s %s 0 2", guestDev, s.GuestPath, s.Block.FSType, opts)),
			fmt.Sprintf("mount %s || true", s.GuestPath),
		}
	}
	return patch, nil, nil
}

// blockSerialFromPath derives a stable 20-char QEMU serial from a
// /dev/disk/by-id path so the guest sees a consistent identifier.
func blockSerialFromPath(devPath string) string {
	base := filepath.Base(devPath)
	for _, prefix := range []string{"ata-", "scsi-", "nvme-", "wwn-", "usb-"} {
		base = strings.TrimPrefix(base, prefix)
	}
	if idx := strings.LastIndex(base, "-part"); idx > 0 {
		base = base[:idx]
	}
	if len(base) > 20 {
		base = base[:20]
	}
	return base
}

func planUSB(s Source) (Patch, []PendingPrompt, error) {
	if s.USB == nil {
		return Patch{}, nil, fmt.Errorf("media: USBSource is required for kind=usb")
	}
	var qdev string
	switch {
	case s.USB.VendorID != "" && s.USB.ProductID != "":
		qdev = fmt.Sprintf("usb-host,vendorid=0x%s,productid=0x%s", s.USB.VendorID, s.USB.ProductID)
	case s.USB.HostBus != "" && s.USB.HostAddr != "":
		qdev = fmt.Sprintf("usb-host,hostbus=%s,hostaddr=%s", s.USB.HostBus, s.USB.HostAddr)
	default:
		return Patch{}, nil, fmt.Errorf("media: USBSource requires VendorID+ProductID or HostBus+HostAddr")
	}

	patch := Patch{ExtraDevices: []string{qdev}}

	if s.USB.MountFSType != "" {
		// udev assigns /dev/disk/by-id/usb-<vendor>_<product>_* — too volatile
		// to rely on without a manufacturer string. We instead mount by label
		// or first-match heuristic via a small inline script.
		opts := "defaults,nofail,x-systemd.automount"
		if s.ReadOnly {
			opts = "ro,nofail,x-systemd.automount"
		}
		patch.RunCmds = []string{
			fmt.Sprintf("mkdir -p %s", s.GuestPath),
			// First USB block device — works for the single-USB-storage case.
			fmt.Sprintf(`USBDEV=$(lsblk -ndo NAME,TRAN | awk '$2=="usb"{print "/dev/"$1; exit}'); `+
				`if [ -n "$USBDEV" ]; then grep -qF %q /etc/fstab || echo "$USBDEV %s %s %s 0 0" >> /etc/fstab; mount %s || true; fi`,
				s.GuestPath, s.GuestPath, s.USB.MountFSType, opts, s.GuestPath),
		}
	}
	return patch, nil, nil
}
