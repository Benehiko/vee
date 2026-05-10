package mirror

import (
	"strings"
	"testing"
)

func TestParseMode(t *testing.T) {
	cases := map[string]Mode{
		"on":       ModeOn,
		"force":    ModeOn,
		"off":      ModeOff,
		"disabled": ModeOff,
		"":         ModeAuto,
		"auto":     ModeAuto,
		"garbage":  ModeAuto,
	}
	for in, want := range cases {
		if got := ParseMode(in); got != want {
			t.Errorf("ParseMode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPacmanMirrorlistContent(t *testing.T) {
	out := PacmanMirrorlistContent("http://10.0.2.2:9129/repo/archlinux/$repo/os/$arch")
	for _, want := range []string{
		"Server = http://10.0.2.2:9129/repo/archlinux/$repo/os/$arch",
		"Server = https://geo.mirror.pkgbuild.com/$repo/os/$arch",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("mirrorlist missing %q\n---\n%s", want, out)
		}
	}
}
