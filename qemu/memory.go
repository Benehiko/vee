package qemu

import "fmt"

// MemfdBackend configures a shared memory backend required for VFIO GPU passthrough.
type MemfdBackend struct {
	ID   string
	Size string
}

var _ Builder = &MemfdBackend{}

func NewMemfdBackend(size string) *MemfdBackend {
	return &MemfdBackend{ID: "mem0", Size: size}
}

func (m *MemfdBackend) Args() []string {
	return []string{
		"-object", fmt.Sprintf("memory-backend-memfd,id=%s,size=%s,share=on", m.ID, m.Size),
		"-numa", fmt.Sprintf("node,memdev=%s", m.ID),
	}
}
