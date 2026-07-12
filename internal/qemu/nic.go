package qemu

import (
	"crypto/sha1" //nolint:gosec // SHA-1 here derives a stable MAC from a VM name; not a security primitive. Changing it would alter MACs of existing VMs.
	"fmt"
	"strings"
)

type NICMode string

const (
	NICBridge NICMode = "bridge"
	NICUser   NICMode = "user"
)

type NIC struct {
	Mode     NICMode
	Bridge   string
	Model    string
	MAC      string
	HostFwds []string
	// Queues enables multiqueue virtio-net when > 1. Only applied in bridge mode.
	Queues int
	// BridgeHelper is the path to qemu-bridge-helper. Only used when Queues > 1.
	BridgeHelper string
}

var _ Builder = &NIC{}

func NewNIC(mode NICMode, bridge, mac string, hostfwds ...string) *NIC {
	return &NIC{
		Mode:     mode,
		Bridge:   bridge,
		Model:    "virtio-net-pci",
		MAC:      mac,
		HostFwds: hostfwds,
	}
}

// DeterministicMAC generates a stable locally-administered MAC from a VM name.
func DeterministicMAC(name string) string {
	h := sha1.Sum([]byte(name)) //nolint:gosec // SHA-1 derives a stable MAC from a VM name; not a security primitive.
	// 52:54:00 is QEMU's conventional locally-administered prefix.
	return fmt.Sprintf("52:54:%02x:%02x:%02x:%02x", h[0], h[1], h[2], h[3])
}

func (n *NIC) Args() []string {
	switch n.Mode {
	case NICBridge:
		if n.Queues > 1 {
			// -nic shorthand does not support queues=; use -netdev tap + -device split.
			helper := n.BridgeHelper
			if helper == "" {
				helper = "/usr/lib/qemu/qemu-bridge-helper"
			}
			netdev := fmt.Sprintf("bridge,id=net0,br=%s,helper=%s", n.Bridge, helper)
			device := fmt.Sprintf("%s,netdev=net0,mac=%s,mq=on,vectors=%d", n.Model, n.MAC, 2*n.Queues+2)
			return []string{"-netdev", netdev, "-device", device}
		}
		val := fmt.Sprintf("bridge,br=%s,model=%s,mac=%s", n.Bridge, n.Model, n.MAC)
		return []string{"-nic", val}
	default:
		// Use -netdev/-device split so we can set rombar=0 on the device.
		// rombar=0 disables the PXE ROM so OVMF doesn't add PXE boot entries
		// and waste time trying to network-boot before finding the disk.
		// (-nic shorthand does not support rombar.)
		netdevParts := []string{"user,id=net0"}
		for _, fwd := range n.HostFwds {
			netdevParts = append(netdevParts, "hostfwd="+fwd)
		}
		device := fmt.Sprintf("%s,netdev=net0,mac=%s,rombar=0", n.Model, n.MAC)
		return []string{"-netdev", strings.Join(netdevParts, ","), "-device", device}
	}
}
