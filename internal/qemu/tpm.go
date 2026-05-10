package qemu

import "fmt"

// TPM configures the QEMU side of a swtpm socket-based TPM 2.0 device.
// The swtpm daemon must be started separately before QEMU.
type TPM struct {
	// SocketPath is the Unix socket created by swtpm.
	SocketPath string
}

var _ Builder = &TPM{}

func NewTPM(socketPath string) *TPM {
	return &TPM{SocketPath: socketPath}
}

func (t *TPM) Args() []string {
	chardev := fmt.Sprintf("socket,id=chrtpm,path=%s", t.SocketPath)
	return []string{
		"-chardev", chardev,
		"-tpmdev", "emulator,id=tpm0,chardev=chrtpm",
		"-device", "tpm-tis,tpmdev=tpm0",
	}
}
