# M09 — Watch + first real use (fsw-esl outbox)

Status: done (light verification; E9.4 fsw-esl outbox-sweeper and the watch where filter are open) · Depends on: M08 · Plan: §10.4, phase 8

## Goal

Consumers read new rows from a saved position, with at-least-once delivery.
First real use: the fsw-esl `outbox-sweeper`.

## Epics

### E9.1 Consumer cursors

- [ ] Tests first: resume after restart; two consumers on one collection; unknown consumer starts from the beginning (or `from: "now"`)
- [ ] `cursors.bolt`: consumer → position
- [ ] `POST /v1/c/{coll}/ack`

### E9.2 Watch stream

- [ ] Tests first: consumer killed at random points resumes with no gap; slow consumer does not block writers
- [ ] `POST /v1/c/{coll}/watch`: chunked NDJSON stream from the consumer position
- [ ] Works for engine mode and direct mode
- [ ] Optional `where` filter (decision: open question 3)

### E9.3 Go client

- [ ] `coll.Watch(ctx, consumer)` → iterator of events; `Ack(ev)`

### E9.4 fsw-esl outbox-sweeper (in the fsw-esl repo, separate change)

- [ ] Declare the outbox collection (engine mode, `durability: strict`)
- [ ] Replace the glob + read of `outbox/*.ndjson` with `Watch` + `Ack` (or `find` on `status`)
- [ ] Keep the current behavior: `exit 0`, log `outbox sweep` with `pending`/`republished`
- [ ] Deploy: `vd apply -f infra/voodu/apps/outbox-sweeper.voodu -y` (run by the maintainer)

## Exit criteria

- [ ] Random-kill consumer tests pass (no gap; duplicates allowed and counted)
- [ ] outbox-sweeper runs against ndb in staging; the `pending=0` path takes < 10 ms whatever the folder size

## Out of scope

- Exactly-once delivery. Consumer groups with shared work.
