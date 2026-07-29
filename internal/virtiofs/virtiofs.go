package virtiofs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/Benehiko/vee/internal/utils"
	"github.com/Benehiko/vee/provider"
)

type Virtiofsd struct {
	provider          provider.Provider
	SocketPath        string
	SharedDir         string
	Tag               string
	AnnounceSubmounts bool
	Writeback         bool
}

type VirtiofsdOption func(*Virtiofsd)

func WithVirtiofsdSocketPath(socketPath string) VirtiofsdOption {
	return func(v *Virtiofsd) {
		v.SocketPath = socketPath
	}
}

func WithVirtiofsdSharedDir(sharedDir string) VirtiofsdOption {
	return func(v *Virtiofsd) {
		v.SharedDir = sharedDir
	}
}

func WithVirtiofsdTag(tag string) VirtiofsdOption {
	return func(v *Virtiofsd) {
		v.Tag = tag
	}
}

func WithAnnounceSubmounts(v bool) VirtiofsdOption {
	return func(vd *Virtiofsd) {
		vd.AnnounceSubmounts = v
	}
}

func WithWriteback(v bool) VirtiofsdOption {
	return func(vd *Virtiofsd) {
		vd.Writeback = v
	}
}

func NewVirtiofsd(p provider.Provider, opts ...VirtiofsdOption) *Virtiofsd {
	vd := &Virtiofsd{provider: p}
	for _, opt := range opts {
		opt(vd)
	}
	if vd.SocketPath == "" {
		name, _ := utils.GenerateRandomString(8)
		vd.SocketPath = filepath.Join(os.TempDir(), name+".sock")
	}
	return vd
}

func (v *Virtiofsd) args() []string {
	var args []string
	args = append(args, "--socket-path", v.SocketPath)
	// The Rust virtiofsd (v1.13.x, what EnsureVirtiofsd builds) spells this
	// --shared-dir; the old C daemon used --share-dir.
	args = append(args, "--shared-dir", v.SharedDir)
	// --sandbox none: the default sandbox tries to drop into a new user
	// namespace and setuid(0), which fails ("Couldn't set the process uid as
	// root") and aborts virtiofsd when vee runs it as an unprivileged user, so
	// the socket is never created and QEMU crashes trying to connect. Access to
	// the share is already bounded by the invoking user's filesystem
	// permissions, so no sandbox is needed here.
	args = append(args, "--sandbox", "none")
	if v.AnnounceSubmounts {
		args = append(args, "--announce-submounts")
	}
	if v.Writeback {
		args = append(args, "--writeback")
	}
	if v.Tag != "" {
		args = append(args, "--tag", v.Tag)
	}
	return args
}

// Start blocks until the virtiofsd process exits.
func (v *Virtiofsd) Start(ctx context.Context) error {
	binary := v.provider.Config().VirtiofsdPath
	//nolint:gosec // G204: binary is the operator-configured virtiofsd path and args are built from validated struct fields, not untrusted input.
	return exec.CommandContext(ctx, binary, v.args()...).Run()
}

// StartDetached launches virtiofsd as a detached background process and returns
// its PID.
//
// The spawn context is deliberately detached from the caller's. virtiofsd has to
// live as long as the VM it backs — QEMU wires the share as a vhost-user-fs-pci
// device over a chardev socket with no reconnect, so a virtiofsd that dies takes
// the guest's mount with it for the rest of the VM's life. exec.CommandContext
// kills the child when its context is cancelled, and vee's root command cancels
// its signal context as soon as the command returns, which would SIGKILL
// virtiofsd the instant `vee start` exited. Setsid does not help: the kill goes
// to this exact pid, not to the process group.
func (v *Virtiofsd) StartDetached(ctx context.Context) (int, error) {
	binary := v.provider.Config().VirtiofsdPath
	//nolint:gosec // G204: binary is the operator-configured virtiofsd path and args are built from validated struct fields, not untrusted input.
	cmd := exec.CommandContext(context.WithoutCancel(ctx), binary, v.args()...)
	setDetachAttrs(cmd)
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	return cmd.Process.Pid, nil
}
