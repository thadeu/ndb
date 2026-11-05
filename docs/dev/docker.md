# Docker

Each `v*` tag publishes `ghcr.io/thadeu/ndb` for `linux/amd64` and `linux/arm64`. Tags are `latest`, the full version, `major.minor`, and `major`. The image is built `FROM gcr.io/distroless/static-debian12:nonroot` from a static, CGO-free binary. It has no shell.

```bash
docker run -d --name ndb \
  -v ndb-data:/var/lib/ndb \
  -v ndb-run:/run/ndb \
  --stop-timeout 35 \
  ghcr.io/thadeu/ndb:latest
```

- Default command: `-data-dir /var/lib/ndb -unix /run/ndb/ndb.sock`.
- `/var/lib/ndb` is the data volume. Mount it for ndb only, on a local disk.
- `/run/ndb` is the socket dir. Mount the same volume in every app container that talks to ndb. The apps must be allowed to open the socket: add `-unix-mode 0660` and run the apps with the same group (see [deploy/examples/single-vm](../../deploy/examples/single-vm/)).
- The process runs as UID 65532. A bind mount must allow this user to write. Named volumes work as they are.
- For TCP, add `-tcp 0.0.0.0:7070` and publish the port on a private network only. There is no authentication yet.
- Run one replica. Two daemons cannot share one data dir.
- No shell in the image: use `GET /health` over the socket or TCP for health checks, and `docker exec`-free tools. For backups run `ndb backup` in a second container that mounts the same two volumes, or use the host binary.
- Stop timeout above 30 s, so that running requests finish.

Build it yourself:

```bash
docker build -t ndb:dev --build-arg VERSION=dev .
```
