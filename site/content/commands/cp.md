---
title: vee cp
weight: 52
---

Copy files between the host and a running VM.

```
vee cp [-r] <src> <dst>
```

Exactly one side names the guest, as `<vm>:<path>`. An empty guest path (`<vm>:`) means the login user's home directory. The copy runs over scp with the vee SSH key, so it works on every guest vee can `ssh` into — including Windows guests, whose paths keep their drive letter (only the first colon separates the VM name).

```sh
vee cp ./e2e.test myvm:/home/dev/e2e.test   # host → guest
vee cp myvm:/var/log/syslog ./syslog        # guest → host
vee cp -r ./testdata myvm:                  # directory into the guest home
vee cp winvm:C:\Users\vee\out.txt .         # Windows guest path
```

The username defaults to the cloud-init user configured at creation time; override with `--user`. A local path containing a colon needs a `./` prefix so it is not read as `vm:path`.
