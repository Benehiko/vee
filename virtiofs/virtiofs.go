package virtiofs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/Benehiko/vee/utils"
)

// Virtiofsd is a struct that represents the virtiofsd process
// that will be used to share a directory with the guest.
// The virtiofsd process will create a socket file that the
// guest will use to communicate with the host.
// The guest will mount the shared directory using the tag
// provided in the Virtiofsd struct.
//
// Below is an example of qemu using host virtiofsd to share a directory with the guest:
// -chardev socket,id=tiny10,path=/mnt/4TB/QemuVM/tiny10-passthrough/tiny10-fs.sock \
// -device vhost-user-fs-pci,queue-size=1024,chardev=tiny10,tag=Games \
// -object memory-backend-memfd,id=mem24,size=24G,share=on \
// -numa node,memdev=mem24
type Virtiofsd struct {
	// path to the virtiofsd binary
	VirtiofsdPath string
	// path to the virtiofsd socket
	// if empty a random socket path will be generated
	SocketPath string
	// path to the directory that will be shared with the guest
	// the guest will mount this directory using the tag provided
	ShareDir string
	// tag which the guest will use to mount the virtiofsd filesystem
	Tag string
	// if true the virtiofsd process will announce submounts to the guest
	// for example sharing /mnt will announce /mnt/subdir1, /mnt/subdir2, etc.
	AnnounceSubmounts bool
	// enable writeback cache
	Writeback bool
}

type VirtiofsdOption func(*Virtiofsd)

func WithVirtiofsdPath(virtiofsdPath string) func(*Virtiofsd) {
	return func(v *Virtiofsd) {
		v.VirtiofsdPath = virtiofsdPath
	}
}

func WithSocketPath(socketPath string) func(*Virtiofsd) {
	return func(v *Virtiofsd) {
		v.SocketPath = socketPath
	}
}

func WithAnnounceSubmounts(announceSubmounts bool) func(*Virtiofsd) {
	return func(v *Virtiofsd) {
		v.AnnounceSubmounts = announceSubmounts
	}
}

func WithWriteback(writeback bool) func(*Virtiofsd) {
	return func(v *Virtiofsd) {
		v.Writeback = writeback
	}
}

func WithTag(tag string) func(*Virtiofsd) {
	return func(v *Virtiofsd) {
		v.Tag = tag
	}
}

func NewVirtiofsd(shareDir string, opts ...VirtiofsdOption) (*Virtiofsd, error) {
	virtiofsd := &Virtiofsd{
		VirtiofsdPath: "virtiofsd",
		ShareDir:      shareDir,
	}

	for _, opt := range opts {
		opt(virtiofsd)
	}

	if virtiofsd.SocketPath == "" {
		socketPath := os.TempDir()
		socketName, err := utils.GenerateRandomString(8)
		if err != nil {
			return nil, err
		}
		virtiofsd.SocketPath = filepath.Join(socketPath, socketName+".sock")
	}

	return virtiofsd, nil
}

// Start is a blocking function that starts the virtiofsd process.
func (v *Virtiofsd) Start(ctx context.Context) error {
	var args []string
	args = append(args, "--socket-path", v.SocketPath)
	args = append(args, "--share-dir", v.ShareDir)

	if v.AnnounceSubmounts {
		args = append(args, "--announce-submounts")
	}

	if v.Writeback {
		args = append(args, "--writeback")
	}

	if v.Tag != "" {
		args = append(args, "--tag", v.Tag)
	}

	return exec.CommandContext(ctx, v.VirtiofsdPath, args...).Run()
}
