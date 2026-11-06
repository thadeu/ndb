# One VM, N apps in different languages

One ndb daemon, one data directory, and several apps (Go, Ruby, Node) that talk to it over a shared unix socket.

```
                  VM (one node)
 ┌──────────────────────────────────────────────────┐
 │  ndb pod (1 replica)                              │
 │   data_dir: /var/lib/ndb  (local persistent disk) │
 │   socket:   /run/ndb/ndb.sock ──┐                 │
 │                                 │ hostPath        │
 │  Go pod ────── unix socket ─────┤ /run/ndb        │
 │  Ruby pod ──── unix socket ─────┤ (shared)        │
 │  Node pod ──── unix socket ─────┤                 │
 │  Bun pod ───── unix socket ─────┘                 │
 └──────────────────────────────────────────────────┘
```

The compose file runs the Go, Ruby and Node apps. A Bun app is the same as the Node app (same SDK, same address).

## Run it

```bash
docker compose up --build -d
docker compose logs -f go ruby node
```

Every 5 seconds each app records one visit in the `visits` collection and prints its own count and the count of all apps:

```
go visits: 3, all apps: 9
```

Query it from the host with a curl container that mounts the socket volume:

```bash
docker run --rm --user 1000:65532 -v ndb-single-vm_ndb-run:/run/ndb curlimages/curl \
  --unix-socket /run/ndb/ndb.sock -s http://ndb/v1/c/visits/stats
```

The ndb image has no shell and no curl, so a separate container is used. It must run in group 65532 to open the socket. The volume name is `<project>_ndb-run`; the project is `ndb-single-vm`.

Stop: `docker compose down` (keeps the data). `docker compose down -v` also deletes the data.

## What is in it

| File | Job |
|---|---|
| `docker-compose.yml` | `ndb` (one replica) and the apps `go`, `ruby`, `node` |
| `apps/{go,ruby,node}/` | the sample apps, each with its Dockerfile. They use the SDKs in `sdk/` |
| `docker-compose.direct.yml`, `ndb.direct.yml` | optional direct-mode variant |
| `voodu/ndb.voodu`, `voodu/app.voodu` | the same layout as a voodu manifest set (template) |

## How it is wired

- **Data**: `ndb-data` is a named local volume that only the `ndb` service mounts, at `/var/lib/ndb`.
- **Socket**: `ndb-run` is mounted at `/run/ndb` in ndb and in every app. All apps use `unix:///run/ndb/ndb.sock` (`NDB_URL`). On a Linux host you can use a host path (`/run/ndb:/run/ndb`) instead of a named volume; the dir must be writable by uid 65532.
- **Socket permissions**: the socket is `0660` by default and ndb runs as uid/gid 65532. The apps run with group 65532 (`user: "1000:65532"`), so they can open the socket and nobody else can. The group is what lets the apps in.
- **Startup order**: the apps may start before ndb. They retry until the daemon answers.

## Direct mode (optional)

```bash
docker compose -f docker-compose.yml -f docker-compose.direct.yml up --build -d
```

The apps append one JSON line per visit to `/data/logs/<app>-<pod>.ndjson` (one file per process, one `write()` per line). ndb tails the files (`ndb.direct.yml`, `write: direct`). Reads still go through the socket. Writing through the API is refused for this collection (`direct_collection`).

## voodu

`voodu/ndb.voodu` and `voodu/app.voodu` show the same layout as manifests: one `ndb` with a data volume and a socket dir, and one app that mounts the socket dir. They are **templates**: they were not run, and the field names follow a generic style. Check them against the manifests you already deploy (for example `infra/voodu/apps/*.voodu` in your own repo) and adjust the syntax. Apply with `vd apply -f ndb.voodu -y` first, then the app.

## Notes

- **Never put `data_dir` on NFS, EFS or any network volume.** `flock` and `fsync` are not reliable there.
- **One ndb replica owns the data dir** (`flock`). A second daemon on the same dir fails to start. Do not scale ndb to 2.
- **Clients reconnect after a daemon restart.** The apps in this example keep running while you do `docker compose restart ndb`, and they print `waiting for ndb` until it is back (recovery returns `503 not_ready`).
- All SDKs accept `NDB_URL` as `unix://...` or `tcp://host:port`, so the same apps run unchanged on another VM. See [cross-vm-wireguard](../cross-vm-wireguard/).
