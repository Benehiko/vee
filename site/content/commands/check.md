---
title: vee check
weight: 120
---

Run health checks on an installed VM and print the results as a table.

```
vee check <name>
```

Output is a `CHECK / STATUS / DETAIL` table with a passed/total summary — useful after an install or a template change to confirm the guest came up as expected.

## Example

```sh
vee check myvm
```

```
CHECK              STATUS   DETAIL
ssh                pass     reachable on 192.168.122.42:22
guest-agent        pass     qemu-guest-agent responding
disk               pass     /dev/vda mounted, 18G free
3/3 checks passed
```

The exact checks depend on the template — for example, gaming VMs run a self-verifying `vee-check` script inside the guest that reports GPU driver and streaming-service health.
