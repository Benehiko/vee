---
title: Quick Start
weight: 20
---

Create and connect to your first VM in under two minutes.

## Create a VM

```sh
vee create myvm
```

This creates an Ubuntu 24.04 LTS server VM using the default `ubuntu-server` template. vee downloads the cloud image on first use and caches it.

## Start the VM

```sh
vee start myvm
```

vee boots the VM in the background and waits until it is ready to accept SSH connections. Progress is shown inline.

## SSH in

```sh
vee ssh myvm
```

vee uses the keypair it generated at create time — no password needed.

## Check status

```sh
vee status myvm
```

Shows the VM's IP addresses, hostname, OS, and uptime (requires `qemu-guest-agent` installed inside the guest).

## List all VMs

```sh
vee list
```

## Stop the VM

```sh
vee stop myvm        # graceful ACPI shutdown
```

## Delete the VM

```sh
vee delete myvm      # removes the VM and its disk images
```

## Next steps

- Browse the [Templates]({{< relref "/templates/" >}}) to see what VM types are available.
- Learn about [GPU passthrough]({{< relref "/gpu-passthrough/" >}}) for gaming VMs.
- See the full [Command Reference]({{< relref "/commands/" >}}).
