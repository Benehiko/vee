package vm

import (
	"slices"
	"testing"
)

func TestBuildScpArgs(t *testing.T) {
	tests := []struct {
		name         string
		user         string
		host         string
		port         int
		spec         CopySpec
		windowsGuest bool
		want         []string
	}{
		{
			name: "to guest with forwarded port",
			user: "dev", host: "127.0.0.1", port: 2201,
			spec: CopySpec{GuestPath: "/home/dev/x", HostPath: "./x", ToGuest: true},
			want: []string{
				"-o", "BatchMode=yes",
				"-o", "UserKnownHostsFile=/kh",
				"-o", "StrictHostKeyChecking=accept-new",
				"-P", "2201", "-i", "/id",
				"./x", "dev@127.0.0.1:/home/dev/x",
			},
		},
		{
			name: "from guest on port 22 omits -P",
			user: "dev", host: "192.168.64.5", port: 22,
			spec: CopySpec{GuestPath: "/var/log/syslog", HostPath: "syslog"},
			want: []string{
				"-o", "BatchMode=yes",
				"-o", "UserKnownHostsFile=/kh",
				"-o", "StrictHostKeyChecking=accept-new",
				"-i", "/id",
				"dev@192.168.64.5:/var/log/syslog", "syslog",
			},
		},
		{
			name: "recursive to guest home",
			user: "vee", host: "127.0.0.1", port: 2202,
			spec: CopySpec{GuestPath: "", HostPath: "/tmp/dir", ToGuest: true, Recursive: true},
			want: []string{
				"-o", "BatchMode=yes",
				"-o", "UserKnownHostsFile=/kh",
				"-o", "StrictHostKeyChecking=accept-new",
				"-P", "2202", "-i", "/id", "-r",
				"/tmp/dir", "vee@127.0.0.1:",
			},
		},
		{
			name: "windows guest path normalized to forward slashes",
			user: "vee", host: "127.0.0.1", port: 2203,
			spec:         CopySpec{GuestPath: `C:\Users\vee\out.txt`, HostPath: "out.txt"},
			windowsGuest: true,
			want: []string{
				"-o", "BatchMode=yes",
				"-o", "UserKnownHostsFile=/kh",
				"-o", "StrictHostKeyChecking=accept-new",
				"-P", "2203", "-i", "/id",
				"vee@127.0.0.1:C:/Users/vee/out.txt", "out.txt",
			},
		},
		{
			name: "local relative path with colon gets ./ prefix",
			user: "dev", host: "127.0.0.1", port: 22,
			spec: CopySpec{GuestPath: "/x", HostPath: "has:colon", ToGuest: true},
			want: []string{
				"-o", "BatchMode=yes",
				"-o", "UserKnownHostsFile=/kh",
				"-o", "StrictHostKeyChecking=accept-new",
				"-i", "/id",
				"./has:colon", "dev@127.0.0.1:/x",
			},
		},
		{
			name: "empty user omits at-sign",
			user: "", host: "127.0.0.1", port: 22,
			spec: CopySpec{GuestPath: "/x", HostPath: "x"},
			want: []string{
				"-o", "BatchMode=yes",
				"-o", "UserKnownHostsFile=/kh",
				"-o", "StrictHostKeyChecking=accept-new",
				"-i", "/id",
				"127.0.0.1:/x", "x",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildScpArgs(tt.user, tt.host, tt.port, "/id", "/kh", tt.spec, tt.windowsGuest)
			if !slices.Equal(got, tt.want) {
				t.Errorf("buildScpArgs() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSplitSSHHostPort(t *testing.T) {
	tests := []struct {
		in       string
		wantHost string
		wantPort int
		wantErr  bool
	}{
		{in: "host", wantHost: "host", wantPort: 22},
		{in: "host:2201", wantHost: "host", wantPort: 2201},
		{in: "[::1]:2201", wantHost: "::1", wantPort: 2201},
		{in: "host:bad", wantErr: true},
		{in: "host:0", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			host, port, err := splitSSHHostPort(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("splitSSHHostPort(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if err == nil && (host != tt.wantHost || port != tt.wantPort) {
				t.Errorf("splitSSHHostPort(%q) = (%q, %d), want (%q, %d)", tt.in, host, port, tt.wantHost, tt.wantPort)
			}
		})
	}
}
