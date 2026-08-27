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
- `--distro omarchy` delegates to the [`omarchy`]({{< relref "omarchy" >}}) template: an unattended install of Omarchy's Arch + Hyprland desktop from its own ISO, with the devbox's `dev` user carried over. Omarchy has no cloud-init, but its stock install already includes Docker, lazydocker, and git.
- Supports `--virtiofs-dir` to share your home directory or project folder into the VM.
