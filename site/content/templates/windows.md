---
title: windows
weight: 80
---

Windows VM with UEFI Secure Boot and TPM 2.0 emulation via `swtpm`.

## Prerequisites

- `swtpm` installed on the host
- Windows ISO (not included — supply your own)

## Create

```sh
vee create mywindows --template windows
```

## Defaults

| Setting | Value |
|---------|-------|
| Memory | 8G |
| CPUs | 4 |
| UEFI | Yes (Secure Boot) |
| TPM | 2.0 (swtpm) |
| Display | SPICE |

## Notes

- Use `vee view mywindows` to open the SPICE console during Windows setup.
- `swtpm` is started automatically when the VM boots and stopped when it shuts down.
