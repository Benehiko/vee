package templates

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/Benehiko/vee/internal/images"
	"github.com/Benehiko/vee/internal/vm"
	"github.com/Benehiko/vee/provider"
)

const (
	// DockerTCPPort is the port Docker daemon listens on inside the VM.
	// Forwarded to the same port on localhost via user-mode NAT.
	DockerTCPPort = 2375
)

// NewDockerConfig returns a VMConfig for a lightweight Alpine Linux VM running
// Docker daemon. The Docker API is exposed on tcp://localhost:2375 on the host
// via user-mode port forwarding (no TLS — suitable for local use only).
//
// Connect the host docker CLI with:
//
//	export DOCKER_HOST=tcp://localhost:2375
func NewDockerConfig(ctx context.Context, p provider.Provider, name string, sshKeys []string, alpineVersion string) (*vm.VMConfig, error) {
	if alpineVersion == "" {
		alpineVersion = "latest"
	}

	img, err := images.NewImage(p, images.DistroAlpine, alpineVersion)
	if err != nil {
		return nil, fmt.Errorf("docker image: %w", err)
	}
	if err := img.Download(ctx); err != nil {
		return nil, fmt.Errorf("docker image download: %w", err)
	}

	conf := p.Config()
	vmDir := filepath.Join(conf.StoragePath, name)
	sshPort := deterministicSSHPort(name)

	// cloud-init for Alpine: install Docker from Alpine's community repo,
	// enable the daemon, and configure it to listen on TCP + unix socket.
	runCmds := []string{
		// Enable community repo (Docker is in community).
		`sed -i 's|#.*community|http://dl-cdn.alpinelinux.org/alpine/latest-stable/community|' /etc/apk/repositories`,
		"apk update",
		"apk add --no-cache docker docker-cli",
		// Configure daemon to listen on TCP (localhost port-forwarded from host).
		`mkdir -p /etc/docker`,
		`printf '{"hosts":["unix:///var/run/docker.sock","tcp://0.0.0.0:2375"]}\n' > /etc/docker/daemon.json`,
		// Add default user to docker group.
		"addgroup alpine docker",
		// Start Docker on boot.
		"rc-update add docker default",
		"service docker start",
	}

	cfg := &vm.VMConfig{
		Name:     name,
		Template: "docker",
		Memory:   "2G",
		CPUs:     2,
		Sockets:  1,
		Cores:    2,
		Threads:  1,
		CPUModel: conf.DefaultCPUModel,
		NIC: vm.NICConfig{
			Mode:  "user",
			Model: "virtio-net-pci",
			// Forward SSH and Docker API ports to the host.
			HostFwds: []string{
				fmt.Sprintf("tcp:127.0.0.1:%d-:22", sshPort),
				fmt.Sprintf("tcp:127.0.0.1:%d-:%d", DockerTCPPort, DockerTCPPort),
			},
		},
		GPU:      vm.GPUConfig{Mode: vm.GPUNone},
		Headless: true,
		SSHPort:  sshPort,
		UEFI:     vm.UEFIConfig{Enabled: false},
		Disks: []vm.DiskConfig{
			{
				Path:        filepath.Join(vmDir, "storage", "disk-os.img"),
				Size:        "10G",
				Format:      "qcow2",
				Interface:   "virtio",
				Media:       "disk",
				Cache:       "writeback",
				BackingFile: img.AbsolutePath(),
			},
		},
		CloudInit: &vm.CloudInitConfig{
			Hostname:    name,
			DefaultUser: images.DefaultUser(images.DistroAlpine),
			SSHKeys:     sshKeys,
			RunCmds:     runCmds,
		},
		CreatedAt: time.Now(),
	}

	return cfg, nil
}
