package cloudinit

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"unicode/utf16"
)

// buildISONative writes a minimal ISO9660 image with a Joliet supplementary
// descriptor containing exactly the NoCloud seed files "user-data" and
// "meta-data" in the root of a volume labelled "cidata". It requires no
// external tools (xorriso/genisoimage/hdiutil), which is why it is the fallback
// on Windows and on any host lacking those tools.
//
// The layout is deliberately simple — a flat root directory with two files —
// because that is all cloud-init's NoCloud datasource reads. Both the primary
// (ISO9660) and Joliet directory trees describe the same files so the seed is
// readable regardless of which the guest kernel mounts.
func buildISONative(isoPath, udPath, mdPath string) error {
	ud, err := os.ReadFile(udPath) //nolint:gosec // internally-built path under the VM directory
	if err != nil {
		return fmt.Errorf("read user-data: %w", err)
	}
	md, err := os.ReadFile(mdPath) //nolint:gosec // internally-built path under the VM directory
	if err != nil {
		return fmt.Errorf("read meta-data: %w", err)
	}

	img, err := writeISO("cidata", []isoFile{
		{name: "user-data", data: ud},
		{name: "meta-data", data: md},
	})
	if err != nil {
		return err
	}

	// isoPath is derived from the VM directory, not user input.
	if err := os.WriteFile(isoPath, img, 0o600); err != nil { //nolint:gosec // internally-built VM path
		return fmt.Errorf("write ISO %s: %w", filepath.Base(isoPath), err)
	}
	return nil
}

const isoSectorSize = 2048

type isoFile struct {
	name string
	data []byte
}

// writeISO returns the bytes of a complete ISO9660+Joliet image.
//
// Sector map:
//
//	0..15  system area (zero)
//	16     Primary Volume Descriptor
//	17     Joliet Supplementary Volume Descriptor
//	18     Volume Descriptor Set Terminator
//	19     Type-L path table (primary)
//	20     Type-M path table (primary)
//	21     Type-L path table (Joliet)
//	22     Type-M path table (Joliet)
//	23     root directory (primary)
//	24     root directory (Joliet)
//	25..   file contents, one file starting on each new sector
func writeISO(volID string, files []isoFile) ([]byte, error) {
	const (
		pvdSector      = 16
		jolietSector   = 17
		terminatorSec  = 18
		pathLPrimary   = 19
		pathMPrimary   = 20
		pathLJoliet    = 21
		pathMJoliet    = 22
		rootPrimarySec = 23
		rootJolietSec  = 24
		firstFileSec   = 25
	)

	// A NoCloud seed is kilobytes; cap each file well below any uint32 sector-math
	// overflow so the size→uint32 conversions below are provably safe.
	const maxFileBytes = 64 << 20 // 64 MiB
	for _, f := range files {
		if len(f.data) > maxFileBytes {
			return nil, fmt.Errorf("cidata seed file %q is %d bytes; exceeds %d-byte limit", f.name, len(f.data), maxFileBytes)
		}
	}

	// Assign each file a starting sector and record its size.
	type placed struct {
		isoFile
		startSector uint32
		sectors     uint32
	}
	placedFiles := make([]placed, 0, len(files))
	sector := uint32(firstFileSec)
	for _, f := range files {
		//nolint:gosec // len(f.data) is bounded above by maxFileBytes (64 MiB), far below uint32 max
		n := uint32((len(f.data) + isoSectorSize - 1) / isoSectorSize)
		if n == 0 {
			n = 1 // an empty file still occupies one extent in our simple layout
		}
		placedFiles = append(placedFiles, placed{isoFile: f, startSector: sector, sectors: n})
		sector += n
	}
	totalSectors := sector

	buf := make([]byte, int(totalSectors)*isoSectorSize)

	// Primary Volume Descriptor (type 1) and Joliet SVD (type 2) reuse the same
	// builder, differing in the type byte, escape sequence, and identifier
	// encoding (ASCII vs UCS-2 big-endian).
	writeSector(buf, pvdSector, buildVolumeDescriptor(volDescParams{
		descType:      1,
		joliet:        false,
		volID:         volID,
		totalSectors:  totalSectors,
		pathTableLBA:  pathLPrimary,
		pathTableMLBA: pathMPrimary,
		rootDirLBA:    rootPrimarySec,
	}))
	writeSector(buf, jolietSector, buildVolumeDescriptor(volDescParams{
		descType:      2,
		joliet:        true,
		volID:         volID,
		totalSectors:  totalSectors,
		pathTableLBA:  pathLJoliet,
		pathTableMLBA: pathMJoliet,
		rootDirLBA:    rootJolietSec,
	}))

	// Volume Descriptor Set Terminator (type 255).
	term := make([]byte, isoSectorSize)
	term[0] = 0xFF
	copy(term[1:6], "CD001")
	term[6] = 1
	writeSector(buf, terminatorSec, term)

	// Path tables: a single root entry each. Little-endian (Type-L) and
	// big-endian (Type-M) variants for both primary and Joliet.
	writeSector(buf, pathLPrimary, buildRootPathTable(rootPrimarySec, false, false))
	writeSector(buf, pathMPrimary, buildRootPathTable(rootPrimarySec, true, false))
	writeSector(buf, pathLJoliet, buildRootPathTable(rootJolietSec, false, true))
	writeSector(buf, pathMJoliet, buildRootPathTable(rootJolietSec, true, true))

	// Root directory extents. Each lists "." and ".." plus one record per file.
	dirEntries := make([]dirRecord, 0, len(placedFiles))
	for _, pf := range placedFiles {
		dirEntries = append(dirEntries, dirRecord{
			name:        pf.name,
			startSector: pf.startSector,
			//nolint:gosec // len(pf.data) is bounded above by maxFileBytes (64 MiB)
			length: uint32(len(pf.data)),
			isDir:  false,
		})
	}
	writeSector(buf, rootPrimarySec, buildDirectory(rootPrimarySec, dirEntries, false))
	writeSector(buf, rootJolietSec, buildDirectory(rootJolietSec, dirEntries, true))

	// File contents.
	for _, pf := range placedFiles {
		off := int(pf.startSector) * isoSectorSize
		copy(buf[off:off+len(pf.data)], pf.data)
	}

	return buf, nil
}

func writeSector(buf []byte, sector int, data []byte) {
	off := sector * isoSectorSize
	copy(buf[off:off+isoSectorSize], data)
}

type volDescParams struct {
	descType      byte
	joliet        bool
	volID         string
	totalSectors  uint32
	pathTableLBA  uint32
	pathTableMLBA uint32
	rootDirLBA    uint32
}

func buildVolumeDescriptor(p volDescParams) []byte {
	s := make([]byte, isoSectorSize)
	s[0] = p.descType
	copy(s[1:6], "CD001")
	s[6] = 1 // version
	if p.joliet {
		// Escape sequence selecting UCS-2 Level 3 (Joliet): %/E.
		copy(s[88:91], []byte{0x25, 0x2F, 0x45})
	}

	// System identifier (32) left blank; Volume identifier (32) at offset 40.
	writeStrField(s[40:72], p.volID, p.joliet)

	// Volume space size (total sectors) — both-endian at offset 80.
	putBothEndian32(s[80:88], p.totalSectors)

	// Volume set size (1) and sequence number (1) — both-endian 16.
	putBothEndian16(s[120:124], 1)
	putBothEndian16(s[124:128], 1)

	// Logical block size (2048) — both-endian 16.
	putBothEndian16(s[128:132], isoSectorSize)

	// Path table size — one root entry: 8 bytes (name len 1 padded to even) +
	// dir identifier. Root path table record is 10 bytes.
	putBothEndian32(s[132:140], rootPathTableSize())
	// Type-L path table location (LE only, offset 140), Type-M (BE only, 148).
	binary.LittleEndian.PutUint32(s[140:144], p.pathTableLBA)
	binary.BigEndian.PutUint32(s[148:152], p.pathTableMLBA)

	// Root directory record (34 bytes) at offset 156.
	root := buildDirRecord(dirRecord{name: "", startSector: p.rootDirLBA, length: isoSectorSize, isDir: true}, dirSelf, p.joliet)
	copy(s[156:156+len(root)], root)

	// Text identifier fields (volume set, publisher, data preparer, application)
	// and the date fields span offsets 190..881. Leaving them as NUL bytes is
	// accepted by lenient readers but rejected by strict validators (libisofs),
	// so fill the a/d-char text region with the padding character. On the
	// primary tree that is an ASCII space; on Joliet it is a UCS-2 space.
	fillTextFields(s[190:813], p.joliet)

	// File structure version — REQUIRED; a zero here is the classic cause of a
	// "damaged Primary Volume Descriptor" rejection.
	s[881] = 1

	return s
}

// fillTextFields fills a region of a volume descriptor with the padding
// character used for empty a/d-char identifier fields.
func fillTextFields(dst []byte, joliet bool) {
	if joliet {
		for i := 0; i+1 < len(dst); i += 2 {
			dst[i] = 0x00
			dst[i+1] = 0x20 // UCS-2 space
		}
		return
	}
	for i := range dst {
		dst[i] = ' '
	}
}

// writeStrField writes a padded identifier. Joliet fields are UCS-2 big-endian
// and padded with UCS-2 spaces; primary fields are ASCII padded with spaces.
func writeStrField(dst []byte, s string, joliet bool) {
	if joliet {
		enc := ucs2BE(s)
		if len(enc) > len(dst) {
			enc = enc[:len(dst)&^1]
		}
		copy(dst, enc)
		for i := len(enc); i+1 < len(dst); i += 2 {
			dst[i] = 0x00
			dst[i+1] = 0x20 // UCS-2 space
		}
		return
	}
	copy(dst, s)
	for i := len(s); i < len(dst); i++ {
		dst[i] = ' '
	}
}

func ucs2BE(s string) []byte {
	u16 := utf16.Encode([]rune(s))
	out := make([]byte, 0, len(u16)*2)
	for _, r := range u16 {
		//nolint:gosec // intentional: split a uint16 code unit into big-endian byte pair
		out = append(out, byte(r>>8), byte(r))
	}
	return out
}

func putBothEndian32(dst []byte, v uint32) {
	binary.LittleEndian.PutUint32(dst[0:4], v)
	binary.BigEndian.PutUint32(dst[4:8], v)
}

func putBothEndian16(dst []byte, v uint16) {
	binary.LittleEndian.PutUint16(dst[0:2], v)
	binary.BigEndian.PutUint16(dst[2:4], v)
}

func rootPathTableSize() uint32 {
	// Directory identifier length 1 (root = 0x00), record = 8 + 1 padded to 8+2?
	// ISO9660 path table record: 8 fixed bytes + identifier length, padded to
	// even. Root identifier is a single 0x00 byte → 8 + 1 = 9, padded to 10.
	return 10
}

func buildRootPathTable(rootDirLBA uint32, bigEndian, _ bool) []byte {
	s := make([]byte, isoSectorSize)
	// One record for the root directory.
	s[0] = 1 // directory identifier length (root uses a single 0x00 byte)
	s[1] = 0 // extended attribute record length
	if bigEndian {
		binary.BigEndian.PutUint32(s[2:6], rootDirLBA)
		binary.BigEndian.PutUint16(s[6:8], 1) // parent directory number
	} else {
		binary.LittleEndian.PutUint32(s[2:6], rootDirLBA)
		binary.LittleEndian.PutUint16(s[6:8], 1)
	}
	s[8] = 0x00 // directory identifier (root)
	s[9] = 0x00 // padding to even length
	return s
}

type dirRecord struct {
	name        string
	startSector uint32
	length      uint32
	isDir       bool
}

const (
	dirNormal = iota
	dirSelf   // "." entry (identifier 0x00)
	dirParent // ".." entry (identifier 0x01)
)

// buildDirectory assembles a directory extent for the given file records,
// always prefixed by the "." and ".." entries.
func buildDirectory(dirLBA uint32, files []dirRecord, joliet bool) []byte {
	s := make([]byte, isoSectorSize)
	off := 0

	self := buildDirRecord(dirRecord{startSector: dirLBA, length: isoSectorSize, isDir: true}, dirSelf, joliet)
	copy(s[off:], self)
	off += len(self)

	parent := buildDirRecord(dirRecord{startSector: dirLBA, length: isoSectorSize, isDir: true}, dirParent, joliet)
	copy(s[off:], parent)
	off += len(parent)

	for _, f := range files {
		rec := buildDirRecord(f, dirNormal, joliet)
		// A directory record may not cross a sector boundary; our seed is tiny
		// and always fits, so a simple append is sufficient here.
		copy(s[off:], rec)
		off += len(rec)
	}
	return s
}

// buildDirRecord encodes one ISO9660 directory record. kind selects the special
// "." / ".." identifiers; otherwise the file name is used (with ";1" version
// suffix on the primary tree, raw UCS-2 on Joliet).
func buildDirRecord(r dirRecord, kind int, joliet bool) []byte {
	var ident []byte
	switch kind {
	case dirSelf:
		ident = []byte{0x00}
	case dirParent:
		ident = []byte{0x01}
	default:
		name := r.name
		if !joliet {
			// Primary tree: uppercase 8.3-ish name with a version suffix. Our
			// names ("user-data"/"meta-data") are kept lowercase-insensitive by
			// cloud-init, but ISO9660 identifiers are conventionally uppercased.
			ident = []byte(name + ";1")
		} else {
			ident = ucs2BE(name)
		}
	}

	recLen := 33 + len(ident)
	if recLen%2 != 0 {
		recLen++ // pad to even length
	}
	// The record-length and identifier-length fields are single bytes, so a
	// directory record cannot exceed 255 bytes. Our identifiers are the fixed
	// NoCloud names ("user-data"/"meta-data", ≤ ~24 UCS-2 bytes), so this never
	// triggers; guard anyway to keep the byte conversions below provably safe.
	if recLen > 255 {
		recLen = 255
	}
	b := make([]byte, recLen)
	//nolint:gosec // recLen is bounded to [0,255] above
	b[0] = byte(recLen) // length of directory record
	b[1] = 0            // extended attribute record length
	putBothEndian32(b[2:10], r.startSector)
	putBothEndian32(b[10:18], r.length)
	// Recording date/time (7 bytes at offset 18) left zero — acceptable for a
	// throwaway seed; cloud-init does not read it.
	var flags byte
	if r.isDir {
		flags |= 0x02
	}
	b[25] = flags
	b[26] = 0                    // file unit size (non-interleaved)
	b[27] = 0                    // interleave gap size
	putBothEndian16(b[28:32], 1) // volume sequence number
	//nolint:gosec // len(ident) ≤ recLen-33 ≤ 222, fits a byte
	b[32] = byte(len(ident))
	copy(b[33:], ident)
	return b
}
