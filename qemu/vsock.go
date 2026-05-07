package qemu

import "fmt"

// VSockDevice adds a vhost-vsock-pci device so the guest can communicate with
// the host via AF_VSOCK. The GuestCID must be unique per VM (>= 3).
type VSockDevice struct {
	GuestCID uint32
}

var _ Builder = &VSockDevice{}

func NewVSockDevice(guestCID uint32) *VSockDevice {
	return &VSockDevice{GuestCID: guestCID}
}

func (v *VSockDevice) Args() []string {
	return []string{
		"-device", fmt.Sprintf("vhost-vsock-pci,guest-cid=%d", v.GuestCID),
	}
}
