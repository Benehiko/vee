package templates

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/Benehiko/vee/images"
	"github.com/Benehiko/vee/provider"
	"github.com/Benehiko/vee/vm"
	"github.com/Benehiko/vee/vpn"
)

const (
	WGServerTunnelIP = "10.99.0.1"
	WGClientTunnelIP = "10.99.0.2"
	WGServerPort     = 51820
	WGSubnet         = "10.99.0.0/24"
)

// WGServerConfig holds the keys and port for a WireGuard server VM.
type WGServerConfig struct {
	ServerKeys *vpn.WireGuardKeyPair
	ClientKeys *vpn.WireGuardKeyPair
	// HostUDPPort is the host-side forwarded UDP port → WGServerPort inside the VM.
	HostUDPPort int
}

// NewWGServerVMConfig returns a VMConfig for a headless Ubuntu VM running a
// WireGuard server. It registers a single peer (the torrent VM client).
// hostUDPPort is the host port forwarded to UDP 51820 inside the server VM.
func NewWGServerVMConfig(ctx context.Context, p provider.Provider, name string, sshKeys []string, hostUDPPort int) (*vm.VMConfig, *WGServerConfig, error) {
	serverKeys, err := vpn.GenerateWireGuardKeyPair()
	if err != nil {
		return nil, nil, fmt.Errorf("generate server keys: %w", err)
	}
	clientKeys, err := vpn.GenerateWireGuardKeyPair()
	if err != nil {
		return nil, nil, fmt.Errorf("generate client keys: %w", err)
	}

	conf := p.Config()
	img, err := images.NewImage(p, images.DistroUbuntu, "latest")
	if err != nil {
		return nil, nil, fmt.Errorf("wg server image: %w", err)
	}
	if err := img.Download(ctx); err != nil {
		return nil, nil, fmt.Errorf("wg server image download: %w", err)
	}

	vmDir := filepath.Join(conf.StoragePath, name)

	serverConf := fmt.Sprintf(`[Interface]
PrivateKey = %s
Address = %s/24
ListenPort = %d
PostUp = iptables -A FORWARD -i wg0 -j ACCEPT; iptables -t nat -A POSTROUTING -o eth0 -j MASQUERADE
PostDown = iptables -D FORWARD -i wg0 -j ACCEPT; iptables -t nat -D POSTROUTING -o eth0 -j MASQUERADE

[Peer]
PublicKey = %s
AllowedIPs = %s/32
`, serverKeys.PrivateKey, WGServerTunnelIP, WGServerPort, clientKeys.PublicKey, WGClientTunnelIP)

	runCmds := []string{
		"sysctl -w net.ipv4.ip_forward=1",
		"echo 'net.ipv4.ip_forward=1' >> /etc/sysctl.conf",
		"systemctl enable --now wg-quick@wg0",
		"ufw allow OpenSSH",
		fmt.Sprintf("ufw allow %d/udp", WGServerPort),
		"ufw --force enable",
	}

	cfg := &vm.VMConfig{
		Name:     name,
		Template: "wg-server",
		Memory:   "512M",
		CPUs:     1,
		Sockets:  1,
		Cores:    1,
		Threads:  1,
		CPUModel: conf.DefaultCPUModel,
		NIC: vm.NICConfig{
			Mode:  "user",
			Model: "virtio-net-pci",
			HostFwds: []string{
				fmt.Sprintf("udp:127.0.0.1:%d-:%d", hostUDPPort, WGServerPort),
			},
		},
		SSHPort:  deterministicSSHPort(name),
		GPU:      vm.GPUConfig{Mode: vm.GPUNone},
		Headless: true,
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
			User:        "vee",
			DefaultUser: images.DefaultUser(images.DistroUbuntu),
			SSHKeys:     sshKeys,
			Packages:    []string{"wireguard", "resolvconf", "ufw", "iptables"},
			RunCmds:     runCmds,
			WriteFiles: []vm.CloudInitWriteFile{
				{
					Path:        "/etc/wireguard/wg0.conf",
					Content:     serverConf,
					Permissions: "0600",
				},
			},
		},
		CreatedAt: time.Now(),
	}

	wgServerCfg := &WGServerConfig{
		ServerKeys:  serverKeys,
		ClientKeys:  clientKeys,
		HostUDPPort: hostUDPPort,
	}

	return cfg, wgServerCfg, nil
}

// ClientWireGuardConfig returns the WireGuardConfig a torrent VM should use
// to connect to the WireGuard server VM via the host's loopback.
func ClientWireGuardConfig(wgs *WGServerConfig) *vpn.WireGuardConfig {
	return &vpn.WireGuardConfig{
		PrivateKey: wgs.ClientKeys.PrivateKey,
		Address:    WGClientTunnelIP + "/32",
		DNS:        "1.1.1.1",
		Endpoint:   fmt.Sprintf("127.0.0.1:%d", wgs.HostUDPPort),
		PublicKey:  wgs.ServerKeys.PublicKey,
	}
}
