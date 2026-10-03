package vm

import (
	"fmt"
	"strings"
)

// ValidateQemuEnv checks qemu_env entries are KEY=VALUE with a non-empty key
// and no NUL bytes, so a malformed entry fails with its value in the error
// rather than being silently dropped by the OS.
func ValidateQemuEnv(env []string) error {
	for _, e := range env {
		k, _, ok := strings.Cut(e, "=")
		if !ok || k == "" || strings.ContainsAny(e, "\x00") {
			return fmt.Errorf("qemu_env entry %q: want KEY=VALUE", e)
		}
	}
	return nil
}
