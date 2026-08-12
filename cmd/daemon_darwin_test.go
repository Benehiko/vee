//go:build darwin

package cmd

import (
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestLaunchdPlistRenders(t *testing.T) {
	got, err := launchdPlist("/usr/local/bin/vee", "alice", "staff", "/Users/alice")
	if err != nil {
		t.Fatalf("launchdPlist: %v", err)
	}

	for _, want := range []string{
		"<string>" + launchdLabel + "</string>",
		"<string>/usr/local/bin/vee</string>",
		"<string>daemon</string>",
		"<key>UserName</key>",
		"<string>alice</string>",
		"<key>GroupName</key>",
		"<string>staff</string>",
		"<string>/Users/alice/.vee/logs/daemon.log</string>",
		// The graceful-stop budget at host shutdown; must comfortably
		// exceed the daemon's ~90s stop batch.
		"<key>ExitTimeOut</key>",
		"<integer>300</integer>",
		// Never let launchd SIGKILL the VM processes with the daemon.
		"<key>AbandonProcessGroup</key>",
		// Restart-on-failure semantics.
		"<key>KeepAlive</key>",
		"<key>SuccessfulExit</key>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plist missing %q\nrendered:\n%s", want, got)
		}
	}
}

func TestLaunchdPlistWellFormedXML(t *testing.T) {
	got, err := launchdPlist("/usr/local/bin/vee", "alice", "staff", "/Users/alice")
	if err != nil {
		t.Fatalf("launchdPlist: %v", err)
	}
	dec := xml.NewDecoder(strings.NewReader(got))
	for {
		if _, err := dec.Token(); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			t.Fatalf("plist is not well-formed XML: %v", err)
		}
	}
}
