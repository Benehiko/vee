package qemu

import (
	"fmt"
	"strings"

	"github.com/Benehiko/vee/provider"
)

type CPUModel string

const (
	CPUHost                   CPUModel = "host"
	CPUCascadeLakeServer      CPUModel = "CascadeLake-Server"
	CPUCascadeLakeServerNoTSX CPUModel = "CascadeLake-Server-noTSX"
	CPUSkylakeServer          CPUModel = "Skylake-Server"
	CPUSkylakeServerIBRS      CPUModel = "Skylake-Server-IBRS"
	CPUSkylakeServerIBRSNoTSX CPUModel = "Skylake-Server-IBRS-noTSX"
	CPUSkylakeClient          CPUModel = "Skylake-Client"
	CPUSkylakeClientIBRS      CPUModel = "Skylake-Client-IBRS"
	CPUSkylakeClientIBRSNoTSX CPUModel = "Skylake-Client-IBRS-noTSX"
	CPUBroadwell              CPUModel = "Broadwell"
	CPUBroadwellNoTSX         CPUModel = "Broadwell-noTSX"
	CPUBroadwellIBRS          CPUModel = "Broadwell-IBRS"
	CPUBroadwellIBRSNoTSX     CPUModel = "Broadwell-IBRS-noTSX"
	CPUHaswell                CPUModel = "Haswell"
	CPUHaswellNoTSX           CPUModel = "Haswell-noTSX"
	CPUHaswellIBRS            CPUModel = "Haswell-IBRS"
	CPUHaswellIBRSNoTSX       CPUModel = "Haswell-IBRS-noTSX"
	CPUIvyBridge              CPUModel = "IvyBridge"
	CPUIvyBridgeIBRS          CPUModel = "IvyBridge-IBRS"
	CPUSandyBridge            CPUModel = "SandyBridge"
	CPUSandyBridgeIBRS        CPUModel = "SandyBridge-IBRS"
	CPUWestmere               CPUModel = "Westmere"
	CPUWestmereIBRS           CPUModel = "Westmere-IBRS"
	CPUNehalem                CPUModel = "Nehalem"
	CPUNehalemIBRS            CPUModel = "Nehalem-IBRS"
	CPUPenryn                 CPUModel = "Penryn"
	CPUConroe                 CPUModel = "Conroe"
)

// GamingCPUFlags are anti-detection flags for GPU passthrough gaming VMs.
// They hide the hypervisor from Windows and spoof an AMD KVM vendor ID.
var GamingCPUFlags = []string{
	"hypervisor=off",
	"kvm=off",
	"hv-time=on",
	"hv-relaxed=on",
	"hv-vapic=on",
	"hv-spinlocks=0x1fff",
	"hv-vendor-id=AMDKVMAMD",
	"invtsc=on",
}

type CPU struct {
	CPU       CPUModel
	Flags     []string
	SMP       int
	Sockets   int
	Threads   int
	Cores     int
	EnableKVM bool
}

var _ Builder = &CPU{}

type CPUOption func(*CPU)

func WithCPUModel(cpu CPUModel) func(*CPU) {
	return func(c *CPU) {
		c.CPU = cpu
	}
}

func WithCPUFlags(flags []string) CPUOption {
	return func(c *CPU) {
		c.Flags = append(c.Flags, flags...)
	}
}

func WithSMP(smp, sockets, threads, cores int) func(*CPU) {
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
		CPU:       CPUModel(conf.DefaultCPUModel),
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

	cpuVal := string(q.CPU)
	if len(q.Flags) > 0 {
		cpuVal += "," + strings.Join(q.Flags, ",")
	}
	args = append(args, "-cpu", cpuVal)

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
	smp := fmt.Sprintf("%d", q.SMP)
	if len(smpArgs) > 0 {
		smp += "," + strings.Join(smpArgs, ",")
	}
	args = append(args, "-smp", smp)
	if q.EnableKVM {
		args = append(args, "-enable-kvm")
	}
	return args
}
