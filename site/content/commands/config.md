---
title: vee config
weight: 130
---

Edit an existing VM's configuration in an interactive TUI form and save it back to `vm.yaml`.

```
vee config [name]
```

- With a name, the editor opens immediately for that VM.
- Without a name, the VM list opens so you can navigate to the VM you want to edit.

Changes take effect the next time the VM starts. You can also edit `~/.vee/vms/<name>/vm.yaml` by hand — see [vm.yaml]({{< relref "/advanced/vm-yaml" >}}).

## Examples

```sh
# Edit a specific VM
vee config myvm

# Pick a VM from the list
vee config
```
