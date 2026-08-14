// Command vee-vz-helper hosts a single guest VM — macOS (issue #51) or Linux
// (issue #127) — on Apple's Virtualization.framework on behalf of vee.
//
// A VZVirtualMachine lives inside the process that creates it, so vee spawns
// one detached helper per VM (the analog of a qemu-system process). The
// helper reads the machine spec from <vm-dir>/vz-machine.json, starts the VM,
// and serves a newline-delimited JSON control protocol on
// <vm-dir>/vz-control.sock (status / stop / wait-shutdown / version /
// vsock-connect / vsock-listen — the QMP analog). When the VM stops it
// records whether the guest initiated the shutdown in <vm-dir>/vz-result.json
// and exits.
//
// The binary must be codesigned with the com.apple.security.virtualization
// entitlement (ad-hoc is sufficient): `make vz-helper`.
package main

import (
	"fmt"
	"os"

	"github.com/Benehiko/vee/internal/buildinfo"
	"github.com/Benehiko/vee/internal/vzhelper"
)

// Build-time overrides, injected by `make vz-helper` and the release workflow
// via -ldflags "-X main.version=...". The helper is installed separately from
// vee and can drift from it, so it has to be able to identify itself.
var (
	version = ""
	commit  = ""
	date    = ""
)

func main() {
	// Handled before run() so it works on every build, including the stub that
	// refuses to host VMs on unsupported hosts — "which helper is this?" is
	// exactly the question someone on the wrong host needs answered.
	if len(os.Args) == 2 && (os.Args[1] == "--version" || os.Args[1] == "-version") {
		v, c, d := buildinfo.Resolve(version, commit, date)
		fmt.Printf("%s (commit %s, built %s)\n", v, c, d)
		return
	}
	// vee probes this BEFORE starting a VM whose spec depends on a newer
	// helper (e.g. recovery, issue #134) — a helper that predates a spec field
	// would ignore it and silently boot the guest normally. Helpers that
	// predate the flag fail to parse it, which vee reads as "too old".
	if len(os.Args) == 2 && (os.Args[1] == "--print-protocol" || os.Args[1] == "-print-protocol") {
		fmt.Println(vzhelper.ProtocolVersion)
		return
	}
	os.Exit(run())
}
