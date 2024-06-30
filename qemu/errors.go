package qemu

import "errors"

type QemuError error

var (
	ErrNoDisks QemuError = errors.New("no disks provided")
)
