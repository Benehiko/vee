---
title: vee helios
weight: 48
---

Inspect and verify Windows guests created with `--gpu-mode=helios`, which run
the [WinBoat Helios](https://github.com/winboat-org/helios) vGPU driver
(Direct3D 11/12, Vulkan, OpenGL and OpenCL over virtio-gpu + Venus).
Experimental. See [the windows template](../../templates/windows/#directx-with-helios-experimental).

```
vee helios status <name> [--wait] [--timeout 30m]
vee helios verify <name> [--timeout 15m]
```

## status

Prints the guest's Helios provisioning state from
`%ProgramData%\Helios\provisioning-status.json`: `not-started`, `waiting`,
`test-signing-restart-required`, `driver-restart-required`, `finished` or
`failed`. The guest reboots itself through the restart-required states.

With `--wait` it polls until `finished` or `failed`, retrying while the
guest is rebooting, and prints each state change. It exits non-zero on
`failed` or when the timeout passes, so it can gate a test run:

```sh
vee start wintest && vee helios status wintest --wait && vee helios verify wintest
```

## verify

Runs Helios' own `Verify-Helios.ps1 -RunSmokeTests`: file and registration
checks, then Vulkan instances, D3D11 and D3D12 devices with texture readback,
WGL contexts (x64 and x86), and an OpenCL kernel. The probes refuse to run in
session 0 (where SSH lives). vee therefore runs them as an interactive
scheduled task in the guest's logged-in desktop session and prints the log.
Exits non-zero if any probe fails.

MCP equivalents: `vm_helios_status` (with `wait`) and `vm_helios_verify`.
