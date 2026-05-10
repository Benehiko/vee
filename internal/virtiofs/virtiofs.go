package virtiofs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

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
	args = append(args, "--share-dir", v.SharedDir)
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
	return exec.CommandContext(ctx, binary, v.args()...).Run()
}

// StartDetached launches virtiofsd as a detached background process and returns its PID.
func (v *Virtiofsd) StartDetached(ctx context.Context) (int, error) {
	binary := v.provider.Config().VirtiofsdPath
	cmd := exec.CommandContext(ctx, binary, v.args()...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	return cmd.Process.Pid, nil
}
