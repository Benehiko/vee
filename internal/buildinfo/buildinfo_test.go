package buildinfo

import "testing"

func TestResolvePrefersInjectedValues(t *testing.T) {
	// A release build passes all three, and they must be reported verbatim —
	// this is the identity a bug report quotes.
	v, c, d := Resolve("v0.4.0", "1b71720", "2026-07-29T13:40:06Z")
	if v != "v0.4.0" || c != "1b71720" || d != "2026-07-29T13:40:06Z" {
		t.Errorf("Resolve = %q/%q/%q, want the injected values unchanged", v, c, d)
	}
}

func TestResolveFallsBackForUnstampedBuilds(t *testing.T) {
	// A plain `go build` injects nothing. Reporting empty strings would make a
	// binary unidentifiable, so every field must come back populated — from the
	// build info Go embeds, or as "unknown".
	v, c, d := Resolve("", "", "")
	for name, got := range map[string]string{"version": v, "commit": c, "date": d} {
		if got == "" {
			t.Errorf("%s is empty for an unstamped build; want a value or \"unknown\"", name)
		}
	}
	// The test binary itself is built from this module, so the version resolves
	// through info.Main.Version, which is empty or "(devel)" for a test binary.
	if v != "dev" && v[0] != 'v' {
		t.Errorf("version = %q, want \"dev\" or a v-prefixed module version", v)
	}
}

func TestResolveKeepsPartialStamps(t *testing.T) {
	// Nothing stamps only some fields today, but the resolver must not discard
	// the ones it was given while filling in the rest.
	v, c, d := Resolve("v9.9.9", "", "")
	if v != "v9.9.9" {
		t.Errorf("version = %q, want the injected v9.9.9", v)
	}
	if c == "" || d == "" {
		t.Errorf("commit/date not filled in: %q/%q", c, d)
	}
}
