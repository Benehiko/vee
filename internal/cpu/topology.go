package cpu

import (
	"os"
	"strings"
)

// ThreadsPerCore reads the host SMT topology from sysfs.
// Returns 2 when the host has Hyper-Threading / SMT enabled, 1 otherwise.
func ThreadsPerCore() int {
	data, err := os.ReadFile("/sys/devices/system/cpu/cpu0/topology/threads_per_core")
	if err != nil {
		return 1
	}
	s := strings.TrimSpace(string(data))
	if s == "2" {
		return 2
	}
	return 1
}

// AdjustSMP resolves the effective sockets/cores/threads for a VM given the
// total vCPU count and the host topology.
//
// Rules:
//   - If Threads is already set to 2 (caller opted in explicitly), leave
//     Sockets/Cores/Threads untouched.
//   - Otherwise, if the host has SMT and cpus is even, set Threads=2,
//     Cores=cpus/2, Sockets=1.
//   - Fall back to Threads=1, Cores=cpus, Sockets=1.
func AdjustSMP(cpus, sockets, cores, threads int) (outSockets, outCores, outThreads int) {
	if threads == 2 {
		// Caller already configured SMT — honour it.
		s := sockets
		if s == 0 {
			s = 1
		}
		c := cores
		if c == 0 {
			c = cpus
		}
		return s, c, 2
	}

	if ThreadsPerCore() == 2 && cpus%2 == 0 {
		return 1, cpus / 2, 2
	}
	return 1, cpus, 1
}
