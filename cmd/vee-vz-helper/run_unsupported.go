//go:build !(darwin && arm64 && cgo)

package main

import (
	"fmt"
	"os"
)

// run refuses on hosts without Virtualization.framework macOS-guest support.
// The stub keeps the package buildable on every platform vee targets, and
// under CGO_ENABLED=0 darwin builds (the vz bindings require cgo — build the
// real helper with `make vz-helper`).
func run() int {
	fmt.Fprintln(os.Stderr, "vee-vz-helper: this build lacks Virtualization.framework support; macOS guests require an Apple Silicon macOS host and a cgo build (`make vz-helper`)")
	return 1
}
