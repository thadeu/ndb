# Milestones

Epics and milestones to build ndb, from the core to the clients.
The design is in the docs: [ndb.clowk.in](https://ndb.clowk.in) (pages in `docs/dev/`). The first design plan, `ndb-v1.md`, is in the git history.
Deferred ideas: [`../backlog/`](../backlog/).

## Rules

- Each milestone ends with something usable and tested.
- Test first (TDD). Each epic starts with its failing tests.
- A milestone is done only when all its **exit criteria** pass.
- Performance numbers are measured with `ndb bench` (or `make bench`), on the
  benchmark machine. A miss is recorded, not hidden. Every check of
  `make bench` must PASS when a milestone closes; a milestone that lowers a
  cost lowers its budget in `internal/bench/checks.go`.
- Status values: `not started`, `in progress`, `done`.

## List

Done milestones are removed from this folder (git history keeps them); the
docs in `docs/dev/` describe what they built.

| ID | Milestone | Goal | Status |
|---|---|---|---|
| M00 | Bootstrap | repo, tooling, `fs` interface, test harnesses | done |
| M01 | WAL, catalog, writes | durable engine-mode writes + catalog | done |
| M02 | Tailer | direct-mode ingestion of NDJSON folders | done |
| M03 | Query + indexes | `find`, operators, indexes, pagination | done |
| M04 | Columnar segments | sealer, schema rules, Parquet | done |
| M05 | Planner + scan | pruning, column scan, parallel | done |
| M06 | Aggregation | pipeline, `$group`, `$dateTrunc` | done |
| M07 | HTTP API + daemon | the `ndb` daemon on unix socket + TCP | done |
| M08 | Go client | `ndb.Collection[T]` | done |
| M09 | Watch + first real use | `watch`/`ack` (the fsw-esl outbox-sweeper moved to M13) | done |
| M10 | Ruby client | gem `ndb-rails`, `Ndb::Record` | done |
| M11 | TS client | package `ndb`, `ndb.model` | done |
| M12 | Compaction, retention, TTL, blobs, metrics | long-running operation; document expiry | done |
| M13 | Packaging + adoption | release, Docker, docs, use in fsw-esl and 1billion | in progress |
| M14 | Ndb Console | self-hosted web UI: connections, browser, dev tools | done |
| M15 | API 1.0 | ES-style routes, `query` body key, one SDK vocabulary (1.0.0) | done |
| M16 | Disk first | bounded memory at any data size (1.1.0; direct mode 1.2.0) | done |
| M19 | Lean at rest | one scheduler for the periodic work (1.7.0), idle collections closed (1.8.0): no CPU, files or reserve at rest | done |
| M18 | Lookup | `lookup` in `_search` (belongs_to, has_many, has_one), `$lookup` at the end of `_aggregate`, SDKs, Rails `includes` | done |
| M22 | SDK fixes from the fsw-esl POC | an error constant per server code, Go connection pool, test clean in Go and TS | done |
| M20 | Streams | Redis Streams parity under `/{s}/_streams/...`: append, consumer groups, per message ack, pending, claim; durable | done |
| M21 | Streams SDK | stream API and the group worker in the Go, Ruby and TS clients | done |
| M23 | Streams in Rails and the Redis commands | `include Ndb::Streams`, `ndb streams`, the Redis Streams commands in Ruby, Go (`ndbredis`) and TS (ioredis, node-redis) | done |
| M24 | Streams: delayed retry | `nack` with `delay_ms`, `RetryAt` in the PEL, `Work.Retry` tiers + `MaxAge` in the SDKs (fsw-esl outbox off RabbitMQ) | done |
| [M25](m25-transactions.md) | Transactions | a block of creates, updates and deletes on one or many collections is one step: `POST /_tx`, N locks, an intent log for many collections, `Model.transaction` and `Ndb::Rollback` | not started |
| M17 | PITR and rename | restore a collection to an instant of the last day; rename to put it in place (1.2.3; at/to 1.2.4) | done |

## Next (after 0.1.2)

| Item | Goal | Status | Plan |
|---|---|---|---|
| `_id` primary key | `_id` in every collection (UUIDv7), `id:` becomes `unique:`, `/{_id}` routes | done (0.1.3) | — |
| Rails finders and destroy | `find_by_<attr>[_and_<attr>]` and `!`; atomic `find_or_create_by` (upsert) and `find_or_initialize_by`; `destroy!`, record `delete`, `Model.destroy(id)`, relation `destroy_all` / `delete_all` | done | — |
| Truncate and drop | O(1) empty or remove a collection; time-split collections (month, week, year) | done (0.3.3) | docs/dev/operations.md#truncate-and-drop |
| Delete by index only | big `delete?many` without decoding each document | done (0.3.2) | — |
| Collection patterns | read many collections by name prefix (`logs-2026-*`): `find`, `count`, `aggregate`, `_coll` per row | done (0.4.0) | docs/dev/query.md#collection-patterns |
| Date math and `$elemMatch` | `now-7d/d` in ranges on the time field; one array element matches many conditions | done (0.4.0) | docs/dev/query.md#date-math |
| Relationships | `lookup` in find, Rails `includes` | done (M18) | docs/dev/query.md#lookup |
| Locks | `with_retry` in every SDK (optimistic, the `_v` guard with the retry done); pessimistic locks: `_lock/{id}`, `_unlock/{id}`, `?lock=<token>`, `Lock` / `lock` in the SDKs, Rails `Model.lock.find`, `lock!`, `with_lock` | done | docs/dev/query.md#lock-one-holder-at-a-time |

## Milestone file template

```
# Mxx — Name
Status · Depends on · Plan refs
## Goal
## Epics (each: tasks as checkboxes, tests first)
## Exit criteria
## Out of scope
```
