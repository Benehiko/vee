---
title: devbox
weight: 20
---

A developer workstation VM. Installs Docker Engine and zsh via cloud-init on first boot.

## Create

```sh
vee create dev --template devbox
vee create dev --template devbox --distro ubuntu  # default
```

## Included software

- Docker Engine (latest)
- zsh + Oh My Zsh
- Common dev tools: git, curl, jq, vim

## Notes

- Supports `--distro` to switch the base Linux distro (where images are available).
- Supports `--virtiofs-dir` to share your home directory or project folder into the VM.
