package qemu

import (
	"fmt"

	"github.com/Benehiko/vee/utils"
)

type Spice struct {
	// spice server name
	Name string
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

func WithSpiceName(name string) SpiceOption {
	return func(q *Spice) {
		q.Name = name
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
	if qs.Name == "" {
		id, _ := utils.GenerateRandomString(4)
		qs.Name = id
	}
	return qs
}

// Args returns the qemu arguments for the spice server
// example of qemu spice args:
//
//	-spice port=3001,disable-ticketing -soundhw hda \
//	-device virtio-serial -chardev spicevmc,id=vdagent,debug=0,name=vdagent \
//	-device virtserialport,chardev=vdagent,name=com.redhat.spice.0
func (qs *Spice) Args() []string {
	var args []string
	args = append(args, "-spice", fmt.Sprintf("port=%d,disable-ticketing=%t", qs.Port, qs.DisableTicketing))
	// add sound device for spice
	args = append(args, "-soundhw", "hda")
	args = append(args, "-device", "virtio-serial", "-chardev", fmt.Sprintf("spicevmc,id=%s,debug=0,name=%s", qs.Name, qs.Name))
	args = append(args, "-device", "virtserialport,chardev=vdagent,name=com.redhat.spice.0")
	return args
}
