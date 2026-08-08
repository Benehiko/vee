package cmd

import "testing"

// TestParseNFSMounts covers the --nfs-mount SERVER:EXPORT:GUESTPATH spec.
// Both the export and the guest path are absolute, so the colon split is
// unambiguous for IPv4/hostnames; IPv6 literals must be bracketed.
func TestParseNFSMounts(t *testing.T) {
	cases := []struct {
		name      string
		specs     []string
		want      []struct{ server, export, guest string }
		wantError bool
	}{
		{
			name:  "ipv4 server",
			specs: []string{"192.168.178.76:/mnt/Data/Movies:/downloads/movies"},
			want: []struct{ server, export, guest string }{
				{"192.168.178.76", "/mnt/Data/Movies", "/downloads/movies"},
			},
		},
		{
			name:  "hostname server",
			specs: []string{"truenas.local:/mnt/Data/Shows:/downloads/shows"},
			want: []struct{ server, export, guest string }{
				{"truenas.local", "/mnt/Data/Shows", "/downloads/shows"},
			},
		},
		{
			name:  "bracketed ipv6",
			specs: []string{"[fd00::1]:/export:/downloads"},
			want: []struct{ server, export, guest string }{
				{"fd00::1", "/export", "/downloads"},
			},
		},
		{
			name: "multiple mounts",
			specs: []string{
				"192.168.178.76:/mnt/Data/Movies:/downloads/movies",
				"192.168.178.76:/mnt/Data/Shows:/downloads/shows",
			},
			want: []struct{ server, export, guest string }{
				{"192.168.178.76", "/mnt/Data/Movies", "/downloads/movies"},
				{"192.168.178.76", "/mnt/Data/Shows", "/downloads/shows"},
			},
		},
		{name: "blank spec skipped", specs: []string{"  "}},
		{name: "missing guest path", specs: []string{"192.168.178.76:/mnt/Data/Movies"}, wantError: true},
		{name: "no colon at all", specs: []string{"192.168.178.76"}, wantError: true},
		{name: "relative export", specs: []string{"192.168.178.76:mnt/Data:/downloads"}, wantError: true},
		{name: "relative guest path", specs: []string{"192.168.178.76:/mnt/Data:downloads"}, wantError: true},
		{name: "empty server", specs: []string{":/mnt/Data:/downloads"}, wantError: true},
		{name: "unterminated ipv6", specs: []string{"[fd00::1:/export:/downloads"}, wantError: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseNFSMounts(tc.specs)
			if tc.wantError {
				if err == nil {
					t.Fatalf("parseNFSMounts(%q) = %+v, want error", tc.specs, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseNFSMounts(%q): %v", tc.specs, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("parseNFSMounts(%q) returned %d mounts, want %d", tc.specs, len(got), len(tc.want))
			}
			for i, w := range tc.want {
				if got[i].Server != w.server || got[i].Export != w.export || got[i].GuestPath != w.guest {
					t.Errorf("mount %d = {%q %q %q}, want {%q %q %q}",
						i, got[i].Server, got[i].Export, got[i].GuestPath, w.server, w.export, w.guest)
				}
			}
		})
	}
}
