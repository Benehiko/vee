---
title: vee pull
weight: 15
---

Download (or build) a base image into the local image cache so it can be reused
by VMs. `vee create` pulls the image it needs automatically, so `vee pull` is only
needed when you want to pre-fetch an image ahead of time. If the image is already
cached, `pull` is a no-op.

Images are cached under `~/.vee/iso/`.

```
vee pull <distro> [version] [flags]
```

## Arguments

Version may be a specific version string or `latest` (the default). The distro and
`distro-version` forms both shell-complete from the built-in list.

```sh
vee pull ubuntu            # newest known Ubuntu
vee pull ubuntu 22.04      # a specific version
vee pull ubuntu-24.04      # same, as a single token
vee pull windows win10     # build the Windows 10 ISO
vee pull --list            # list every supported distro and version
```

## Flags

| Flag | Description |
|------|-------------|
| `--list` | List all supported distros and versions instead of pulling |

## Supported images

| Distro | Notes |
|--------|-------|
| `ubuntu` | Cloud image (cloud-init ready) — 24.04, 22.04, 20.04 |
| `arch` | Bootstrap image |
| `fedora` | Cloud image — 42, 41, 40 |
| `alpine` | Cloud image |
| `bazzite` | Fedora Atomic gaming ISO |
| `truenas` | TrueNAS SCALE installer ISO |
| `windows` | Built on demand — `win11`, `win10`, `server2025`, `server2022` |

Run `vee pull --list` to see the exact version strings supported by your build.

## Windows ISOs

vee builds Windows install ISOs on demand — there is no manual ISO download step.
It resolves the newest build through the [UUP dump](https://uupdump.net/) API,
downloads the ESD packages directly from Microsoft's servers, and assembles a
bootable UEFI ISO inside a throwaway container using `wimlib` and `xorriso`.

Building a Windows ISO requires `nerdctl` or `docker` on `PATH`; no ISO-assembly
tooling is installed on the host.

{{< hint type=note >}}
vee downloads Windows bits from Microsoft's own servers and assembles the ISO
locally — it never redistributes Windows. You still need a valid Windows license
key to activate the guest.
{{< /hint >}}
