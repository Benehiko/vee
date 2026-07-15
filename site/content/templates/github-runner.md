---
title: github-runner
weight: 90
---

Self-hosted GitHub Actions runner on an Ubuntu cloud image with a rootless container stack (containerd + BuildKit + nerdctl via RootlessKit). Outbound-only: the runner reaches GitHub over HTTPS long-polling, with no inbound port forwarding.

## Create

```sh
vee create ci-1 --template github-runner \
  --runner-url https://github.com/Benehiko/vee
```

You are prompted for a registration token interactively.

## Defaults

| Setting | Value |
|---------|-------|
| Memory | 4G |
| CPUs | 4 |
| Disk | 20G qcow2 (COW overlay on Ubuntu image) |
| Network | User-mode NAT (outbound only) |
| Display | None (headless) |
| UEFI | No |
| User | `admin` |

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--runner-url` | *(required)* | GitHub repo or org URL to register the runner against |
| `--runner-labels` | `self-hosted,linux,kvm` | Runner labels |
| `--runner-ssh-key` | `false` | Generate a per-instance GitHub SSH key instead of the shared global key |

## Credentials & reinstall

Runner registration credentials are persisted to the host, age-encrypted, at `~/.vee/runner-creds/<name>.age`. This lets `vee create --reinstall <name>` rejoin GitHub as the **same** runner without a fresh token. Manage keys and snapshots with [`vee runner`]({{< relref "/commands/runner" >}}).

See [docs/github-runner.md](https://github.com/Benehiko/vee/blob/main/docs/github-runner.md) for credential persistence, SSH-key rollout, and disk garbage collection.
