# Docker

Run ndb as one container from the published image. For several apps that share it, see [Single VM](single-vm.md).

## Image

Each `v*` tag publishes `ghcr.io/thadeu/ndb` for `linux/amd64` and `linux/arm64`. Tags are `latest`, the full version, `major.minor`, and `major`. The image is built `FROM gcr.io/distroless/static-debian12:nonroot` from a static, CGO-free binary. It has no shell.

## Run

```bash
docker run -d --name ndb \
  -v ./ndb.yml:/etc/ndb/ndb.yml:ro \
  -v ndb-data:/var/lib/ndb \
  -v ndb-run:/run/ndb \
  --stop-timeout 35 \
  ghcr.io/thadeu/ndb:latest
```

- No command is needed. The defaults are: config `/etc/ndb/ndb.yml` (optional), data dir `/var/lib/ndb`, socket `/run/ndb/ndb.sock` with mode `0660`. A `command:` only adds flags, such as `-tcp 0.0.0.0:7070`.
- `/var/lib/ndb` is the data volume. Mount a named volume for ndb only, on a local disk. Without it the data is in an anonymous volume, see [Compose](#compose).
- `/run/ndb` is the socket dir. Mount the same volume in every app container that talks to ndb. The apps must be allowed to open the socket: run them with group 65532 (see [deploy/examples/single-vm](single-vm.md)).
- The process runs as UID 65532. A bind mount must allow this user to write. Named volumes work as they are.
- For TCP, add `-tcp 0.0.0.0:7070` and publish the port on a private network only, and add `-token-file` (mount the token file as a secret).
- Run one replica. Two daemons cannot share one data dir.
- No shell in the image, but `ndb` is the entrypoint binary: see [CLI](#cli). For health checks use `ndb health` (see [Health check](#health-check)).
- Stop timeout above 30 s, so that running requests finish.

## Compose

### Quick start (dev)

The image has every default, so ndb needs no config, no command and no volume. The SDKs create the collections and their indexes on first use. The app reaches the socket with `volumes_from`:

```yaml
services:
  ndb:
    image: ghcr.io/thadeu/ndb:latest
    healthcheck:
      test: ["CMD", "/usr/local/bin/ndb", "health"]
      interval: 1s
      retries: 60

  app:
    build: .
    volumes_from: [ndb]                  # sees /run/ndb/ndb.sock
    environment:
      NDB_URL: unix:///run/ndb/ndb.sock
    depends_on:
      ndb:
        condition: service_healthy
```

- `volumes_from` mounts every volume of `ndb` in the app, so the app sees `/run/ndb`. It also sees the data dir `/var/lib/ndb` and can write to it (even with `ndb:ro`, a root app could). Use it for dev, not for production.
- The app must run as root or in group 65532 to open the socket (mode `0660`).

::: warning The data lives in an anonymous volume
The image declares `VOLUME /var/lib/ndb`, so Docker keeps the data across `restart` and a crash. But `docker compose down` and then `up` (or a new image version) creates a new container with a new, empty volume: ndb starts with no data. The old volume is still on disk, orphaned. For anything you want to keep, use a named volume.
:::

### Production

Name the data volume, and share only the socket dir with the apps:

```yaml
services:
  ndb:
    image: ghcr.io/thadeu/ndb:0.2.2
    volumes:
      - ndb-data:/var/lib/ndb            # data: survives down/up and upgrades
      - ndb-run:/run/ndb                 # socket: shared with the apps
    healthcheck:
      test: ["CMD", "/usr/local/bin/ndb", "health"]

  app:
    build: .
    user: "1000:65532"                   # group of the socket
    volumes:
      - ndb-run:/run/ndb
    environment:
      NDB_URL: unix:///run/ndb/ndb.sock
    depends_on:
      ndb:
        condition: service_healthy

volumes:
  ndb-data:
  ndb-run:
```

Add `- ./ndb.yml:/etc/ndb/ndb.yml:ro` only for what code cannot declare: direct mode, `durability`, `retention`, `wal_retention`, TCP.

### Over TCP

Inside the Compose network, TCP needs no shared volume: `command: ["-tcp", "0.0.0.0:7070"]` on ndb and `NDB_URL: tcp://ndb:7070` in the app. Do not publish the port. Outside a private network, add `-token-file`.

## Health check

The image has no shell and no curl. `ndb health` asks the daemon on its socket and exits 0 when it is ready, so Compose can wait for it:

```yaml
services:
  ndb:
    image: ghcr.io/thadeu/ndb:latest
    healthcheck:
      test: ["CMD", "/usr/local/bin/ndb", "health"]
      interval: 2s
      timeout: 2s
      retries: 30

  app:
    depends_on:
      ndb:
        condition: service_healthy
```

`ndb health` exists from 0.2.1. From 0.2.2 it finds the socket by itself.

## CLI

The data dir is a volume on the host, so the CLI can reach it in three ways.

```bash
# 1. Inside the running container: the binary is in the image.
docker exec ndb /usr/local/bin/ndb stats            # finds /run/ndb/ndb.sock by itself
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
