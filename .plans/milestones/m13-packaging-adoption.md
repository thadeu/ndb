# M13 — Packaging + adoption

Status: in progress (E13.1, E13.2, E13.3, E13.5, E13.6 done; E13.4 open) · Depends on: all · Plan: phase 10

## Goal

ndb is installable in one step and runs real queries in production for
fsw-esl, anway and voodu-webui.

## Epics

### E13.1 Release

- [x] Static binary (no CGO) for linux/amd64, linux/arm64, darwin/arm64
- [x] Release pipeline with checksums: `.github/workflows/release.yml` + `make dist` (GoReleaser not used: it would duplicate the workflow)
- [x] Docker image on `distroless/static`; volume for `data_dir`; socket shared through a volume
- [x] systemd unit example

### E13.2 Clients release

- [x] Go module tagged (`sdk/go/vX.Y.Z`, by the release)
- [x] Gems `ndb` and `ndb-rails` published (`publish-gem.yml`)
- [x] npm package published (`publish-npm.yml`)

### E13.3 Docs

- [x] README: what it is, quick start in 5 minutes
- [x] Query reference (operators, stages, pagination, errors)
- [x] Client guides: Rails, Go, TS
- [x] Operations runbook: config, durability, backup/restore, metrics, quarantine, recovery

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

- [x] `docker-compose.yml` with: `ndb` service (one replica, `data_dir` on a named local volume that only ndb mounts, socket dir `/run/ndb` on a bind/hostPath shared by every app service), and sample app services in Go, Ruby and Node that mount `/run/ndb` read-write and use `unix:///run/ndb/ndb.sock`
- [x] The same as a voodu manifest set (`ndb.voodu` + one app). Written as a template, not run: check the syntax against the real `infra/voodu/apps/*.voodu` in fsw-esl
- [x] Optional direct-mode variant: apps append to `/data/logs/<app>-<pod>.ndjson` on a shared volume (one file per process), ndb tails it
- [x] Socket permissions: daemon flag `-unix-mode 0660` and a shared gid; the example sets both (flag added in this milestone)
- [x] README notes: the `data_dir` is never on NFS/EFS or any network volume (flock and fsync are not reliable there); one ndb replica owns the data dir (`flock`); clients reconnect after a daemon restart

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

- [x] Daemon listens on the WireGuard address only (`-tcp 10.10.0.1:7070`), never on `0.0.0.0` or a public IP; firewall allows 7070 on `wg0` only
- [x] Apps on the db VM keep the unix socket; apps on other VMs use `tcp://<private dns>:7070`
- [x] Optional bearer token on the TCP listener (`-token-file`, header `Authorization: Bearer ...`) as defense in depth on top of WireGuard (closes plan open question 5); the unix socket stays token-free
- [x] Direct mode stays local to the db VM: apps on other VMs write through the API (engine mode), never NDJSON files over a network file system

### E13.6 SDK transport gaps found while planning E13.5

- [x] Ruby SDK: TCP transport (`tcp://host:port` in `config/ndb.yml`), same pool and keep-alive as the unix socket, DNS resolved on connect
- [x] Token support in the Go, Ruby and TS SDKs (`token:` option / `NDB_TOKEN`)
- [x] All SDKs read `NDB_URL` when no address is given

### E13.4 Adoption

- [ ] fsw-esl: outbox-sweeper (from M09) + one report or chart query
- [ ] anway: job history (direct mode), see [backlog/anway-job-history.md](../backlog/anway-job-history.md)
- [ ] voodu-webui: replace one in-memory filter class with ndb
- [ ] Record what was hard in each app → new backlog items

## Exit criteria

- [ ] A fresh server goes from zero to a working query in < 5 minutes with the README
- [ ] Each of the three apps runs at least one real query against ndb in production
- [ ] The in-memory filter code removed from at least one app

## Out of scope

- Hosted service. Multi-server.

## Open items from done milestones

- M12: an `expires_field` with no index still scans; skip it with the zone
  map of the field.
- M16: a restore holds the rows of a snapshot in memory
  (`collection.WriteSnapshot` takes a slice); stream them in two passes.
- M16: a scan of the memtable decodes each document (a `_count` right
  after a bulk load); keep the time value of each memtable entry.
