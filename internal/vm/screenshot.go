package vm

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"time"

	"github.com/Benehiko/vee/internal/backend"
	"github.com/Benehiko/vee/internal/qemu"
)

// Screenshot captures a running QEMU-backed VM's primary display and returns
// it PNG-encoded along with its pixel dimensions. QMP's screendump writes a
// PPM file (the one format every QEMU build supports, unlike its optional
// PNG output); vee converts it so callers never see the intermediate.
//
// The QMP command is routed like any other (owner connection, daemon, direct
// dial — see QMPExecute). vz-backed VMs have no QMP socket and are rejected
// with a pointer at Screen Sharing.
func (m *Manager) Screenshot(ctx context.Context, name string) ([]byte, int, int, error) {
	state, err := m.loadState(name)
	if err != nil {
		return nil, 0, 0, err
	}
	if !state.Running {
		return nil, 0, 0, fmt.Errorf("VM %q is not running", name)
	}
	if state.BackendName() == backend.VZ {
		return nil, 0, 0, fmt.Errorf("VM %q runs on the vz backend, which has no screenshot support; macOS guests expose Screen Sharing instead (vee view)", name)
	}
	if state.QMPSocket == "" {
		return nil, 0, 0, fmt.Errorf("VM %q has no QMP socket", name)
	}

	// QEMU (same user, same host) writes the dump; keep it in a private temp
	// dir that is removed regardless of outcome.
	tmpDir, err := os.MkdirTemp("", "vee-screendump-*")
	if err != nil {
		return nil, 0, 0, err
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()
	ppmPath := filepath.Join(tmpDir, "screen.ppm")

	if _, err := m.QMPExecute(ctx, name, "screendump", map[string]any{"filename": ppmPath}, 5*time.Second); err != nil {
		return nil, 0, 0, fmt.Errorf("screendump: %w", err)
	}
	raw, err := os.ReadFile(ppmPath) //nolint:gosec // path is built above in a fresh MkdirTemp dir
	if err != nil {
		return nil, 0, 0, fmt.Errorf("read screendump output: %w", err)
	}
	img, err := qemu.DecodePPM(raw)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("decode screendump: %w", err)
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, 0, 0, fmt.Errorf("encode PNG: %w", err)
	}
	b := img.Bounds()
	return buf.Bytes(), b.Dx(), b.Dy(), nil
}
