# GitHub Actions Runner

The `github-runner` template provisions a VM that registers itself as a
self-hosted GitHub Actions runner. The VM uses user-mode NAT networking and
reaches GitHub over outbound HTTPS long-polling, so no inbound port forwarding
is required.

```sh
vee create ci-runner-1 --template github-runner \
  --runner-url https://github.com/owner/repo \
  --runner-labels self-hosted,linux,kvm
```

`--runner-url` accepts either a repository or an organisation URL. A short-lived
registration token is collected interactively and injected via cloud-init; it is
never written to the on-disk VM config.

## Credential persistence (reinstall without a new token)

A registered runner stores long-lived credentials inside the VM at
`/opt/actions-runner/.credentials`, `.credentials_rsaparams` and `.runner`.
These survive a VM restart but are lost when the disk is destroyed — for example
by `vee create --reinstall <name>`.

vee persists an encrypted copy on the host so a recreated runner can rejoin
GitHub as the same runner, with no new registration token and no orphaned runner
entry:

- After a fresh `vee create` of a runner, vee waits for registration to finish,
  pulls the three credential files over SSH, encrypts them with
  [age](https://age-encryption.org), and writes the archive to
  `~/.vee/runner-creds/<name>.age`.
- The age identity lives at `~/.vee/age/identity.txt` (generated on first use,
  `0600`). It is the only key that can decrypt the snapshots; back it up if you
  want runner credentials to survive a host rebuild.
- On `vee create --reinstall <name>` (or any `vee create` reusing the name), if a
  snapshot exists vee decrypts it, injects the files into the new VM, and skips
  `config.sh` registration entirely — so it never prompts for a token.

If the automatic post-create snapshot does not complete (registration can take a
few minutes via cloud-init), capture it manually once the runner is online:

```sh
vee runner snapshot <name>
```

The snapshot lives outside the VM storage directory, so `vee create --reinstall`
(which clears the VM dir) does not delete it.

## Automatic disk garbage collection

CI jobs that build images and run compose stacks accumulate containers, images,
volumes and BuildKit cache. Left unchecked these fill the runner disk to 100%,
at which point the `actions-runner` service can no longer write its working
files, crash-loops, and GitHub marks the runner **offline**.

Every runner ships `vee-runner-gc.sh` driven by a `vee-runner-gc.timer`
(`OnCalendar=daily`, `Persistent=true`). On each run it:

- skips entirely if a job is in progress (checks for a live `Runner.Worker`), so
  it never prunes an in-flight build;
- runs `nerdctl system/volume/builder prune -af`;
- prunes the BuildKit cache down to a 2 GB ceiling (keeping warm layers);
- trims the Go build cache and stale `_diag` logs / `_work/_temp` leftovers.

It derives the rootless socket env (`XDG_RUNTIME_DIR`, `CONTAINERD_ADDRESS`,
`BUILDKIT_HOST`) from its own UID rather than reading the root-owned
`runner.env`, since it runs as the unprivileged `runner` user.

Trigger a prune immediately with:

```sh
vee ssh <name> -- sudo systemctl start vee-runner-gc.service
```

## Rootless container stack

Every runner ships a fully rootless container stack so CI jobs can build and run
container images without root and without sharing a daemon socket from the host:

| Component   | Role                                          |
|-------------|-----------------------------------------------|
| containerd  | Container runtime (user-scoped, RootlessKit)  |
| BuildKit    | Image builder, used by `nerdctl build`        |
| nerdctl     | Docker-compatible CLI                         |

The stack is installed from the pinned **nerdctl "full"** release tarball, which
bundles containerd, BuildKit, nerdctl, RootlessKit, slirp4netns and the CNI
plugins as a single reproducible artifact. The pinned version lives in
`internal/templates/runner.go` (`nerdctlFullVersion`); bump it deliberately.

### How it runs

- A normal login user `runner` (UID 1001) owns the stack — not a `--system`
  account, because rootless containerd needs a home directory and a `systemd
  --user` instance.
- Subordinate UID/GID ranges are allocated for user namespaces.
- `loginctl enable-linger` keeps the user services alive with no active login.
- containerd and BuildKit run as `systemd --user` services and start on boot.
- An AppArmor profile is installed for `/usr/local/bin/rootlesskit`, because
  Ubuntu 24.04 sets `kernel.apparmor_restrict_unprivileged_userns=1` and would
  otherwise block the unprivileged user namespaces RootlessKit needs.

### Using it in CI jobs

The runner environment exports the socket locations, so `nerdctl` works out of
the box inside workflow steps:

```yaml
jobs:
  build:
    runs-on: [self-hosted, linux, kvm]
    steps:
      - uses: actions/checkout@v4
      - run: nerdctl build -t myapp:ci .
      - run: nerdctl run --rm myapp:ci ./run-tests.sh
```

Relevant environment variables (set in `/etc/actions-runner/runner.env`):

| Variable             | Value                                              |
|----------------------|----------------------------------------------------|
| `CONTAINERD_ADDRESS` | `/run/user/1001/containerd/containerd.sock`        |
| `BUILDKIT_HOST`      | `unix:///run/user/1001/buildkit/buildkitd.sock`    |
| `XDG_RUNTIME_DIR`    | `/run/user/1001`                                   |

There is no Docker daemon and no `DOCKER_HOST`; use the `nerdctl` CLI, which is
command-line compatible with `docker`.

## Host build tools

A host toolchain is installed alongside the container stack so CI jobs that
compile directly on the runner (rather than inside a container) have what they
need — in particular Go builds with `CGO_ENABLED=1`:

- `build-essential` — `gcc`, `g++`, `make` and `libc6-dev` (C/C++ compilers,
  Make, and the C standard library headers).
- `pkg-config` — resolves `#cgo pkg-config:` directives in cgo packages
  (sqlite3, image libraries, and other C-library wrappers).

The runner itself does not ship a Go toolchain; CI workflows install Go with
`actions/setup-go`, which picks up `gcc` from `PATH` for cgo.

These are installed as cloud-init `Packages` in `internal/templates/runner.go`.

## Notes

- The runner is also added to the `kvm` group so nested e2e tests can use KVM
  acceleration.
- Only outbound HTTPS and inbound SSH (for `vee ssh`) are open; `ufw` blocks the
  rest.
