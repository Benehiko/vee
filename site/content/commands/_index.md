---
title: Command Reference
weight: 20
---

Complete reference for all `vee` commands.

## VM lifecycle

| Command | Description |
|---------|-------------|
| [`vee create`]({{< relref "create" >}}) | Create a new VM |
| [`vee start`]({{< relref "start" >}}) | Start a VM |
| [`vee stop`]({{< relref "stop" >}}) | Stop a running VM |
| [`vee delete`]({{< relref "delete" >}}) | Delete a VM and its disks |
| [`vee autostart`]({{< relref "autostart" >}}) | Enable or disable autostart for a VM |
| [`vee config`]({{< relref "config" >}}) | Edit a VM's configuration in a TUI |

## Inspect & monitor

| Command | Description |
|---------|-------------|
| [`vee list`]({{< relref "list" >}}) | List all VMs and their status |
| [`vee status`]({{< relref "status" >}}) | Show detailed VM status and guest info |
| [`vee check`]({{< relref "check" >}}) | Run health checks on an installed VM |
| [`vee ip`]({{< relref "ip" >}}) | Print the VM's IP address |
| [`vee ports`]({{< relref "ports" >}}) | List bound TCP ports inside a running VM |
| [`vee logs`]({{< relref "logs" >}}) | Stream QEMU output |
| [`vee monitor`]({{< relref "monitor" >}}) | Real-time CPU/memory/disk/network stats |
| [`vee dashboard`]({{< relref "dashboard" >}}) | Start a web dashboard for all VMs |

## Access

| Command | Description |
|---------|-------------|
| [`vee ssh`]({{< relref "ssh" >}}) | Open an SSH session |
| [`vee ssh-share`]({{< relref "ssh-share" >}}) | Share the host SSH agent into a VM via AF_VSOCK |
| [`vee tunnel`]({{< relref "tunnel" >}}) | Forward a VM port to localhost via SSH |
| [`vee view`]({{< relref "view" >}}) | Open the VM display (SPICE or GPU) |
| [`vee backup`]({{< relref "backup" >}}) | Back up guest directories to the host |

## Images

| Command | Description |
|---------|-------------|
| [`vee pull`]({{< relref "pull" >}}) | Download or build a base image into the cache |
| [`vee mirror`]({{< relref "mirror" >}}) | Host-side pacman caching proxy for Arch VMs |

## Advanced / infrastructure

| Command | Description |
|---------|-------------|
| [`vee gpu`]({{< relref "gpu" >}}) | Manage GPU passthrough bindings |
| [`vee qmp`]({{< relref "qmp" >}}) | Send a QMP command to a running VM |
| [`vee daemon`]({{< relref "daemon" >}}) | Run the vee daemon (autostart supervision) |
| [`vee runner`]({{< relref "runner" >}}) | Manage self-hosted GitHub Actions runner VMs |
| [`vee version`]({{< relref "version" >}}) | Print version, commit, and build date |

## Shell completion

vee ships Cobra-generated completions:

```sh
source <(vee completion bash)   # bash
source <(vee completion zsh)    # zsh
vee completion fish | source    # fish
```
