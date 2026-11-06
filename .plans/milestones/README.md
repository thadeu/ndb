# Milestones

Epics and milestones to build ndb, from the core to the clients.
Source of truth for the design: [`../ndb-v1.md`](../ndb-v1.md).
Deferred ideas: [`../backlog/`](../backlog/).

## Rules

- Each milestone ends with something usable and tested.
- Test first (TDD). Each epic starts with its failing tests.
- A milestone is done only when all its **exit criteria** pass.
- Performance numbers are measured with the benchmarks of M00, on the
  benchmark machine. A miss is recorded, not hidden.
- Status values: `not started`, `in progress`, `done`.

## List

| ID | Milestone | Goal | Depends on | Plan |
|---|---|---|---|---|
| [M00](m00-bootstrap.md) | Bootstrap | repo, tooling, `fs` interface, test harnesses | — | §14 |
| [M01](m01-wal-catalog-writes.md) | WAL, catalog, writes | durable engine-mode writes + catalog | M00 | §5, §7.2, §8, phase 1 |
| [M02](m02-tailer.md) | Tailer | direct-mode ingestion of NDJSON folders | M01 | §6.1, §9, phase 2 |
| [M03](m03-query-indexes.md) | Query + indexes | `find`, operators, indexes, pagination | M01 | §10, §11.1, phase 3 |
| [M04](m04-segments.md) | Columnar segments | sealer, schema rules, Parquet | M03 | §7.3, §7.4, phase 4 |
| [M05](m05-planner-scan.md) | Planner + scan | pruning, column scan, parallel | M04 | §11.2, §11.4, phase 5 |
| [M06](m06-aggregation.md) | Aggregation | pipeline, `$group`, `$dateTrunc` | M05 | §10.2, §11.3, phase 6 |
| [M07](m07-http-api.md) | HTTP API + daemon | the `ndb` daemon on unix socket + TCP | M03 (M04–M06 for full surface) | §12.1, phase 7 |
| [M08](m08-client-go.md) | Go client | `ndb.Collection[T]` | M07 | §12.2 |
| [M09](m09-watch-outbox.md) | Watch + first real use | `watch`/`ack`; fsw-esl outbox-sweeper | M08 | §10.4, phase 8 |
| [M10](m10-client-ruby.md) | Ruby client | gem `ndb-rails`, `Ndb::Record` | M07 | §12.2 |
| [M11](m11-client-ts.md) | TS client | package `ndb`, `ndb.model` | M07 | §12.2 |
| [M12](m12-compaction-blobs-metrics.md) | Compaction, retention, TTL, blobs, metrics | long-running operation; Redis-like document expiry | M05 | §7.5, phase 9 |
| [M13](m13-packaging-adoption.md) | Packaging + adoption | release, Docker, docs, use in 3 apps | all | phase 10 |

## Next (after 0.1.2)

| Item | Goal | Status | Plan |
|---|---|---|---|
| `_id` primary key | `_id` in every collection (UUIDv7), `id:` becomes `unique:`, `/{_id}` routes | in progress (0.1.3) | — |
| Rails finders and destroy | `find_by_<attr>[_and_<attr>]` and `!`; atomic `find_or_create_by` (upsert) and `find_or_initialize_by`; `destroy!`, record `delete`, `Model.destroy(id)`, relation `destroy_all` / `delete_all` | planned (0.1.3, after `_id`) | — |
| Truncate and drop | O(1) empty or remove a collection; time-split collections (month, week, year) | not started | [backlog/truncate-drop.md](../backlog/truncate-drop.md) |
| Delete by index only | big `delete?many` without decoding each document | not started | [backlog/truncate-drop.md](../backlog/truncate-drop.md#related-delete-by-index-only) |
| Relationships | `include` in find | not now | [backlog/relationships.md](../backlog/relationships.md) |

## Dependencies

```
M00 ─▶ M01 ─┬─▶ M02
            └─▶ M03 ─▶ M04 ─▶ M05 ─┬─▶ M06
                 │                  └─▶ M12
                 └─▶ M07 ─┬─▶ M08 ─▶ M09
                          ├─▶ M10
                          └─▶ M11
                                         all ─▶ M13
```

- M02 and M03 can run in parallel after M01.
- M07 can start after M03 with `find` only, and grow as M04–M06 land.
- M10 and M11 can run in parallel after M07.
- `attribute :blob` in M10/M11 waits for M12.

## Milestone file template

```
# Mxx — Name
Status · Depends on · Plan refs
## Goal
## Epics (each: tasks as checkboxes, tests first)
## Exit criteria
## Out of scope
```
