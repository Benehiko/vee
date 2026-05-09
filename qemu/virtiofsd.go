package qemu

type Virtiofsd struct {
	// path to the virtiofsd socket
	SocketPath string
	// tag which the guest will use to mount the virtiofsd filesystem
	Tag string
	// a unique identifier for the virtiofsd device
	Chardev string

	Device string
	Object string
}

var _ Builder = &Virtiofsd{}

type QemuVirtiofsdOption func(*Virtiofsd)

func WithQemuVirtiofsdSocketPath(socketPath string) QemuVirtiofsdOption {
	return func(q *Virtiofsd) {
		q.SocketPath = socketPath
	}
}

func WithQemuVirtiofsdTag(tag string) QemuVirtiofsdOption {
	return func(q *Virtiofsd) {
		q.Tag = tag
	}
}

func WithQemuVirtiofsdChardev(chardev string) QemuVirtiofsdOption {
	return func(q *Virtiofsd) {
		q.Chardev = chardev
	}
}

func WithQemuVirtiofsdDevice(device string) QemuVirtiofsdOption {
	return func(q *Virtiofsd) {
		q.Device = device
	}
}

func WithQemuVirtiofsdObject(object string) QemuVirtiofsdOption {
	return func(q *Virtiofsd) {
		q.Object = object
	}
}

func NewQemuVirtiofsd(opts ...QemuVirtiofsdOption) *Virtiofsd {
	q := &Virtiofsd{
		Device: "vhost-user-fs-pci",
		Object: "virtiofsd",
	}
	for _, opt := range opts {
		opt(q)
	}
	return q
}

// Args returns the qemu arguments for the virtiofsd device
// example of qemu virtiofsd args:
//
//	-device virtiofsd-pci,chardev=chardev0,tag=tag0 -chardev socket,id=chardev0,path=/path/to/socket
func (q *Virtiofsd) Args() []string {
	var args []string
	args = append(args, "-device", q.Device+",chardev="+q.Chardev+",tag="+q.Tag)
	args = append(args, "-chardev", "socket,id="+q.Chardev+",path="+q.SocketPath)
	return args
}
