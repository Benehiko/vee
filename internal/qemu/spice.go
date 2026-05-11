package qemu

import (
	"fmt"
)

type Spice struct {
	// the port on which the spice server will listen
	Port int
	// if true simple authentication method is not used
	DisableTicketing bool
}

var _ Builder = &Spice{}

type SpiceOption func(*Spice)

func WithSpicePort(port int) SpiceOption {
	return func(q *Spice) {
		q.Port = port
	}
}

func WithSpiceDisableTicketing(disable bool) SpiceOption {
	return func(q *Spice) {
		q.DisableTicketing = disable
	}
}

func NewSpice(opts ...SpiceOption) *Spice {
	qs := &Spice{
		Port:             5930,
		DisableTicketing: true,
	}
	for _, opt := range opts {
		opt(qs)
	}
	return qs
}

// Args returns the qemu arguments for the spice server
// example of qemu spice args:
//
//	-spice port=3001,disable-ticketing \
//	-device intel-hda -device hda-duplex \
//	-device virtio-serial -chardev spicevmc,id=vdagent,debug=0,name=vdagent \
//	-device virtserialport,chardev=vdagent,name=com.redhat.spice.0
func (qs *Spice) Args() []string {
	var args []string
	args = append(args, "-spice", fmt.Sprintf("port=%d,disable-ticketing=%t,gl=off", qs.Port, qs.DisableTicketing))
	// -soundhw was removed in QEMU 6.0; use device-based HDA instead.
	args = append(args, "-device", "intel-hda", "-device", "hda-duplex")
	args = append(args, "-device", "virtio-serial", "-chardev", "spicevmc,id=vdagent,debug=0,name=vdagent")
	args = append(args, "-device", "virtserialport,chardev=vdagent,name=com.redhat.spice.0")
	return args
}
