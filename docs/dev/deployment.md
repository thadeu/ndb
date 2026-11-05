# Deployment examples

Two complete examples are in `deploy/examples/`. Each has its own README with the diagram and the steps.

| Example | Use it when |
|---|---|
| [single-vm](../../deploy/examples/single-vm/) | One VM. One ndb and several apps (Go, Ruby, Node) share a unix socket. Compose file, direct-mode variant, voodu template. |
| [cross-vm-wireguard](../../deploy/examples/cross-vm-wireguard/) | ndb on a db VM. Apps on other VMs use TCP inside a WireGuard tunnel. |

Rules for both:

- `data_dir` is on a local disk, never on NFS, EFS or another network volume.
- One ndb replica owns the data dir.
- Unix socket: share the socket dir, and set `-unix-mode 0660` with a shared group.
- TCP: listen on a private (WireGuard) address only. There is no authentication yet.
- Direct mode only for files on the same machine as ndb.

For a plain host, use [deploy/systemd](../../deploy/systemd/). For one container, see [docker.md](docker.md).
