// Command vee-vz-helper hosts a single macOS guest VM on Apple's
// Virtualization.framework on behalf of vee (issue #51).
//
// A VZVirtualMachine lives inside the process that creates it, so vee spawns
// one detached helper per VM (the analog of a qemu-system process). The
// helper reads the machine spec from <vm-dir>/vz-machine.json, starts the VM,
// and serves a newline-delimited JSON control protocol on
// <vm-dir>/vz-control.sock (status / stop / wait-shutdown — the QMP analog).
// When the VM stops it records whether the guest initiated the shutdown in
// <vm-dir>/vz-result.json and exits.
//
// The binary must be codesigned with the com.apple.security.virtualization
// entitlement (ad-hoc is sufficient): `make vz-helper`.
package main

import "os"

func main() {
	os.Exit(run())
}
