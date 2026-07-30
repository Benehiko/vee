package tui

import (
	"slices"
	"testing"
)

func TestTemplatesForHost(t *testing.T) {
	// The wizard's whole purpose is that the user does not have to know which
	// constraints apply, so a template that cannot succeed must not be offered.
	// macOS guests need an Apple Silicon Mac; Windows guests need an x86_64
	// host (there is no Windows-on-ARM pipeline); everything else runs
	// anywhere vee does.
	//
	// The expected lists preserve allTemplateNames order: the wizard indexes
	// the slice by cursor position, so a reordering would silently select the
	// wrong template.
	without := func(dropped ...string) []string {
		out := make([]string, 0, len(allTemplateNames))
		for _, n := range allTemplateNames {
			if !slices.Contains(dropped, n) {
				out = append(out, n)
			}
		}
		return out
	}

	cases := []struct {
		name    string
		macOS   bool
		windows bool
		want    []string
	}{
		// The real hosts vee runs on.
		{"Apple Silicon Mac", true, false, without("windows")},
		{"x86_64 host", false, true, without("macos")},
		{"arm64 Linux host", false, false, without("macos", "windows")},
		// No real host supports both today, but the helper must stay a pure
		// filter — each capability drops its own template and nothing else.
		{"host supporting both", true, true, without()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := templatesForHost(allTemplateNames, tc.macOS, tc.windows)
			if !slices.Equal(got, tc.want) {
				t.Errorf("templatesForHost(macOS=%v, windows=%v):\ngot  %v\nwant %v",
					tc.macOS, tc.windows, got, tc.want)
			}
		})
	}
}

func TestTemplateNamesMatchesThisHost(t *testing.T) {
	// The package-level list the wizard actually renders must agree with the
	// host checks, not just the pure helper above.
	if slices.Contains(templateNames, "macos") != macOSGuestsSupported() {
		t.Errorf("templateNames offers macos = %v, but macOSGuestsSupported() = %v",
			slices.Contains(templateNames, "macos"), macOSGuestsSupported())
	}
	if slices.Contains(templateNames, "windows") != windowsGuestsSupported() {
		t.Errorf("templateNames offers windows = %v, but windowsGuestsSupported() = %v",
			slices.Contains(templateNames, "windows"), windowsGuestsSupported())
	}
}
