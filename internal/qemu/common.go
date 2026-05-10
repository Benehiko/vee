package qemu

import "context"

type Builder interface {
	Args() []string
}

type Machine interface {
	Name() string
	AbsolutePath() string
	Start(ctx context.Context) error
}
