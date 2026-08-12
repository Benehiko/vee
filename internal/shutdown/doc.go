// Package shutdown integrates the vee daemon with the host's shutdown
// machinery so running VMs can be gracefully powered off before the host
// goes down.
//
// Every platform exposes the same surface — Connect returning a *Conn with
// Acquire/Release, PrepareForShutdown, PreparingForShutdown, and Close —
// with platform-appropriate semantics:
//
//   - Linux (inhibitor_linux.go): the systemd-logind D-Bus API. A "block"
//     inhibitor on shutdown:sleep while VMs run, and the
//     PrepareForShutdown(true) signal announcing host shutdown.
//   - macOS (inhibitor_darwin.go): launchd's contract. Shutdown arrives as
//     SIGTERM with no pre-notification and no inhibitor API — the launchd
//     job's ExitTimeOut is the graceful-stop budget — and a caffeinate
//     assertion stands in for the sleep half of the Linux inhibitor.
//   - Everything else (inhibitor_other.go): no integration. Connect fails
//     and the daemon runs without host-shutdown handling.
package shutdown
