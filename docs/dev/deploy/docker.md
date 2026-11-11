# Docker

Run ndb as one container from the published image. For several apps that share it, see [Single VM](single-vm.md).

## Image

Each `v*` tag publishes `ghcr.io/thadeu/ndb` for `linux/amd64` and `linux/arm64`. Tags are `latest`, the full version, `major.minor`, and `major`. The image is built `FROM gcr.io/distroless/static-debian12:nonroot` from a static, CGO-free binary. It has no shell. For development, the same tag publishes `ghcr.io/thadeu/ndb-stack`: the daemon and the console in one image (see [ndb-stack](#ndb-stack-development)).

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
- No shell in the image, but `ndb` is the entrypoint binary: see [CLI](#cli). The image has a health check, `ndb health` (see [Health check](#health-check)).
- Add a restart policy (`restart: unless-stopped`), so the daemon comes back after a crash: see [Crash, hang and restart](#crash-hang-and-restart).
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

The image has a health check: every 5 s, `ndb health` asks the daemon on its socket and exits 0 when it answers (the image has no shell and no curl). So `docker ps` shows `healthy`, `unhealthy` or `starting`, and Compose can wait for the daemon with `condition: service_healthy`. You can change the timing:

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

`ndb health` exists from 0.2.1. From 0.2.2 it finds the socket by itself. The image has the check from the release after 1.20.0; before it, declare the check as above.

- With only a TCP listener (`-unix ""`), the check finds no socket. Set `NDB_URL` in the container: `NDB_URL: tcp://127.0.0.1:7070`. With a token, declare the check in Compose with `-url` and the token, or keep the unix socket for it.
- During recovery `/_health` answers `503 recovering`, so the container is `starting`, then `unhealthy` if recovery takes more than the retries. A big data dir needs a longer `start_period`.

## Crash, hang and restart

**The daemon ends** (a panic, an out-of-memory kill, a fatal error). `ndb serve` is the main process of the container, so the container stops with it. Docker starts it again only with a restart policy:

```yaml
services:
  ndb:
    image: ghcr.io/thadeu/ndb:latest
    restart: unless-stopped          # or: docker run --restart unless-stopped
```

At the start the daemon recovers by itself: it replays the [WAL](../wal.md#recovery), which has every acked write, and drops a cut last line (a write that was never acked). No step by hand. The SDKs retry a refused connection and `not_ready` for 10 s, so the apps go on after a short restart.

**The daemon hangs** (alive, but it does not answer). The container stays up. The health check fails, and after 3 failures (15 s) the container is `unhealthy`. Plain Docker and Compose **do not restart** an unhealthy container: they only show it. To restart it, use one of:

- an orchestrator that acts on health (Docker Swarm, Kubernetes with a liveness probe on `ndb health`);
- a monitor of `/_health`, or of the health status of the container (the command below), that runs `docker restart ndb`;
- systemd on the host ([Linux](linux.md)), which restarts on failure.

```bash
docker inspect --format '{{.State.Health.Status}}' ndb   # healthy, unhealthy or starting
```

After any restart, the apps reconnect by themselves.

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

## ndb-stack (development)

`ghcr.io/thadeu/ndb-stack` has the daemon and the console in one image, like `redis-stack`. Use it on a laptop or in a dev Compose file. It needs no parameters:

```bash
docker run -p 7070:7070 -p 4321:4321 ghcr.io/thadeu/ndb-stack
```

- `7070`: the API over TCP, on `0.0.0.0:7070` inside the container. Apps on the host use `NDB_URL=tcp://127.0.0.1:7070`.
- `4321`: the console. Open `http://127.0.0.1:4321`. Its connection `local` opens the daemon on the unix socket `/run/ndb/ndb.sock`, in the same container.
- `/var/lib/ndb`: the data. Add `-v ndb-data:/var/lib/ndb` to keep the data across `docker rm` and new image versions.
- The defaults have **no token**: any client that reaches port 7070 can read and write all data, and the console has no login. Use the stack for development only, never on a public server. For a server, use `ghcr.io/thadeu/ndb` (and `ghcr.io/thadeu/ndb-ui` on `127.0.0.1`).
- `-e NDB_TOKEN=<token>` makes TCP require `Authorization: Bearer <token>`. The console uses the socket, so it needs no token.
- `-e NDB_TCP=<addr>` changes the TCP address (default `0.0.0.0:7070`).
- Arguments go to `ndb serve`, for example `docker run ... ghcr.io/thadeu/ndb-stack -log-level debug`.
- The image has a health check (`ndb health`). `docker stop` stops the two processes. When one process ends, the container ends too, so a restart policy starts it again.
- Base: `gcr.io/distroless/cc-debian12:debug-nonroot` (busybox for the small entrypoint script), UID 65532. The `ndb` and `ndb-ui` binaries come from the published images of the same version.

In Compose, the apps use TCP in the network:

```yaml
services:
  ndb:
    image: ghcr.io/thadeu/ndb-stack:latest
    ports:
      - 127.0.0.1:7070:7070
      - 127.0.0.1:4321:4321
    volumes:
      - ndb-data:/var/lib/ndb

  app:
    build: .
    environment:
      NDB_URL: tcp://ndb:7070
    depends_on:
      ndb:
        condition: service_healthy

volumes:
  ndb-data:
```

### Which image

| Image | Has | Use it for |
|---|---|---|
| `ghcr.io/thadeu/ndb` | the daemon (no shell, socket only by default) | production, and a dev setup like production |
| `ghcr.io/thadeu/ndb-ui` | the console | a console for daemons that run elsewhere |
| `ghcr.io/thadeu/ndb-stack` | the daemon on socket and TCP with no token, and the console | development only |

## Build it yourself

```bash
docker build -t ndb:dev --build-arg VERSION=dev .
```

The stack is built FROM the two images. `NDB_TAG` (default `latest`) selects their version. To build it from local images:

```bash
docker build -t ndb-local .
docker build -t ndb-ui-local console
docker build -t ndb-stack-local \
  --build-arg NDB_IMAGE=ndb-local --build-arg NDB_UI_IMAGE=ndb-ui-local \
  deploy/docker/stack
```
