# Deploy

Pick one way to run ndb. All of them run the same binary with the same rules.

| Way | Use it when | Page |
|---|---|---|
| Linux (systemd) | a plain VM or server; the simplest setup | [Linux](deploy/linux.md) |
| Docker | one container from `ghcr.io/thadeu/ndb` | [Docker](deploy/docker.md) |
| Single VM | one VM, several apps in containers that share the unix socket | [Single VM](deploy/single-vm.md) |
| Cross-VM | apps on other VMs, over TCP inside WireGuard, with a token | [Cross-VM](deploy/cross-vm.md) |
| Voodu | the Single VM layout as voodu manifests | [Voodu](deploy/voodu.md) |

After it runs, the day-to-day work (health, metrics, backup, recovery, upgrade) is in [Operations](operations.md).

## Rules for every setup

- `data_dir` is on a local disk, never on NFS, EFS or another network volume: `flock` and `fsync` are not reliable there.
- One ndb process owns the data dir. A second one fails on the lock. Never run 2 replicas.
- Stop with SIGTERM and wait at least 30 s, so running requests finish.
- Unix socket for apps on the same host: share the socket dir and a group (the socket is `0660` by default).
- TCP only on a private address (for example WireGuard), always with `-token-file`. The apps send the token with `NDB_TOKEN`.
- Direct mode only for files on the same machine as ndb.
