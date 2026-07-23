package qemu_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Benehiko/vee/internal/qemu"
)

func newTestMachine(t *testing.T) qemu.Machine {
	t.Helper()
	p := newTestProvider(t)
	m, err := qemu.NewEmptyMachine(p)
	if err != nil {
		t.Fatalf("NewEmptyMachine: %v", err)
	}
	return m
}

func TestDiskArgsVirtioDefault(t *testing.T) {
	p := newTestProvider(t)
	m := newTestMachine(t)
	disk := qemu.NewDisk(p, m,
		qemu.WithCustomPath("/data/disk.qcow2"),
		qemu.WithInterface(qemu.InterfaceVirtio),
		qemu.WithFormat(qemu.QCOW2),
		qemu.WithSize("20G"),
	)
	args := disk.Args()
	joined := strings.Join(args, " ")

	if len(args) < 2 || args[0] != "-drive" {
		t.Fatalf("expected -drive as first arg, got: %v", args)
	}
	if !strings.Contains(joined, "if=virtio") {
		t.Errorf("missing if=virtio in: %s", joined)
	}
	if !strings.Contains(joined, "file=/data/disk.qcow2") {
		t.Errorf("missing file path in: %s", joined)
	}
	if !strings.Contains(joined, "format=qcow2") {
		t.Errorf("missing format=qcow2 in: %s", joined)
	}
}

func TestDiskArgsCacheVariants(t *testing.T) {
	cases := []struct {
		cache     qemu.DiskCache
		media     qemu.DiskMedia
		wantCache string
	}{
		{qemu.CacheWriteback, qemu.DiskMediaDisk, "writeback"},
		{qemu.CacheUnsafe, qemu.DiskMediaDisk, "unsafe"},
		{qemu.CacheDirectSync, qemu.DiskMediaDisk, "directsync"},
		{qemu.CacheWritethrough, qemu.DiskMediaDisk, "writethrough"},
		{qemu.CacheNone, qemu.DiskMediaCdrom, "none"},
		// cdrom forces cache=none regardless of what's set
		{qemu.CacheWriteback, qemu.DiskMediaCdrom, "none"},
	}

	for _, tc := range cases {
		p := newTestProvider(t)
		m := newTestMachine(t)
		disk := qemu.NewDisk(p, m,
			qemu.WithCustomPath("/fake/path.qcow2"),
			qemu.WithMedia(tc.media),
			qemu.WithCache(tc.cache),
			qemu.WithInterface(qemu.InterfaceVirtio),
		)
		args := disk.Args()
		joined := strings.Join(args, " ")
		want := "cache=" + tc.wantCache
		if !strings.Contains(joined, want) {
			t.Errorf("cache=%v media=%v: expected %q in %q", tc.cache, tc.media, want, joined)
		}
	}
}

func TestDiskArgsCdromFormat(t *testing.T) {
	p := newTestProvider(t)
	m := newTestMachine(t)
	disk := qemu.NewDisk(p, m,
		qemu.WithCustomPath("/iso/image.iso"),
		qemu.WithMedia(qemu.DiskMediaCdrom),
		qemu.WithFormat(qemu.QCOW2),
		qemu.WithInterface(qemu.InterfaceVirtio),
	)
	args := disk.Args()
	joined := strings.Join(args, " ")

	// CDRom: format should be cleared (FixOptions) → no "format=" in drive args
	if strings.Contains(joined, "format=") {
		t.Errorf("cdrom disk should not have format= in args: %s", joined)
	}
	// CDRom: should be readonly
	if !strings.Contains(joined, "readonly=true") {
		t.Errorf("cdrom disk should be readonly: %s", joined)
	}
}

func TestDiskArgsPassthrough(t *testing.T) {
	p := newTestProvider(t)
	m := newTestMachine(t)
	disk := qemu.NewDisk(p, m,
		qemu.WithCustomPath("/dev/disk/by-id/nvme0n1"),
		qemu.WithPassthrough(true),
		qemu.WithSerial("SERIAL123"),
	)
	args := disk.Args()
	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "format=raw") {
		t.Errorf("passthrough disk should have format=raw: %s", joined)
	}
	if !strings.Contains(joined, "if=none") {
		t.Errorf("passthrough disk should have if=none: %s", joined)
	}
	if !strings.Contains(joined, "cache=none") {
		t.Errorf("passthrough disk should have cache=none: %s", joined)
	}
	if !strings.Contains(joined, "virtio-blk-pci") {
		t.Errorf("passthrough disk should use virtio-blk-pci device: %s", joined)
	}
	if !strings.Contains(joined, "serial=SERIAL123") {
		t.Errorf("passthrough disk should carry serial: %s", joined)
	}
	if !strings.Contains(joined, "discard=unmap") {
		t.Errorf("passthrough disk should pass discards through: %s", joined)
	}
}

// Passthrough disks must get a dedicated iothread so their I/O is serviced off
// the main QEMU loop instead of contending with vCPU execution.
func TestDiskArgsPassthroughIothread(t *testing.T) {
	p := newTestProvider(t)
	m := newTestMachine(t)
	disk := qemu.NewDisk(p, m,
		qemu.WithCustomPath("/dev/disk/by-id/nvme0n1"),
		qemu.WithPassthrough(true),
	)
	args := disk.Args()
	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "-object iothread,id=iothread-disk0") {
		t.Errorf("passthrough disk should declare an iothread object: %s", joined)
	}
	if !strings.Contains(joined, "iothread=iothread-disk0") {
		t.Errorf("passthrough device should bind its iothread: %s", joined)
	}
	// The object must be declared before the device that references it.
	if strings.Index(joined, "-object iothread") > strings.Index(joined, "virtio-blk-pci") {
		t.Errorf("iothread object must precede the device referencing it: %s", joined)
	}

	// A non-passthrough disk must NOT get an iothread — only the passthrough
	// path is wired for it.
	plain := qemu.NewDisk(p, m, qemu.WithCustomPath("/data/plain.qcow2"))
	if strings.Contains(strings.Join(plain.Args(), " "), "iothread") {
		t.Errorf("non-passthrough disk should not declare an iothread: %v", plain.Args())
	}
}

// A hyperthreaded topology (1 socket / 1 core / 2 threads) must emit an -smp
// whose total matches the vCPU count; QEMU rejects a mismatch.
func TestCPUArgsHyperthreadedTopology(t *testing.T) {
	p := newTestProvider(t)
	base, err := qemu.NewEmptyMachine(p)
	if err != nil {
		t.Fatalf("NewEmptyMachine: %v", err)
	}
	// WithSMP takes (smp, sockets, threads, cores) — note threads precedes
	// cores, so 1 socket / 1 core / 2 threads is (2, 1, 2, 1).
	m, err := base.BuildMachine(
		qemu.WithCPU(qemu.NewCPU(p, qemu.WithSMP(2, 1, 2, 1))),
		qemu.AddDisk(qemu.NewDisk(p, base, qemu.WithCustomPath("/data/os.qcow2"))),
	)
	if err != nil {
		t.Fatalf("BuildMachine: %v", err)
	}
	joined := strings.Join(m.Args(), " ")
	if !strings.Contains(joined, "-smp 2,sockets=1,cores=1,threads=2") {
		t.Errorf("expected hyperthreaded -smp topology, got: %s", joined)
	}
}

// Multiple passthrough disks must each get a UNIQUE iothread id — a collision
// makes QEMU refuse to start ("duplicate object id").
func TestDiskArgsPassthroughIothreadUnique(t *testing.T) {
	p := newTestProvider(t)
	base, err := qemu.NewEmptyMachine(p)
	if err != nil {
		t.Fatalf("NewEmptyMachine: %v", err)
	}
	machine, err := base.BuildMachine(
		qemu.AddDisk(qemu.NewDisk(p, base,
			qemu.WithCustomPath("/dev/disk/by-id/ata-DISK-A"),
			qemu.WithPassthrough(true),
		)),
		qemu.AddDisk(qemu.NewDisk(p, base,
			qemu.WithCustomPath("/dev/disk/by-id/ata-DISK-B"),
			qemu.WithPassthrough(true),
		)),
	)
	if err != nil {
		t.Fatalf("BuildMachine: %v", err)
	}

	ids := map[string]int{}
	args := machine.Args()
	for i, a := range args {
		if a == "-object" && i+1 < len(args) && strings.HasPrefix(args[i+1], "iothread,id=") {
			ids[strings.TrimPrefix(args[i+1], "iothread,id=")]++
		}
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2 distinct iothread objects, got %v (args: %v)", ids, args)
	}
	for id, n := range ids {
		if n != 1 {
			t.Errorf("iothread id %q declared %d times, must be unique", id, n)
		}
	}
}

func TestDiskAbsolutePath(t *testing.T) {
	p := newTestProvider(t)
	m := newTestMachine(t)

	// Explicit .qcow2 path — returned as-is.
	disk := qemu.NewDisk(p, m, qemu.WithCustomPath("/data/disk.qcow2"))
	if disk.AbsolutePath() != "/data/disk.qcow2" {
		t.Errorf("unexpected path: %s", disk.AbsolutePath())
	}

	// Explicit .iso path — returned as-is.
	disk2 := qemu.NewDisk(p, m, qemu.WithCustomPath("/iso/image.iso"))
	if disk2.AbsolutePath() != "/iso/image.iso" {
		t.Errorf("unexpected path: %s", disk2.AbsolutePath())
	}

	// Directory-style path (no suffix) — joined with Name().
	disk3 := qemu.NewDisk(p, m, qemu.WithCustomPath("/data/storage"), qemu.WithFormat(qemu.QCOW2), qemu.WithSize("10G"))
	got := disk3.AbsolutePath()
	if !strings.HasPrefix(got, "/data/storage/") {
		t.Errorf("expected path under /data/storage/, got: %s", got)
	}

	// Passthrough path — always returned exactly.
	disk4 := qemu.NewDisk(p, m, qemu.WithCustomPath("/dev/disk/by-id/sda"), qemu.WithPassthrough(true))
	if disk4.AbsolutePath() != "/dev/disk/by-id/sda" {
		t.Errorf("passthrough path changed: %s", disk4.AbsolutePath())
	}
}

func TestDiskAHCIControllerEmittedOnce(t *testing.T) {
	p := newTestProvider(t)
	base, err := qemu.NewEmptyMachine(p)
	if err != nil {
		t.Fatalf("NewEmptyMachine: %v", err)
	}

	machine, err := base.BuildMachine(
		qemu.AddDisk(qemu.NewDisk(p, base,
			qemu.WithCustomPath("/data/sata1.qcow2"),
			qemu.WithInterface(qemu.InterfaceAHCI),
			qemu.WithFormat(qemu.QCOW2),
			qemu.WithCache(qemu.CacheNone),
		)),
		qemu.AddDisk(qemu.NewDisk(p, base,
			qemu.WithCustomPath("/data/sata2.qcow2"),
			qemu.WithInterface(qemu.InterfaceAHCI),
			qemu.WithFormat(qemu.QCOW2),
			qemu.WithCache(qemu.CacheNone),
		)),
	)
	if err != nil {
		t.Fatalf("BuildMachine: %v", err)
	}

	args := machine.Args()
	joined := strings.Join(args, " ")

	count := strings.Count(joined, "ahci,id=ahci0")
	if count != 1 {
		t.Errorf("ahci controller should appear exactly once, got %d in: %s", count, joined)
	}
}

func TestDiskAHCIArgs(t *testing.T) {
	p := newTestProvider(t)
	base, err := qemu.NewEmptyMachine(p)
	if err != nil {
		t.Fatalf("NewEmptyMachine: %v", err)
	}

	machine, err := base.BuildMachine(
		qemu.AddDisk(qemu.NewDisk(p, base,
			qemu.WithCustomPath("/data/sata.qcow2"),
			qemu.WithInterface(qemu.InterfaceAHCI),
			qemu.WithFormat(qemu.QCOW2),
			qemu.WithCache(qemu.CacheNone),
		)),
	)
	if err != nil {
		t.Fatalf("BuildMachine: %v", err)
	}

	args := machine.Args()
	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "if=none") {
		t.Errorf("AHCI disk drive should have if=none: %s", joined)
	}
	if !strings.Contains(joined, "bus=ahci0.") {
		t.Errorf("AHCI device should specify bus=ahci0.N: %s", joined)
	}
}

func TestDiskName(t *testing.T) {
	p := newTestProvider(t)
	m := newTestMachine(t)
	disk := qemu.NewDisk(p, m,
		qemu.WithFormat(qemu.QCOW2),
		qemu.WithSize("50G"),
	)
	name := disk.Name()
	if !strings.Contains(name, "50G") {
		t.Errorf("disk name should contain size: %s", name)
	}
	if !strings.HasSuffix(name, ".qcow2") {
		t.Errorf("disk name should end with .qcow2: %s", name)
	}
	// Name uses the machine's name
	if !strings.Contains(name, filepath.Base(m.AbsolutePath())) &&
		!strings.Contains(name, m.Name()) {
		t.Logf("disk name: %s (machine: %s)", name, m.Name())
	}
}
