package images

import "testing"

func TestMacOSVersionFromFilename(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"UniversalMac_15.5_24F74_Restore.ipsw", "15.5"},
		{"UniversalMac_12.6.1_21G217_Restore.ipsw", "12.6.1"},
		{"custom.ipsw", "custom"},
	}
	for _, tt := range tests {
		if got := macOSVersionFromFilename(tt.in); got != tt.want {
			t.Errorf("macOSVersionFromFilename(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestMacOSImageSetURL(t *testing.T) {
	img := &MacOSImage{}
	if err := img.setURL("https://updates.cdn-apple.com/x/UniversalMac_15.5_24F74_Restore.ipsw"); err != nil {
		t.Fatalf("valid URL rejected: %v", err)
	}
	if img.Version() != "15.5" {
		t.Errorf("Version() = %q, want 15.5", img.Version())
	}
	if img.Name() != "UniversalMac_15.5_24F74_Restore.ipsw" {
		t.Errorf("Name() = %q", img.Name())
	}

	for _, bad := range []string{
		"http://updates.cdn-apple.com/x.ipsw", // not https
		"https://example.com/not-an-ipsw.dmg", // wrong suffix
		"latest.ipsw",                         // not a URL
		"https://example.com/",                // no filename
	} {
		if err := (&MacOSImage{}).setURL(bad); err == nil {
			t.Errorf("setURL(%q): expected error", bad)
		}
	}
}
