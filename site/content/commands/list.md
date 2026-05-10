---
title: vee list
weight: 40
---

List all VMs and their current status.

```
vee list
```

## Output columns

| Column | Description |
|--------|-------------|
| `NAME` | VM name |
| `TEMPLATE` | Template the VM was created from |
| `MEMORY` | Configured RAM |
| `CPUs` | Number of virtual CPUs |
| `STATUS` | `running` or `stopped` |
| `PID` | QEMU process ID (running VMs only) |
| `SPICE` | SPICE display port (if configured) |

## Example

```
NAME           TEMPLATE       MEMORY  CPUs  STATUS   PID     SPICE
linux-gaming   passthrough    16G     6     running  12345   5930
myvm           ubuntu-server  2G      2     stopped  -       -
```
