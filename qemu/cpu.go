package qemu

import (
	"fmt"
	"strings"

	"github.com/Benehiko/vee/provider"
)

type QemuCPUIntelx86 string

const (
	Intelx86                       QemuCPUIntelx86 = "host"
	Intelx86CascadeLakeServer      QemuCPUIntelx86 = "CascadeLake-Server"
	Intelx86CascadeLakeServerNoTSX QemuCPUIntelx86 = "CascadeLake-Server-noTSX"
	Intelx86SkylakeServer          QemuCPUIntelx86 = "Skylake-Server"
	Intelx86SkylakeServerIBRS      QemuCPUIntelx86 = "Skylake-Server-IBRS"
	Intelx86SkylakeServerIBRSNoTSX QemuCPUIntelx86 = "Skylake-Server-IBRS-noTSX"

	Intelx86SkylakeClient          QemuCPUIntelx86 = "Skylake-Client"
	Intelx86SkylakeClientIBRS      QemuCPUIntelx86 = "Skylake-Client-IBRS"
	Intelx86SkylakeClientIBRSNoTSX QemuCPUIntelx86 = "Skylake-Client-IBRS-noTSX"

	Intelx86Broadwell          QemuCPUIntelx86 = "Broadwell"
	Intelx86BroadwellNoTSX     QemuCPUIntelx86 = "Broadwell-noTSX"
	Intelx86BroadwellIBRS      QemuCPUIntelx86 = "Broadwell-IBRS"
	Intelx86BroadwellIBRSNoTSX QemuCPUIntelx86 = "Broadwell-IBRS-noTSX"

	Intelx86Haswell          QemuCPUIntelx86 = "Haswell"
	Intelx86HaswellNoTSX     QemuCPUIntelx86 = "Haswell-noTSX"
	Intelx86HaswellIBRS      QemuCPUIntelx86 = "Haswell-IBRS"
	Intelx86HaswellIBRSNoTSX QemuCPUIntelx86 = "Haswell-IBRS-noTSX"

	Intelx86IvyBridge     QemuCPUIntelx86 = "IvyBridge"
	Intelx86IvyBridgeIBRS QemuCPUIntelx86 = "IvyBridge-IBRS"

	Intelx86SandyBridge     QemuCPUIntelx86 = "SandyBridge"
	Intelx86SandyBridgeIBRS QemuCPUIntelx86 = "SandyBridge-IBRS"

	Intelx86Westmere     QemuCPUIntelx86 = "Westmere"
	Intelx86WestmereIBRS QemuCPUIntelx86 = "Westmere-IBRS"

	Intelx86Nehalem     QemuCPUIntelx86 = "Nehalem"
	Intelx86NehalemIBRS QemuCPUIntelx86 = "Nehalem-IBRS"

	Intelx86Penryn QemuCPUIntelx86 = "Penryn"
	Intelx86Conroe QemuCPUIntelx86 = "Conroe"
)

type CPU struct {
	CPU QemuCPUIntelx86
	// flags to pass cpu features to the guest
	// e.g. "vmx", "hypervisor=on", "kvm=off", "host-passthrough"
	Flags     []string
	SMP       int
	Sockets   int
	Threads   int
	Cores     int
	EnableKVM bool
}

var _ Builder = &CPU{}

type CPUOption func(*CPU)

func WithCPUModel(cpu QemuCPUIntelx86) func(*CPU) {
	return func(c *CPU) {
		c.CPU = cpu
	}
}

func WithSMP(smp int, sockets int, threads int, cores int) func(*CPU) {
	return func(c *CPU) {
		c.SMP = smp
		c.Sockets = sockets
		c.Threads = threads
		c.Cores = cores
	}
}

func WithEnableKVM(enableKVM bool) func(*CPU) {
	return func(c *CPU) {
		c.EnableKVM = enableKVM
	}
}

func NewCPU(provider provider.Provider, opts ...CPUOption) *CPU {
	conf := provider.Config()

	cpu := &CPU{
		CPU:       QemuCPUIntelx86(conf.DefaultCPUModel),
		SMP:       conf.DefaultCPUs,
		EnableKVM: true,
	}

	for _, opt := range opts {
		opt(cpu)
	}

	return cpu
}

func (q *CPU) Args() []string {
	var args []string
	args = append(args, "-cpu", string(q.CPU))

	var smpArgs []string
	if q.Sockets > 0 {
		smpArgs = append(smpArgs, fmt.Sprintf("sockets=%d", q.Sockets))
	}
	if q.Cores > 0 {
		smpArgs = append(smpArgs, fmt.Sprintf("cores=%d", q.Cores))
	}
	if q.Threads > 0 {
		smpArgs = append(smpArgs, fmt.Sprintf("threads=%d", q.Threads))
	}
	smp := fmt.Sprintf(" %d", q.SMP)
	if len(smpArgs) > 0 {
		smp += fmt.Sprintf(",%s", strings.Join(smpArgs, ","))
	}
	args = append(args, "-smp", smp)
	if q.EnableKVM {
		args = append(args, "-enable-kvm")
	}
	return args
}
