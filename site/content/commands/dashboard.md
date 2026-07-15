---
title: vee dashboard
weight: 145
---

Start a web dashboard serving a live HTML view and JSON API for all VMs. VM state is polled every 2 seconds.

```
vee dashboard
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--addr` | `127.0.0.1:7777` | Address to listen on |

## Examples

```sh
# Default: http://127.0.0.1:7777
vee dashboard

# Expose on the LAN
vee dashboard --addr 0.0.0.0:7777
```

> The dashboard has no built-in authentication. Bind it to `127.0.0.1` (the default), or put it behind a reverse proxy with auth before exposing it beyond localhost.
