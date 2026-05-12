package templates

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Benehiko/vee/internal/images"
	"github.com/Benehiko/vee/internal/vm"
	"github.com/Benehiko/vee/provider"
)

// NewGitHubRunnerConfig returns a VMConfig for a self-hosted GitHub Actions runner.
//
// The runner uses user-mode NAT networking — it reaches GitHub via outbound HTTPS
// long-polling and requires no inbound port forwarding. runnerURL is the GitHub
// repo or org URL (e.g. https://github.com/owner/repo). runnerToken is the
// short-lived registration token obtained from the GitHub API; it is injected
// into the VM via cloud-init and is not stored in the on-disk VM config.
// labels defaults to [self-hosted, linux, kvm] when empty.
func NewGitHubRunnerConfig(
	ctx context.Context,
	p provider.Provider,
	name string,
	sshKeys []string,
	runnerURL string,
	runnerToken string,
	labels []string,
) (*vm.VMConfig, error) {
	if runnerURL == "" {
		return nil, fmt.Errorf("github-runner: runnerURL is required")
	}
	if runnerToken == "" {
		return nil, fmt.Errorf("github-runner: runnerToken is required")
	}
	if len(labels) == 0 {
		labels = []string{"self-hosted", "linux", "kvm"}
	}

	img, err := images.NewImage(p, images.DistroUbuntu, "latest")
	if err != nil {
		return nil, fmt.Errorf("github-runner image: %w", err)
	}
	if err := img.Download(ctx); err != nil {
		return nil, fmt.Errorf("github-runner image download: %w", err)
	}

	conf := p.Config()
	vmDir := filepath.Join(conf.StoragePath, name)
	labelStr := strings.Join(labels, ",")

	runnerEnv := fmt.Sprintf("RUNNER_URL=%s\nRUNNER_TOKEN=%s\nRUNNER_LABELS=%s\nRUNNER_NAME=%s\n",
		runnerURL, runnerToken, labelStr, name)

	actionsRunnerService := `[Unit]
Description=GitHub Actions Runner
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=runner
WorkingDirectory=/opt/actions-runner
EnvironmentFile=/etc/actions-runner/runner.env
ExecStart=/opt/actions-runner/run.sh
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`

	writeFiles := []vm.CloudInitWriteFile{
		{
			Path:        "/etc/actions-runner/runner.env",
			Content:     runnerEnv,
			Permissions: "0600",
			Owner:       "runner:runner",
		},
		{
			Path:        "/etc/systemd/system/actions-runner.service",
			Content:     actionsRunnerService,
			Permissions: "0644",
		},
	}

	runCmds := []string{
		// Create runner system user and required directories.
		"useradd --system --shell /usr/sbin/nologin --home-dir /opt/actions-runner runner",
		"mkdir -p /opt/actions-runner /etc/actions-runner",
		"chown runner:runner /etc/actions-runner",
		// Add runner to kvm group so e2e tests can use KVM acceleration.
		"usermod -aG kvm runner",
		// Download and extract the latest runner release.
		`RUNNER_VERSION=$(curl -fsSL -o /dev/null -w '%{url_effective}' https://github.com/actions/runner/releases/latest | sed 's|.*/v||')`,
		`curl -fsSL "https://github.com/actions/runner/releases/download/v${RUNNER_VERSION}/actions-runner-linux-x64-${RUNNER_VERSION}.tar.gz" -o /tmp/actions-runner.tar.gz`,
		"tar -xzf /tmp/actions-runner.tar.gz -C /opt/actions-runner",
		"rm /tmp/actions-runner.tar.gz",
		"chown -R runner:runner /opt/actions-runner",
		// Install runner dependencies.
		"/opt/actions-runner/bin/installdependencies.sh",
		// Register the runner with GitHub (uses env vars from runner.env).
		`. /etc/actions-runner/runner.env && sudo -u runner /opt/actions-runner/config.sh --unattended --url "$RUNNER_URL" --token "$RUNNER_TOKEN" --labels "$RUNNER_LABELS" --name "$RUNNER_NAME"`,
		// Enable and start the runner service.
		"systemctl enable --now actions-runner",
		// Firewall: SSH only (runner uses outbound HTTPS; no inbound needed).
		"ufw allow OpenSSH",
		"ufw --force enable",
		"systemctl enable --now qemu-guest-agent",
	}

	cfg := &vm.VMConfig{
		Name:     name,
		Template: "github-runner",
		Memory:   "4G",
		CPUs:     4,
		Sockets:  1,
		Cores:    4,
		Threads:  1,
		CPUModel: conf.DefaultCPUModel,
		NIC: vm.NICConfig{
			Mode:  "user",
			Model: "virtio-net-pci",
		},
		GPU:        vm.GPUConfig{Mode: vm.GPUNone},
		Headless:   true,
		GuestAgent: true,
		SSHPort:    deterministicSSHPort(name),
		UEFI:       vm.UEFIConfig{Enabled: false},
		Disks: []vm.DiskConfig{
			{
				Path:        filepath.Join(vmDir, "storage", "disk-os.img"),
				Size:        conf.DefaultDiskSize,
				Format:      "qcow2",
				Interface:   "virtio",
				Media:       "disk",
				Cache:       "writeback",
				BackingFile: img.AbsolutePath(),
			},
		},
		CloudInit: &vm.CloudInitConfig{
			Hostname:    name,
			User:        "admin",
			DefaultUser: images.DefaultUser(images.DistroUbuntu),
			SSHKeys:     sshKeys,
			Packages:    []string{"curl", "ca-certificates", "ufw", "qemu-guest-agent", "jq", "libicu-dev"},
			RunCmds:     runCmds,
			WriteFiles:  writeFiles,
		},
		CreatedAt: time.Now(),
	}

	return cfg, nil
}
