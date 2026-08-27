package vm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGLCrashInQEMULog(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{name: "missing file", content: "", want: false},
		{
			name:    "latest boot crashed",
			content: "=== boot on 2026-08-27T10:00:00Z ===\nqemu-system-aarch64: OpenGL is not supported by display backend 'cocoa'\n",
			want:    true,
		},
		{
			name: "old crash, latest boot healthy",
			content: "=== boot on 2026-08-27T10:00:00Z ===\nqemu-system-aarch64: OpenGL is not supported by display backend 'cocoa'\n" +
				"=== boot on 2026-08-27T11:00:00Z ===\nsome normal output\n",
			want: false,
		},
		{
			name:    "unrelated failure",
			content: "=== boot on 2026-08-27T10:00:00Z ===\nqemu-system-aarch64: could not open disk image\n",
			want:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "qemu.log")
			if tt.content != "" {
				if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if got := glCrashInQEMULog(path); got != tt.want {
				t.Errorf("glCrashInQEMULog() = %v, want %v", got, tt.want)
			}
		})
	}
}
