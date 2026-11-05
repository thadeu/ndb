# M07 — HTTP API + daemon

Status: done (light verification) · Depends on: M03 (grows with M04–M06) · Plan: §12.1, phase 7

## Goal

The `ndb` daemon: one binary that loads the config, recovers, and serves
the full API on a unix socket and an optional TCP port.

## Epics

### E7.1 Daemon main

- [ ] Tests first: start, recovery, ready; `SIGTERM` = graceful stop; `SIGKILL` + restart is correct
- [ ] Flags + optional `ndb.yml`; `data_dir`; `LOCK`
- [ ] Graceful stop: stop accepting, finish writes, flush, final checkpoint

### E7.2 Server

- [ ] Tests first: error format; client disconnect cancels the query
- [ ] HTTP/1.1 on unix socket (default `/run/ndb.sock`) and TCP (optional)
- [ ] Error body `{"error":{"code":"...","message":"..."}}` with a mapped HTTP status
- [ ] Request size limit derived from `max_line_size`

### E7.3 Endpoints

- [ ] `POST /v1/c/{coll}/find`, `/aggregate` (streaming NDJSON response)
- [ ] `POST /v1/c/{coll}/insert`, `/update`, `/delete`
- [ ] `PUT /v1/c/{coll}`, `GET /v1/c`, `DELETE /v1/c/{coll}/indexes/{field}`
- [ ] `GET /v1/c/{coll}/stats`, `GET /health`, `GET /metrics` (basic set)

### E7.4 Shared cases over HTTP

- [ ] The M00 case runner gets an HTTP mode; all `spec/cases/` pass over HTTP
- [ ] `curl --unix-socket` smoke script in the repo

## Exit criteria

- [ ] All `spec/cases/` pass over HTTP
- [ ] Graceful stop and kill + restart tests pass
- [ ] Overhead per request on the unix socket measured and recorded (expected ~20–50 µs)

## Out of scope

- `watch`/`ack` (M09), `/v1/blobs` (M12), auth on TCP (open question 5).
