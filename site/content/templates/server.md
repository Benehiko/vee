---
title: server
weight: 30
---

A hardened Linux server VM. Installs openssh, ufw, and fail2ban via cloud-init.

## Create

```sh
vee create myserver --template server
```

## Included software

- openssh-server
- ufw (firewall, SSH allowed by default)
- fail2ban

## Notes

Supports `--distro` to switch the base distro.
