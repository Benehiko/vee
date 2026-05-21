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

// nerdctlFullVersion pins the nerdctl "full" release. The full tarball bundles
// containerd, BuildKit, nerdctl, RootlessKit, slirp4netns and the CNI plugins —
// every component needed for a rootless container stack in one artifact. Pinning
// keeps runner images reproducible; bump deliberately.
const nerdctlFullVersion = "2.2.2"

// NewGitHubRunnerConfig returns a VMConfig for a self-hosted GitHub Actions runner.
//
// The runner uses user-mode NAT networking — it reaches GitHub via outbound HTTPS
// long-polling and requires no inbound port forwarding. runnerURL is the GitHub
// repo or org URL (e.g. https://github.com/owner/repo). runnerToken is the
// short-lived registration token obtained from the GitHub API; it is injected
// into the VM via cloud-init and is not stored in the on-disk VM config.
// labels defaults to [self-hosted, linux, kvm] when empty.
//
// The runner ships a rootless container stack — containerd, BuildKit and nerdctl,
// all running under the unprivileged "runner" user via RootlessKit. CI jobs reach
// it through CONTAINERD_ADDRESS / BUILDKIT_HOST exported in the runner
// environment, so `nerdctl` and `nerdctl build` (BuildKit) work with no root and
// no daemon socket shared from the host.
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

	// The runner user's UID is fixed at 1001 below (admin/default user takes 1000),
	// so its rootless XDG_RUNTIME_DIR and socket paths are known ahead of boot.
	// Exporting them into the runner environment lets CI jobs invoke nerdctl and
	// BuildKit against the rootless stack with no extra setup. nerdctl talks to
	// the user containerd socket; `nerdctl build` talks to the user BuildKit.
	const runnerUID = 1001
	runnerEnv := fmt.Sprintf("RUNNER_URL=%s\nRUNNER_TOKEN=%s\nRUNNER_LABELS=%s\nRUNNER_NAME=%s\n"+
		"XDG_RUNTIME_DIR=/run/user/%d\n"+
		"CONTAINERD_ADDRESS=/run/user/%d/containerd/containerd.sock\n"+
		"BUILDKIT_HOST=unix:///run/user/%d/buildkit/buildkitd.sock\n"+
		"CONTAINERD_NAMESPACE=default\n",
		runnerURL, runnerToken, labelStr, name,
		runnerUID, runnerUID, runnerUID)

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

	vsockService := vsockSSHAgentService()

	// Ubuntu 24.04 sets kernel.apparmor_restrict_unprivileged_userns=1, which
	// blocks unprivileged user namespaces unless the binary creating them has a
	// matching AppArmor profile. The nerdctl-full RootlessKit lives at
	// /usr/local/bin/rootlesskit (the stock Ubuntu profile only covers
	// /usr/bin/rootlesskit), so ship a profile for that path.
	rootlesskitAppArmor := `abi <abi/4.0>,
include <tunables/global>

profile rootlesskit /usr/local/bin/rootlesskit flags=(unconfined) {
  userns,
  include if exists <local/rootlesskit>
}
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
		{
			Path:        "/etc/systemd/system/vee-ssh-agent.service",
			Content:     vsockService,
			Permissions: "0644",
		},
		{
			Path:        "/etc/apparmor.d/usr.local.bin.rootlesskit",
			Content:     rootlesskitAppArmor,
			Permissions: "0644",
		},
	}

	nerdctlFullURL := fmt.Sprintf(
		"https://github.com/containerd/nerdctl/releases/download/v%s/nerdctl-full-%s-linux-amd64.tar.gz",
		nerdctlFullVersion, nerdctlFullVersion)

	runCmds := []string{
		// Create the runner user. Rootless containerd requires a real login user
		// with a home directory and a user systemd instance, so this is a normal
		// (UID 1001) account, not a --system/nologin one. The runner software
		// still lives in /opt/actions-runner.
		"useradd --create-home --uid 1001 --shell /bin/bash runner",
		"mkdir -p /opt/actions-runner /etc/actions-runner",
		"chown runner:runner /etc/actions-runner /opt/actions-runner",
		// Add runner to kvm group so e2e tests can use KVM acceleration.
		"usermod -aG kvm runner",
		// Allocate subordinate UID/GID ranges for rootless user namespaces.
		"usermod --add-subuids 165536-231071 --add-subgids 165536-231071 runner",
		// --- Rootless container stack: containerd + BuildKit + nerdctl ---
		// The nerdctl "full" tarball bundles every component; extract into
		// /usr/local so the binaries and the rootless setup tool are on PATH.
		fmt.Sprintf("curl -fsSL %q -o /tmp/nerdctl-full.tar.gz", nerdctlFullURL),
		"tar -C /usr/local -xzf /tmp/nerdctl-full.tar.gz",
		"rm /tmp/nerdctl-full.tar.gz",
		// Kernel sysctl required for rootless ping and privileged-port mapping.
		`printf 'net.ipv4.ping_group_range=0 2147483647\nnet.ipv4.ip_unprivileged_port_start=0\n' > /etc/sysctl.d/99-rootless.conf`,
		"sysctl --system",
		// Load the RootlessKit AppArmor profile written above so unprivileged
		// user namespaces are permitted on Ubuntu 24.04.
		"apparmor_parser -r /etc/apparmor.d/usr.local.bin.rootlesskit",
		// Allow the runner's user systemd instance to keep containerd and
		// BuildKit running without an active login session. Lingering also
		// starts systemd --user now; wait for its D-Bus socket before using it.
		"loginctl enable-linger runner",
		`for i in $(seq 1 30); do [ -S /run/user/1001/bus ] && break; sleep 1; done`,
		// Run the bundled setup tools as the runner user. `install` brings up
		// user-scoped rootless containerd; `install-buildkit` adds the rootless
		// BuildKit daemon. Both register and start systemd --user services.
		`sudo -u runner XDG_RUNTIME_DIR=/run/user/1001 DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1001/bus PATH=/usr/local/bin:/usr/bin:/bin /usr/local/bin/containerd-rootless-setuptool.sh install`,
		`sudo -u runner XDG_RUNTIME_DIR=/run/user/1001 DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1001/bus PATH=/usr/local/bin:/usr/bin:/bin /usr/local/bin/containerd-rootless-setuptool.sh install-buildkit`,
		// Enable the user services so containerd + BuildKit start on every boot
		// (lingering, set above, keeps them running with no active session).
		"sudo -u runner XDG_RUNTIME_DIR=/run/user/1001 systemctl --user enable containerd buildkit",
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
		// SSH: Ubuntu cloud images need explicit enable; required for vee ssh.
		"systemctl enable --now ssh",
		// Firewall: allow SSH (runner uses outbound HTTPS; no other inbound needed).
		"ufw allow OpenSSH",
		"ufw --force enable",
		"systemctl enable --now vee-ssh-agent",
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
			// uidmap (newuidmap/newgidmap) and dbus-user-session are mandatory for
			// rootless containerd; iptables is needed by RootlessKit's network
			// setup. build-essential (gcc, g++, make, libc6-dev) plus pkg-config
			// give CI jobs a host toolchain for Go cgo and native build steps.
			// The nerdctl-full tarball bundles the remaining binaries.
			Packages: []string{
				"curl", "ca-certificates", "ufw", "qemu-guest-agent", "jq",
				"libicu-dev", "socat", "uidmap", "dbus-user-session", "iptables",
				"build-essential", "pkg-config",
			},
			RunCmds:    runCmds,
			WriteFiles: writeFiles,
		},
		CreatedAt: time.Now(),
	}

	return cfg, nil
}
