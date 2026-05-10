---
title: vee version
weight: 130
---

Print version information for the `vee` binary.

```
vee version [--short]
```

Default output includes the version string, git commit, build timestamp, Go runtime, and target OS/arch:

```
$ vee version
vee v0.4.0
  commit: be9ed1a
  built:  2026-05-10T19:37:04Z
  go:     go1.26.1
  os:     linux/amd64
```

`--short` prints only the version string — handy in scripts:

```sh
vee version --short
```

When built via `make build` or a release pipeline, the `version`, `commit`, and `date` values are injected through `-ldflags`. Plain `go install github.com/Benehiko/vee@latest` builds fall back to module info reported by `runtime/debug.ReadBuildInfo`, so the binary still reports a meaningful identity.
