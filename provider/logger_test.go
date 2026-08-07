package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoggerLevelFollowsVerbose covers issue #76: --verbose used to change the
// log sink but not the level, which made every logger.Debug call site
// unreachable at any flag combination.
func TestLoggerLevelFollowsVerbose(t *testing.T) {
	tests := []struct {
		name      string
		silent    bool
		wantDebug bool
	}{
		{name: "default run stays at info", silent: true, wantDebug: false},
		{name: "verbose run logs debug", silent: false, wantDebug: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			logger, err := newLogger(dir, tt.silent)
			if err != nil {
				t.Fatalf("newLogger: %v", err)
			}
			logger.Debug("marker-debug")
			logger.Info("marker-info")
			if err := logger.Sync(); err != nil {
				// stderr does not always support Sync; the file core is what
				// this test reads back.
				t.Logf("sync: %v", err)
			}

			//nolint:gosec // dir is this test's own t.TempDir().
			data, err := os.ReadFile(filepath.Join(dir, "vee.log"))
			if err != nil {
				t.Fatalf("read log: %v", err)
			}
			out := string(data)

			if !strings.Contains(out, "marker-info") {
				t.Errorf("info line missing from the log file: %q", out)
			}
			if got := strings.Contains(out, "marker-debug"); got != tt.wantDebug {
				t.Errorf("debug line present = %v, want %v (log: %q)", got, tt.wantDebug, out)
			}
		})
	}
}
