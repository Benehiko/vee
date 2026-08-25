//go:build !windows

package platform

import "syscall"

// Filesystem magic numbers for the network and overlay filesystems that must
// not host a PostgreSQL data directory. From linux/magic.h.
const (
	magicNFS     = 0x6969
	magicSMB     = 0x517B
	magicCIFS    = 0xFF534D42
	magicSMB2    = 0xFE534D42
	magicFUSE    = 0x65735546
	magic9P      = 0x01021997
	magicCEPH    = 0x00C36400
	magicGLUSTER = 0x58465342
)

// NetworkFilesystemName returns a human-readable filesystem name when dir sits
// on a network or pass-through filesystem, or "" when it is local.
//
// This exists because pointing --pg-data-dir at a network mount does not fail —
// it hangs. PostgreSQL's initdb fsyncs thousands of small files during
// bootstrap, and over virtiofs onto NFS each of those is a full network round
// trip, which turns a ten-second initdb into hours. Worse, an NFS mount with
// the default "hard" option never returns an error, so the guest blocks
// forever rather than failing: cloud-init stalls mid-initdb and every step
// after it — including the firewall and the VPN tunnel — never runs.
func NetworkFilesystemName(dir string) string {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		// Undetectable is not the same as unsafe; let the create proceed.
		return ""
	}
	switch int64(st.Type) { //nolint:unconvert // Type is int64 on some arches, uint32 on others.
	case magicNFS:
		return "NFS"
	case magicSMB, magicCIFS, magicSMB2:
		return "SMB/CIFS"
	case magicFUSE:
		return "FUSE"
	case magic9P:
		return "9p"
	case magicCEPH:
		return "Ceph"
	case magicGLUSTER:
		return "GlusterFS"
	}
	return ""
}
