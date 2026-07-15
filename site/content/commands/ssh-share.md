---
title: vee ssh-share
weight: 55
---

Share the host SSH agent into a running VM over `AF_VSOCK`, so the guest can use your host keys without copying them into the VM.

```
vee ssh-share <name>
```

Starts a vsock proxy on port `2222` that forwards guest connections to the host SSH agent socket. The command runs until you press Ctrl-C.

## Prerequisites

- The VM must have been created with `--ssh-share`.
- The VM must be running.
- `vsock` is Linux-only — this command is unavailable on macOS and Windows hosts.

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--agent-sock` | `$SSH_AUTH_SOCK` | SSH agent socket path on the host |

## Example

```sh
# Create a VM with agent sharing enabled
vee create devbox --template devbox --ssh-share

# In another terminal, bridge the host agent into the guest
vee ssh-share devbox

# Inside the guest, host keys are now available
vee ssh devbox -- ssh-add -l
```
