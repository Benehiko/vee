package mirror

import (
	"strings"
	"testing"
)

func TestRenderConfigContainsRepo(t *testing.T) {
	p := &Paths{
		CacheDir: "/tmp/vee-mirror-test/cache",
	}
	out := renderConfig(p)
	for _, want := range []string{
		"cache_dir: /tmp/vee-mirror-test/cache",
		"port: 9129",
		"archlinux:",
		"geo.mirror.pkgbuild.com",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("renderConfig missing %q\n---\n%s", want, out)
		}
	}
}

func TestRenderUnit(t *testing.T) {
	p := &Paths{
		BinPath:    "/home/u/.vee/bin/pacoloco",
		ConfigPath: "/home/u/.config/vee/mirror/pacoloco.yaml",
	}
	out := renderUnit(p)
	for _, want := range []string{
		"Description=vee pacman caching proxy",
		"ExecStart=/home/u/.vee/bin/pacoloco -config /home/u/.config/vee/mirror/pacoloco.yaml",
		"WantedBy=default.target",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("renderUnit missing %q\n---\n%s", want, out)
		}
	}
}

func TestGuestMirrorURL(t *testing.T) {
	if got, want := GuestMirrorURL(""), "http://10.0.2.2:9129/repo/archlinux/$repo/os/$arch"; got != want {
		t.Errorf("default URL: got %q want %q", got, want)
	}
	if got, want := GuestMirrorURL("192.168.1.5:9129"), "http://192.168.1.5:9129/repo/archlinux/$repo/os/$arch"; got != want {
		t.Errorf("override URL: got %q want %q", got, want)
	}
}
