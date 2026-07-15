---
title: vee runner
weight: 155
---

Manage self-hosted GitHub Actions runner VMs (the [`github-runner` template]({{< relref "/templates/github-runner" >}})). Runner registration credentials can be persisted to the host, age-encrypted, so `vee create --reinstall <name>` rejoins GitHub as the same runner.

```
vee runner <subcommand>
```

## Subcommands

### vee runner key

Print a runner's GitHub SSH public key so you can add it to GitHub.

```sh
# Shared GLOBAL key injected into every fresh runner (generated on first use)
vee runner key

# A specific runner's PER-INSTANCE key (created at `vee create --runner-ssh-key` time)
vee runner key myrunner
```

The public key goes to stdout and guidance goes to stderr, so `vee runner key >> ~/keys` captures just the key.

### vee runner snapshot

Persist a running runner's credentials to the host, age-encrypted, at `~/.vee/runner-creds/<name>.age`. Run this if the automatic snapshot during `vee create` did not complete.

```sh
vee runner snapshot myrunner
```

The VM must be a `github-runner` template VM that is running with an SSH port.

See [docs/github-runner.md](https://github.com/Benehiko/vee/blob/main/docs/github-runner.md) for credential persistence, SSH keys, and disk GC.
