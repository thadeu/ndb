# M13 — Packaging + adoption

Status: not started · Depends on: all · Plan: phase 10

## Goal

ndb is installable in one step and runs real queries in production for
fsw-esl, anway and voodu-webui.

## Epics

### E13.1 Release

- [ ] Static binary (no CGO) for linux/amd64, linux/arm64, darwin/arm64
- [ ] Release pipeline (for example GoReleaser) with checksums
- [ ] Docker image on `distroless/static`; volume for `data_dir`; socket shared through a volume
- [ ] systemd unit example

### E13.2 Clients release

- [ ] Go module tagged
- [ ] Gem `ndb_record` published
- [ ] npm package `ndb` (name to confirm) published

### E13.3 Docs

- [ ] README: what it is, quick start in 5 minutes
- [ ] Query reference (operators, stages, pagination, errors)
- [ ] Client guides: Rails, Go, TS
- [ ] Operations runbook: config, durability, backup/restore, metrics, quarantine, recovery

### E13.5 Deployment examples (`deploy/examples/`, also linked from the docs)

**A. One VM, N pods in different languages** (`deploy/examples/single-vm/`):

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

- [ ] `docker-compose.yml` with: `ndb` service (one replica, `data_dir` on a named local volume that only ndb mounts, socket dir `/run/ndb` on a bind/hostPath shared by every app service), and sample app services in Go, Ruby and Node that mount `/run/ndb` read-write and use `unix:///run/ndb/ndb.sock`
- [ ] The same as a voodu manifest set (`ndb.voodu` + one app), following `infra/voodu/apps/*.voodu` in fsw-esl
- [ ] Optional direct-mode variant: apps append to `/data/logs/<app>-<pod>.ndjson` on a shared volume (one file per process), ndb tails it
- [ ] Socket permissions: daemon flag `-unix-mode 0660` and a shared gid; the example sets both
- [ ] README notes: the `data_dir` is never on NFS/EFS or any network volume (flock and fsync are not reliable there); one ndb replica owns the data dir (`flock`); clients reconnect after a daemon restart

**B. Cross-VM over WireGuard** (`deploy/examples/cross-vm-wireguard/`):

```
  db VM (wg0 10.10.0.1)                     apps VM (wg0 10.10.0.2)
 ┌──────────────────────────────┐          ┌──────────────────────────────┐
 │ postgres, rabbitmq, anway... │          │ Go / Ruby / Node / Bun pods   │
 │ ndb  -tcp 10.10.0.1:7070     │◀─────────│ NDB_URL=tcp://ndb.internal:7070│
 │      -unix /run/ndb/ndb.sock │ WireGuard│ (private DNS → 10.10.0.1)     │
 │ data_dir on local disk       │ (UDP)    │                               │
 └──────────────────────────────┘          └──────────────────────────────┘
```

- [ ] Daemon listens on the WireGuard address only (`-tcp 10.10.0.1:7070`), never on `0.0.0.0` or a public IP; firewall allows 7070 on `wg0` only
- [ ] Apps on the db VM keep the unix socket; apps on other VMs use `tcp://<private dns>:7070`
- [ ] Optional bearer token on the TCP listener (`-token-file`, header `Authorization: Bearer ...`) as defense in depth on top of WireGuard (closes plan open question 5); the unix socket stays token-free
- [ ] Direct mode stays local to the db VM: apps on other VMs write through the API (engine mode), never NDJSON files over a network file system

### E13.6 SDK transport gaps found while planning E13.5

- [ ] Ruby SDK: TCP transport (`tcp://host:port` in `config/ndb.yml`), same pool and keep-alive as the unix socket, DNS resolved on connect
- [ ] Token support in the Go, Ruby and TS SDKs (`token:` option / `NDB_TOKEN`)
- [ ] All SDKs read `NDB_URL` when no address is given

### E13.4 Adoption

- [ ] fsw-esl: outbox-sweeper (from M09) + one report or chart query
- [ ] anway: logs collection (direct mode) + outbox steps
- [ ] voodu-webui: replace one in-memory filter class with ndb
- [ ] Record what was hard in each app → new backlog items

## Exit criteria

- [ ] A fresh server goes from zero to a working query in < 5 minutes with the README
- [ ] Each of the three apps runs at least one real query against ndb in production
- [ ] The in-memory filter code removed from at least one app

## Out of scope

- Hosted service. Multi-server.
