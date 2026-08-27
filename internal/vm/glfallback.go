package vm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"

	"github.com/Benehiko/vee/internal/backend"
	"github.com/Benehiko/vee/internal/platform"
)

// glCrashMarker is the error QEMU prints when a -display gl= suboption names
// a backend compiled without OpenGL, e.g.
// "OpenGL is not supported by display backend 'cocoa'".
const glCrashMarker = "OpenGL is not supported by display backend"

// retryStartWithoutGL relaunches a VM whose QEMU exited at startup because its
// windowed display backend lacks OpenGL — the common case on macOS, where
// neither the bundled nor the Homebrew QEMU is built with cocoa GL. The
// relaunch downgrades to the plain 2D adapter for this boot only; the config
// keeps GL so a future GL-capable QEMU picks it back up. Returns the
// replacement start result and whether a retry happened.
func (m *Manager) retryStartWithoutGL(ctx context.Context, cfg *VMConfig, recovery bool, res *backend.StartResult) (*backend.StartResult, bool, error) {
	if !platform.IsMacOS() || cfg.GPU.Mode != GPUVirtio || cfg.Headless || cfg.SPICE != nil || cfg.GPU.disableGL {
		return res, false, nil
	}
	if isAlive(res.PID) || !glCrashInQEMULog(filepath.Join(m.vmDir(cfg.Name), "qemu.log")) {
		return res, false, nil
	}

	m.provider.Logger().Warn("QEMU display backend has no OpenGL — retrying with the 2D virtio-gpu adapter",
		zap.String("vm", cfg.Name))
	// stderr, not stdout: the manager also runs under the MCP server, whose
	// stdout carries the protocol.
	fmt.Fprintf(os.Stderr, "Warning: this QEMU's windowed display has no OpenGL support — booting %q with the 2D virtio-gpu adapter (guest renders in software)\n", cfg.Name)

	retryCfg := *cfg
	retryCfg.GPU.disableGL = true
	machine, _, err := m.buildBackendMachine(ctx, &retryCfg, recovery)
	if err != nil {
		return res, false, fmt.Errorf("rebuild without GL: %w", err)
	}
	res2, err := machine.StartDetached(ctx)
	if err != nil {
		return res, false, err
	}
	return res2, true, nil
}

// glCrashInQEMULog reports whether the latest boot section of qemu.log carries
// the GL-unsupported error. Sections are delimited by the "=== boot on ==="
// banner StartDetached writes, so an old crash never triggers a downgrade of
// a later, healthy boot.
func glCrashInQEMULog(logPath string) bool {
	data, err := os.ReadFile(logPath) //nolint:gosec // logPath is the VM's own qemu.log under vee's storage dir.
	if err != nil {
		return false
	}
	s := string(data)
	if i := strings.LastIndex(s, "=== boot on "); i >= 0 {
		s = s[i:]
	}
	return strings.Contains(s, glCrashMarker)
}
