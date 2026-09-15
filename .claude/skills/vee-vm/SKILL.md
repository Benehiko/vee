---
name: vee-vm
description: "Drive QEMU/VZ test VMs with the vee CLI: create Linux/Windows/macOS guests, wait for SSH, copy files in and out, run commands and Go tests inside, screenshot, tear down. Use whenever a task needs a VM to reproduce or test something (Linux-only test failures, Windows-specific code paths, GUI apps, nested virtualization)."
---

# Driving vee VMs for testing

`~/.vee/bin/vee` manages QEMU and Virtualization.framework VMs on an Apple Silicon Mac. Guests are arm64; cross-arch needs `--emulate`. Use the CLI via Bash. `vee <cmd> --help` is accurate; `vee mcp` also exists if wired up.

## Inventory first, teardown last

```sh
vee list    # VMs may already exist. Reuse them.
```

Reuse an existing stopped VM rather than creating a new one. Do not touch VMs another session is actively using. Delete VMs you created when done: `vee delete <name>`.

## Create

```sh
vee create mylinux --template devbox --distro ubuntu --cpus 6 --memory 8G --headless   # Ubuntu + Docker preinstalled, ~5 min first boot
vee create mywin   --template windows --cpus 6 --memory 8G                             # Win11 arm64, hardware checks bypassed; sshd ~5 min after create
vee create mydesk  --template desktop --distro ubuntu                                  # GNOME GUI guest (GDM autologin)
vee create mymac   --template macos                                                    # macOS guest (VZ; ~14 GB IPSW download, cached)
```

- Add `--nested` when the guest itself needs KVM (Docker Desktop, KubeVirt). Requires an M3 or newer host, QEMU 11.1 or newer, and macOS 15 or newer.
- Add `--vsock` for a private host-to-guest channel.
- `--virtiofs-dir` is refused on macOS hosts (virtiofsd is Linux-only). Use `vee cp`.
- Cloud-init run_cmds run once per instance. If first boot breaks provisioning, run `vee create --reinstall <name>` rather than patching by hand.

## Wait for readiness

```sh
vee wait <vm> --timeout 10m          # authenticated SSH round-trip (Windows guests too)
vee wait <vm> --cloud-init           # also waits for first-boot provisioning (Linux)
```

`vee status` can show a stuck "Kernel boot" phase while SSH already works (user-mode NIC IP resolution). Trust `vee wait`, not the phase. Fallback on an old vee build: poll `vee ssh <vm> -- true` (Linux) or `vee ssh <vm> -- ver` (Windows).

## Copy files

```sh
vee cp ./local.bin <vm>:dest.bin          # host to guest ("<vm>:" = guest home)
vee cp <vm>:/var/log/syslog ./syslog      # guest to host
vee cp -r ./dir <vm>:                     # directories
vee cp winvm:C:\Users\vee\out.txt .       # Windows guest paths keep their drive letter
```

For a whole git tree, a tar pipe beats scp -r over many small files:

```sh
git archive --format=tar HEAD | vee ssh <vm> -- sh -c 'rm -rf myrepo && mkdir -p myrepo && tar -x -C myrepo'
```

Wipe the old tree first; deleted files linger otherwise. Fallbacks on an old vee build: `vee ssh <vm> -- dd of=file` (stdin redirect) for single files, or raw `scp -P <port> -i ~/.vee/ssh/id_ed25519 -o UserKnownHostsFile=~/.vee/ssh/known_hosts` (per-VM port from `~/.vee/config.yaml` or the ssh error output).

## Run commands inside

- Linux guests: the remote shell treats the whole `--` argument as one command. Wrap compound commands: `vee ssh <vm> -- sh -c 'cmd1 && cmd2'`. Use relative paths (`myrepo`), not `~/myrepo`, inside the quoted command.
- Windows guests (user `vee`, shell cmd.exe): prefix with `cmd /c`, e.g. `vee ssh winvm -- cmd /c "x.exe -test.v"`.
- Multi-line scripts: `printf '…' | vee ssh <vm> -- bash -s`.
- Backgrounded processes must fully redirect stdin, stdout and stderr, or the ssh session never returns.

## Run Go tests

Linux: containerized, with a named volume for the build cache.

```sh
vee ssh <vm> -- sh -c 'docker run --rm -v /home/dev/myrepo:/src -w /src -v gocache:/root/.cache/go-build golang:1.26 go test -count=1 -run "<Test>" ./<pkg>/'
```

Windows: cross-compile on the host, copy, run. No race detector on windows/arm64.

```sh
GOOS=windows GOARCH=arm64 go test -c -o x.exe ./pkg/
vee cp x.exe mywin:
vee ssh mywin -- cmd /c "x.exe -test.run <Test> -test.v -test.count=1 -test.timeout 180s"
```

Real Windows COM (INetworkListManager etc.) works in the VM, which suits code paths cross-compilation cannot exercise.

## GUI

- `vee screenshot <vm> shot.png` (QEMU guests); `vee view <vm>` opens the display.
- GUI automation inside Linux guests: GTK accepts `xdotool type` and XTEST synthetic input; Qt needs real pointer clicks (`xdotool mousemove --sync … click 1`).
- On QEMU builds without cocoa OpenGL, vee retries desktop-template boots with the 2D adapter and warns on stderr ("no OpenGL support"). The guest renders in software (llvmpipe), which is fine for app testing.

## Gotchas

- **SPICE is unavailable on macOS QEMU builds.** They ship without spice-server (`-spice: invalid option`), and a `spice:` block in a VM's config makes every start die instantly (vee misreads it as "install pass finished; powered off"; check `~/.vee/vms/<vm>/qemu.log`). For remote display use guest-side VNC instead: GNOME on Xorg (`WaylandEnable=false` in /etc/gdm/custom.conf plus `gnome-session-xsession` and `xorg-x11-server-Xorg` on Fedora) and `x11vnc -display :0 -auth /run/user/1000/gdm/Xauthority -localhost -rfbauth ~/.vnc/passwd` as a user unit, then `ssh -f -N -L 5900:127.0.0.1:5900 -p <ssh_port> -i ~/.vee/ssh/id_ed25519` and connect with macOS Screen Sharing (`open vnc://127.0.0.1:5900`). Bonus: the Xorg session also makes xdotool automation work.
- **Never `sudo reboot` inside a guest.** QEMU does not restart it: the VM wedges ("Display output is not active", ssh connection reset, `vee status` stuck in "Init"). Power-cycle with `vee stop <vm>` and `vee start <vm>` instead.
- **`vee wait` returns before first-boot provisioning finishes** (SSH comes up mid-cloud-init). On desktop-template guests the GUI (graphical.target/GDM) becomes default only after provisioning, so gnome-shell is not running yet. Gate on `vee wait <vm> --cloud-init`, or in-guest `sudo cloud-init status --wait` (plain `cloud-init status` needs sudo on Fedora), then power-cycle to boot into the GUI.
- Desktop-template GDM autologin sessions can sit **locked** (black screenshot, `loginctl show-session <N> -p LockedHint` = yes): `sudo loginctl unlock-session <N>`. Screenshots can still come back black after that (compositor/virtio-gpu presentation issue). Drive the app under test via its API or logs instead of pixels where possible.
- **Avoid restarting the vee daemon while VMs run.** It can treat the restart as a host shutdown and gracefully stop every running VM, corrupting one mid-install. The daemon reads `~/.vee/config.yaml` only at startup.
- Go post-quantum TLS handshakes can hang under QEMU user-mode (slirp) networking. Set `GODEBUG=tlsmlkem=0` for network-heavy Go programs in the guest.

## Windows guests: hard-won details

- **`vee ssh <win> -- …` strips backslashes from arguments.** Use forward slashes in guest paths (`powershell -File C:/Users/vee/x.ps1`); Windows APIs accept them. Anything with nested quotes (`reg query "HKLM\…"`, `findstr "Boot Time"`) breaks: put it in a `.ps1`, `vee cp` it in, run with `-File`.
- **The SSH session token is a full admin token with every privilege enabled** (SeBackup, SeRestore, SeDebug, …). ACL denies on files do not bite it (MoveFileEx/DeleteFile open with backup intent), so it does not represent an unelevated desktop app. For ACL, delete/rename or filtered-token behaviour, run as a standard user:
  1. `New-LocalUser probe …; Add-LocalGroupMember Users probe`.
  2. Grant `SeBatchLogonRight` to that user with `secedit /export`, edit `[Privilege Rights]`, `secedit /configure` (standard users lack it, and `schtasks /run` silently stays "Ready" without it).
  3. Stage binaries and scripts somewhere the user can read, e.g. `C:\probe` with `icacls … /grant "probe:(OI)(CI)(RX,W)"`; `C:\Users\vee` is private.
  4. `schtasks /create /tn t /tr "cmd /c powershell -File C:\probe\x.ps1 > C:\probe\x.log 2>&1" /sc once /st 00:00 /ru probe /rp <pw> /rl LIMITED /f`, then `schtasks /run /tn t`, poll `schtasks /query /tn t /fo csv /nh` until not `Running`, read the log.
  `Start-Process -Credential` from an SSH session does not work: the child dies with 0xC0000142 (no window station).
- `sc query`, `fsutil` and `fltmc` need admin: run them over ssh, not in the standard-user task. Confirm a `vee stop`/`vee start` cycle really rebooted with `(Get-CimInstance Win32_OperatingSystem).LastBootUpTime`.
- **`vee create --template windows` builds its unattend ISO with a host `docker run`** and prints nothing while it runs (minutes). If the host Docker Desktop API proxy hangs (containers stuck in `Created`, `docker run` never returns, while `docker ps` works), the raw daemon socket still works: `DOCKER_HOST=unix://$HOME/Library/Containers/com.docker.docker/Data/docker.raw.sock TMPDIR=$HOME/.vee/tmp vee create …`. The raw socket maps `/Users/…` bind mounts into the VM but not `/var/folders/…`, hence `TMPDIR` under home. Check progress with `pgrep -fl "docker run"` and `ls -lat ~/.vee/iso`.
- The guest user `vee` is a local Administrator; `whoami /groups` shows S-1-5-32-544 enabled over ssh.
