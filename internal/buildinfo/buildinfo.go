// Package buildinfo resolves a binary's build identity.
//
// It exists because vee ships two binaries — the CLI and vee-vz-helper, which
// hosts macOS guests — and both need to report the same identity in the same
// way. The helper is installed separately from the CLI and can drift from it, so
// "which build is this?" has to be answerable for each of them independently.
package buildinfo

import "runtime/debug"

// Resolve returns the version, commit and date to report, preferring values
// injected at link time and falling back to the module's own build info.
//
// Release builds pass all three via -ldflags. A plain `go build` or `go install`
// passes none, and would otherwise report nothing useful — so the VCS stamps Go
// records in the binary are used instead, including the dirty marker, which is
// exactly what a bug report from a local build needs to say.
func Resolve(version, commit, date string) (v, c, d string) {
	v, c, d = version, commit, date

	info, ok := debug.ReadBuildInfo()
	if !ok {
		return orUnknown(v), orUnknown(c), orUnknown(d)
	}

	if v == "" {
		v = info.Main.Version
		if v == "" || v == "(devel)" {
			v = "dev"
		}
	}
	if c == "" || d == "" {
		var modified bool
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				if c == "" {
					c = s.Value
				}
			case "vcs.time":
				if d == "" {
					d = s.Value
				}
			case "vcs.modified":
				modified = s.Value == "true"
			}
		}
		// A build from a dirty tree is not the commit it claims to be.
		if modified && c != "" {
			c += "-dirty"
		}
	}
	return v, orUnknown(c), orUnknown(d)
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
