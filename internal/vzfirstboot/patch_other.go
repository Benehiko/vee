//go:build !darwin

package vzfirstboot

import (
	"context"
	"fmt"
)

// Patch is darwin-only: it drives diskutil/hdiutil to write into an APFS
// guest volume. macOS guests only run on Apple Silicon macOS hosts anyway.
func Patch(_ context.Context, _ string, _ Options) (*Result, error) {
	return nil, fmt.Errorf("macOS guest first-boot patching requires a macOS host")
}
