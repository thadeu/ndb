# Docker

Run ndb as one container from the published image. For several apps that share it, see [Single VM](single-vm.md).

## Image

Each `v*` tag publishes `ghcr.io/thadeu/ndb` for `linux/amd64` and `linux/arm64`. Tags are `latest`, the full version, `major.minor`, and `major`. The image is built `FROM gcr.io/distroless/static-debian12:nonroot` from a static, CGO-free binary. It has no shell.

## Run

```bash
docker run -d --name ndb \
  -v ndb-data:/var/lib/ndb \
  -v ndb-run:/run/ndb \
  --stop-timeout 35 \
  ghcr.io/thadeu/ndb:latest
```

- Default command: `-data-dir /var/lib/ndb -unix /run/ndb/ndb.sock`.
- `/var/lib/ndb` is the data volume. Mount it for ndb only, on a local disk.
- `/run/ndb` is the socket dir. Mount the same volume in every app container that talks to ndb. The apps must be allowed to open the socket: add `-unix-mode 0660` and run the apps with the same group (see [deploy/examples/single-vm](single-vm.md)).
- The process runs as UID 65532. A bind mount must allow this user to write. Named volumes work as they are.
- For TCP, add `-tcp 0.0.0.0:7070` and publish the port on a private network only, and add `-token-file` (mount the token file as a secret).
- Run one replica. Two daemons cannot share one data dir.
- No shell in the image, but `ndb` is the entrypoint binary: see [CLI](#cli). For health checks use `GET /health` over the socket or TCP.
- Stop timeout above 30 s, so that running requests finish.

## CLI

The data dir is a volume on the host, so the CLI can reach it in three ways.

```bash
# 1. Inside the running container: the binary is in the image.
docker exec ndb /usr/local/bin/ndb stats
docker exec ndb /usr/local/bin/ndb size

# 2. The host binary, on the shared socket (a bind mount or the volume path).
ndb stats -url unix:///var/lib/docker/volumes/ndb-run/_data/ndb.sock

# 3. The host binary, on the data dir. Works with the daemon running or stopped.
ndb size -data-dir /var/lib/docker/volumes/ndb-data/_data
ndb stats -data-dir /var/lib/docker/volumes/ndb-data/_data
```

- The CLI exists from 0.1.1. The image entrypoint is `/usr/local/bin/ndb`, so `docker run ... ghcr.io/thadeu/ndb <command>` also works.
- `-data-dir` stats show sizes only. Use the socket (or `-url tcp://...` with `NDB_TOKEN`) for rows, indexes and quarantine.
- `ndb restore` needs the daemon stopped: `docker stop ndb`, then `ndb restore <backup> -data-dir <volume path>` (or `docker run --rm -v ndb-data:/var/lib/ndb -v /backups:/backups:ro ghcr.io/thadeu/ndb restore /backups/daily.ndb.gz -data-dir /var/lib/ndb`), then `docker start ndb`. The files must stay owned by UID 65532.
- `ndb backup` runs the same way, in a second container with both volumes, or with the host binary.

## Build it yourself

```bash
docker build -t ndb:dev --build-arg VERSION=dev .
```
