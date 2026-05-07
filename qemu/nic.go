package qemu

import (
	"crypto/sha1"
	"fmt"
)

type NICMode string

const (
	NICBridge NICMode = "bridge"
	NICUser   NICMode = "user"
)

type NIC struct {
	Mode   NICMode
	Bridge string
	Model  string
	MAC    string
}

var _ Builder = &NIC{}

func NewNIC(mode NICMode, bridge, mac string) *NIC {
	return &NIC{
		Mode:   mode,
		Bridge: bridge,
		Model:  "virtio-net-pci",
		MAC:    mac,
	}
}

// DeterministicMAC generates a stable locally-administered MAC from a VM name.
func DeterministicMAC(name string) string {
	h := sha1.Sum([]byte(name))
	// 52:54:00 is QEMU's conventional locally-administered prefix.
	return fmt.Sprintf("52:54:%02x:%02x:%02x:%02x", h[0], h[1], h[2], h[3])
}

func (n *NIC) Args() []string {
	switch n.Mode {
	case NICBridge:
		val := fmt.Sprintf("bridge,br=%s,model=%s,mac=%s", n.Bridge, n.Model, n.MAC)
		return []string{"-nic", val}
	default:
		val := fmt.Sprintf("user,model=%s,mac=%s", n.Model, n.MAC)
		return []string{"-nic", val}
	}
}
