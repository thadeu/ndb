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
