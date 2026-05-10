# Host-side pacman caching proxy

Vee can run a host-local pacman caching proxy ([pacoloco](https://github.com/anatol/pacoloco)) so Arch VMs do not re-download identical packages on every fresh create. The proxy is optional, off by default unless the user starts it, and only affects Arch guests that reach it through QEMU user-mode networking.

## When this helps

If you spin up Arch `devbox` or `server` VMs frequently — each one's first boot pulls `base-devel`, `neovim`, `openssh`, `docker`, … from `geo.mirror.pkgbuild.com`. Multiply by N VMs and you hit the upstream mirror N times for identical bytes.

With the cache enabled, the first VM populates a host directory; every subsequent VM reads from disk over a `10.0.2.2:9129` HTTP connection. Pacman's signature verification is unaffected — pacoloco proxies the original signed `.pkg.tar.zst` files byte-for-byte.

## Usage

```sh
# One-time: install the binary + systemd user unit, then start it
vee mirror start

# Inspect
vee mirror status

# Stop (does not delete the cache)
vee mirror stop

# Wipe the on-disk cache (e.g. before a long absence)
vee mirror purge
```

Once `vee mirror start` has been run, any subsequent `vee create … --template devbox --distro arch` (or `--template server --distro arch`) automatically points `/etc/pacman.d/mirrorlist` at the host cache. The upstream geo-mirror is appended as a fallback, so a stopped or broken proxy never breaks the install.

## Flags

The global `--mirror` flag selects the resolution mode:

| Value  | Behaviour                                                          |
| ------ | ------------------------------------------------------------------ |
| `auto` | Default. Use the cache if `vee-pacoloco.service` is currently active. |
| `on`   | Force-use the cache even if the unit is inactive (guests retry).   |
| `off`  | Never wire the cache in. Useful when reproducing a mirror failure. |

Examples:

```sh
vee --mirror=off create dev-no-cache --template devbox --distro arch
vee --mirror=on  create dev-cached    --template devbox --distro arch
```

The default mode can also be set persistently in `~/.vee/config.yaml`:

```yaml
mirror_mode: auto         # auto | on | off
mirror_address: 10.0.2.2:9129
```

## Networking caveat — bridge mode

The address `10.0.2.2` is the QEMU user-mode NAT gateway. VMs on a real bridge (e.g. `br0`) cannot reach it. Vee detects bridge mode and skips injection with an informational log line.

If you want the cache to work for bridged VMs, set `mirror_address` in `~/.vee/config.yaml` to a routable host IP (for example, the host's address on the bridge). pacoloco still listens on `127.0.0.1:9129` for safety — you would need to either run a reverse proxy or change pacoloco's listener separately.

## What lives where

| Path                                       | Purpose                          |
| ------------------------------------------ | -------------------------------- |
| `~/.vee/bin/pacoloco`                      | Downloaded pacoloco binary       |
| `~/.config/vee/mirror/pacoloco.yaml`       | Generated pacoloco config        |
| `~/.config/systemd/user/vee-pacoloco.service` | Systemd --user unit             |
| `~/.cache/vee/mirror/pacoloco/`            | Cached `.pkg.tar.zst` files      |

Cached packages are aged out automatically after 30 days. There is no upper limit on cache size — `vee mirror status` reports the current footprint, and `vee mirror purge` clears it on demand.

## Out of scope (for now)

- **Apt / dnf**: Debian/Ubuntu and Fedora guests are unaffected this round. A follow-up will wire `apt-cacher-ng` for apt.
- **Gaming-arch template**: it provisions from the live Arch ISO via a bash installer (not cloud-init), runs on a bridge NIC by default, and additionally uses `reflector` to pick fast mirrors. Wiring pacoloco into that flow needs a coordinated change in both the live env's `pacman.conf` and the chroot's `pacman.conf`; tracked separately.
