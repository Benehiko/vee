package qemu_test

import (
	"strings"
	"testing"

	"github.com/Benehiko/vee/internal/qemu"
)

func TestVirtiofsdArgs(t *testing.T) {
	vfs := &qemu.Virtiofsd{
		SocketPath: "/tmp/test.sock",
		Tag:        "data",
		Chardev:    "virtiofs-data",
		Device:     "vhost-user-fs-pci",
	}
	args := vfs.Args()

	if len(args) != 4 {
		t.Fatalf("expected 4 args, got %d: %v", len(args), args)
	}
	if args[0] != "-device" {
		t.Errorf("args[0]: got %q, want -device", args[0])
	}
	if !strings.HasPrefix(args[1], "vhost-user-fs-pci,") {
		t.Errorf("device must start with vhost-user-fs-pci: %q", args[1])
	}
	if !strings.Contains(args[1], "chardev=virtiofs-data") {
		t.Errorf("chardev missing from device arg: %q", args[1])
	}
	if !strings.Contains(args[1], "tag=data") {
		t.Errorf("tag missing from device arg: %q", args[1])
	}
	if args[2] != "-chardev" {
		t.Errorf("args[2]: got %q, want -chardev", args[2])
	}
	if !strings.Contains(args[3], "id=virtiofs-data") {
		t.Errorf("chardev id missing: %q", args[3])
	}
	if !strings.Contains(args[3], "path=/tmp/test.sock") {
		t.Errorf("socket path missing from chardev: %q", args[3])
	}
}

func TestVirtiofsdArgsDeviceNeverEmpty(t *testing.T) {
	// Regression: WithVirtiofsd previously left Device empty, producing
	// "-device ,chardev=..." which QEMU rejects with "Parameter 'id' expects an identifier".
	// WithVirtiofsd must always set Device = "vhost-user-fs-pci".
	opt := qemu.WithVirtiofsd("/tmp/test.sock", "data")
	machine, err := qemu.NewEmptyMachine(newTestProvider(t))
	if err != nil {
		t.Fatalf("NewEmptyMachine: %v", err)
	}
	opt(machine)
	built, err := machine.BuildMachine(
		qemu.AddDisk(qemu.NewDisk(newTestProvider(t), machine,
			qemu.WithCustomPath("/fake/disk.img"),
			qemu.WithInterface(qemu.InterfaceVirtio),
		)),
	)
	if err != nil {
		t.Fatalf("BuildMachine: %v", err)
	}
	for i, arg := range built.Args() {
		if i > 0 && built.Args()[i-1] == "-device" && strings.HasPrefix(arg, ",") {
			t.Errorf("empty device type in QEMU args: -device %q", arg)
		}
	}
}

func TestNewQemuVirtiofsdDefaults(t *testing.T) {
	vfs := qemu.NewQemuVirtiofsd(
		qemu.WithQemuVirtiofsdSocketPath("/tmp/foo.sock"),
		qemu.WithQemuVirtiofsdTag("media"),
		qemu.WithQemuVirtiofsdChardev("virtiofs-media"),
	)
	if vfs.Device != "vhost-user-fs-pci" {
		t.Errorf("default Device: got %q, want vhost-user-fs-pci", vfs.Device)
	}
	args := vfs.Args()
	if !strings.HasPrefix(args[1], "vhost-user-fs-pci,") {
		t.Errorf("Args() device: %q", args[1])
	}
}

func TestWithVirtiofsdSetsFields(t *testing.T) {
	machine, err := qemu.NewEmptyMachine(newTestProvider(t))
	if err != nil {
		t.Fatalf("NewEmptyMachine: %v", err)
	}
	opt := qemu.WithVirtiofsd("/tmp/movies.sock", "movies")
	opt(machine)

	// Build a minimal valid machine so we can call Args().
	built, err := machine.BuildMachine(
		qemu.AddDisk(qemu.NewDisk(newTestProvider(t), machine,
			qemu.WithCustomPath("/fake/disk.img"),
			qemu.WithInterface(qemu.InterfaceVirtio),
		)),
	)
	if err != nil {
		t.Fatalf("BuildMachine: %v", err)
	}

	args := built.Args()
	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "vhost-user-fs-pci,chardev=virtiofs-movies,tag=movies") {
		t.Errorf("vhost-user-fs-pci device arg missing: %s", joined)
	}
	if !strings.Contains(joined, "socket,id=virtiofs-movies,path=/tmp/movies.sock") {
		t.Errorf("chardev socket arg missing: %s", joined)
	}
}

func TestWithVirtiofsdMultipleMountsUniqueChardevs(t *testing.T) {
	machine, err := qemu.NewEmptyMachine(newTestProvider(t))
	if err != nil {
		t.Fatalf("NewEmptyMachine: %v", err)
	}

	built, err := machine.BuildMachine(
		qemu.AddDisk(qemu.NewDisk(newTestProvider(t), machine,
			qemu.WithCustomPath("/fake/disk.img"),
			qemu.WithInterface(qemu.InterfaceVirtio),
		)),
		qemu.WithVirtiofsd("/tmp/movies.sock", "movies"),
		qemu.WithVirtiofsd("/tmp/shows.sock", "shows"),
	)
	if err != nil {
		t.Fatalf("BuildMachine: %v", err)
	}

	args := built.Args()
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"vhost-user-fs-pci,chardev=virtiofs-movies,tag=movies",
		"vhost-user-fs-pci,chardev=virtiofs-shows,tag=shows",
		"socket,id=virtiofs-movies,path=/tmp/movies.sock",
		"socket,id=virtiofs-shows,path=/tmp/shows.sock",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing expected arg %q in: %s", want, joined)
		}
	}

	// Chardev IDs must be unique — duplicate IDs cause QEMU to reject the config.
	movieCount := strings.Count(joined, "virtiofs-movies")
	showsCount := strings.Count(joined, "virtiofs-shows")
	if movieCount < 2 {
		t.Errorf("virtiofs-movies should appear in both -device and -chardev; count=%d", movieCount)
	}
	if showsCount < 2 {
		t.Errorf("virtiofs-shows should appear in both -device and -chardev; count=%d", showsCount)
	}
}
