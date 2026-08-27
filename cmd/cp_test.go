package cmd

import "testing"

func TestParseCpArg(t *testing.T) {
	tests := []struct {
		name        string
		arg         string
		windowsHost bool
		wantVM      string
		wantPath    string
		wantRemote  bool
	}{
		{name: "bare local", arg: "file.txt", wantPath: "file.txt"},
		{name: "local dir", arg: "./testdata", wantPath: "./testdata"},
		{name: "remote path", arg: "myvm:/home/dev/x", wantVM: "myvm", wantPath: "/home/dev/x", wantRemote: true},
		{name: "remote home", arg: "myvm:", wantVM: "myvm", wantPath: "", wantRemote: true},
		{name: "remote relative", arg: "myvm:pinata/e2e.test", wantVM: "myvm", wantPath: "pinata/e2e.test", wantRemote: true},
		{name: "windows guest drive letter", arg: `winvm:C:\Users\vee\out.txt`, wantVM: "winvm", wantPath: `C:\Users\vee\out.txt`, wantRemote: true},
		{name: "separator before colon is local", arg: "./has:colon", wantPath: "./has:colon"},
		{name: "abs path with colon is local", arg: "/tmp/has:colon", wantPath: "/tmp/has:colon"},
		{name: "drive path on windows host", arg: `C:\Users\me\f`, windowsHost: true, wantPath: `C:\Users\me\f`},
		{name: "drive path forward slashes on windows host", arg: "C:/Users/me/f", windowsHost: true, wantPath: "C:/Users/me/f"},
		{name: "single-letter vm on unix host", arg: "C:/Users/me/f", wantVM: "C", wantPath: "/Users/me/f", wantRemote: true},
		{name: "backslash before colon on windows host", arg: `.\odd:name`, windowsHost: true, wantPath: `.\odd:name`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vmName, path, remote := parseCpArg(tt.arg, tt.windowsHost)
			if vmName != tt.wantVM || path != tt.wantPath || remote != tt.wantRemote {
				t.Errorf("parseCpArg(%q, windowsHost=%v) = (%q, %q, %v), want (%q, %q, %v)",
					tt.arg, tt.windowsHost, vmName, path, remote, tt.wantVM, tt.wantPath, tt.wantRemote)
			}
		})
	}
}
