package tui

import (
	"slices"
	"testing"
)

func TestTemplatesForHost(t *testing.T) {
	// The wizard's whole purpose is that the user does not have to know which
	// constraints apply, so a template that cannot succeed must not be offered.
	// macOS guests need Apple Silicon; everything else runs anywhere vee does.
	t.Run("host that can run macOS guests", func(t *testing.T) {
		got := templatesForHost(allTemplateNames, true)
		if !slices.Contains(got, "macos") {
			t.Error("an Apple Silicon Mac was not offered the macos template")
		}
		if len(got) != len(allTemplateNames) {
			t.Errorf("offered %d templates, want all %d", len(got), len(allTemplateNames))
		}
	})

	t.Run("host that cannot", func(t *testing.T) {
		got := templatesForHost(allTemplateNames, false)
		if slices.Contains(got, "macos") {
			t.Error("offered the macos template on a host that would refuse it at the last step")
		}
		if len(got) != len(allTemplateNames)-1 {
			t.Errorf("offered %d templates, want %d — only macos should be dropped",
				len(got), len(allTemplateNames)-1)
		}
		// Everything else must survive, in order: the list is indexed by the
		// wizard's cursor, so a reordering would silently select the wrong one.
		want := make([]string, 0, len(allTemplateNames)-1)
		for _, n := range allTemplateNames {
			if n != "macos" {
				want = append(want, n)
			}
		}
		if !slices.Equal(got, want) {
			t.Errorf("template order changed:\ngot  %v\nwant %v", got, want)
		}
	})
}

func TestTemplateNamesMatchesThisHost(t *testing.T) {
	// The package-level list the wizard actually renders must agree with the
	// host check, not just the pure helper above.
	if slices.Contains(templateNames, "macos") != macOSGuestsSupported() {
		t.Errorf("templateNames offers macos = %v, but macOSGuestsSupported() = %v",
			slices.Contains(templateNames, "macos"), macOSGuestsSupported())
	}
}
