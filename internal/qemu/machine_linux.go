//go:build linux

package qemu

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sys/unix"
)

// qemuEnv returns the environment for the QEMU child process. The managed
// vee-qemu binary has no rpath, and on non-Debian distros it needs a compat
// symlink for Debian-renamed sonames (e.g. libaio.so.1t64 -> libaio.so.1) that
// qemubin creates in the binary's own directory (~/.vee/bin). Prepending that
// directory to LD_LIBRARY_PATH lets the loader find the symlink. See issue #40.
func qemuEnv(binary string) []string {
	binDir := filepath.Dir(binary)
	env := os.Environ()
	prev := os.Getenv("LD_LIBRARY_PATH")
	val := binDir
	if prev != "" {
		val = binDir + string(os.PathListSeparator) + prev
	}
	// Replace any inherited LD_LIBRARY_PATH with the augmented value.
	out := make([]string, 0, len(env)+1)
	for _, e := range env {
		if strings.HasPrefix(e, "LD_LIBRARY_PATH=") {
			continue
		}
		out = append(out, e)
	}
	return append(out, "LD_LIBRARY_PATH="+val)
}

// applyVFIOLimits raises RLIMIT_MEMLOCK on the child process when VFIO devices
// are configured. VFIO DMA-maps the entire guest RAM into the IOMMU; without
// sufficient locked-memory headroom vfio_container_dma_map returns ENOMEM.
//
// Raising Max beyond the inherited hard limit requires CAP_SYS_RESOURCE. To
// avoid that requirement, configure unlimited memlock system-wide:
//
//	/etc/security/limits.d/vee-vfio.conf:
//	  * - memlock unlimited
func (q *BaseMachine) applyVFIOLimits(pid int) error {
	if len(q.vfioDevices) == 0 {
		return nil
	}
	var before unix.Rlimit
	if err := unix.Prlimit(pid, unix.RLIMIT_MEMLOCK, nil, &before); err != nil {
		return fmt.Errorf("get memlock rlimit: %w", err)
	}
	q.provider.Logger().Info("VFIO memlock before",
		zap.String("machine", q.name),
		zap.Int("pid", pid),
		zap.Uint64("soft_bytes", before.Cur),
		zap.Uint64("hard_bytes", before.Max),
	)

	// Try to raise both soft and hard limits to infinity (requires CAP_SYS_RESOURCE
	// or a pre-configured unlimited hard limit via /etc/security/limits.d/).
	want := unix.Rlimit{Cur: unix.RLIM_INFINITY, Max: unix.RLIM_INFINITY}
	if err := unix.Prlimit(pid, unix.RLIMIT_MEMLOCK, &want, nil); err != nil {
		// Fall back to raising soft limit to the current hard limit.
		fallback := unix.Rlimit{Cur: before.Max, Max: before.Max}
		if err2 := unix.Prlimit(pid, unix.RLIMIT_MEMLOCK, &fallback, nil); err2 != nil {
			return fmt.Errorf("set memlock rlimit: %w", err2)
		}
		q.provider.Logger().Warn("memlock hard limit capped — VFIO DMA map may fail; set 'memlock unlimited' in /etc/security/limits.d/vee-vfio.conf",
			zap.String("machine", q.name),
			zap.Uint64("hard_limit_bytes", before.Max),
		)
	} else {
		q.provider.Logger().Info("VFIO memlock raised to unlimited",
			zap.String("machine", q.name),
			zap.Int("pid", pid),
		)
	}
	return nil
}

// applyCPUPinning pins the QEMU process and all its threads to the configured
// host CPU indices using taskset. It reads /proc/<pid>/task/ to discover vCPU
// threads that QEMU spawns after start.
//
// This is a best-effort operation: failures are logged but not fatal. The host
// kernel can still schedule other work onto the pinned cores; for full isolation
// add isolcpus=<range> to the host kernel cmdline.
func (q *BaseMachine) applyCPUPinning(pid int) {
	if len(q.cpuPinning) == 0 {
		return
	}

	taskset, err := exec.LookPath("taskset")
	if err != nil {
		q.provider.Logger().Warn("taskset not found — CPU pinning skipped",
			zap.String("machine", q.name))
		return
	}

	// Build comma-separated CPU list: "4,5,6,7"
	cpuList := make([]string, len(q.cpuPinning))
	for i, c := range q.cpuPinning {
		cpuList[i] = strconv.Itoa(c)
	}
	mask := strings.Join(cpuList, ",")

	// Brief pause so QEMU has time to spawn its vCPU threads.
	time.Sleep(200 * time.Millisecond)

	// Collect all thread IDs from /proc/<pid>/task/.
	taskDir := fmt.Sprintf("/proc/%d/task", pid)
	entries, err := os.ReadDir(taskDir)
	if err != nil {
		q.provider.Logger().Warn("CPU pinning: cannot read task dir",
			zap.String("machine", q.name),
			zap.Int("pid", pid),
			zap.Error(err))
		return
	}

	pinned := 0
	for _, e := range entries {
		tid := e.Name()
		//nolint:gosec,noctx // taskset is a resolved binary; mask/tid are internally derived CPU indices/PIDs. Best-effort post-start pinning; no ctx in scope.
		out, err := exec.Command(taskset, "-cp", mask, tid).CombinedOutput()
		if err != nil {
			q.provider.Logger().Warn("CPU pinning: taskset failed for thread",
				zap.String("machine", q.name),
				zap.String("tid", tid),
				zap.String("output", strings.TrimSpace(string(out))),
				zap.Error(err))
			continue
		}
		pinned++
	}

	q.provider.Logger().Info("CPU pinning applied",
		zap.String("machine", q.name),
		zap.Int("pid", pid),
		zap.String("cpus", mask),
		zap.Int("threads_pinned", pinned))
}
