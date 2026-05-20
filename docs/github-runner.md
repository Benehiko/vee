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

## Notes

- The runner is also added to the `kvm` group so nested e2e tests can use KVM
  acceleration.
- Only outbound HTTPS and inbound SSH (for `vee ssh`) are open; `ufw` blocks the
  rest.
