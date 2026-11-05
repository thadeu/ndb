# M03 — Query + indexes

Status: not started · Depends on: M01 · Plan: §10.1, §10.6, §11.1, phase 3

## Goal

`find` with the v1 operators, secondary indexes, snapshots and pagination,
over the memtable. After M04/M05 the same code runs over segments.

## Epics

### E3.1 Query parser

- [ ] Tests first: every operator; invalid queries return clear errors
- [ ] Parse `where`, `fields`, `sort`, `limit`, `skip`, `after`, `count`, `snapshot`
- [ ] Operators: `$eq`, `$ne`, `$gt`, `$gte`, `$lt`, `$lte`, `$in`, `$nin`, `$exists`, `$and`, `$or`, `$not`
- [ ] Dot paths (`address.city`)

### E3.2 Evaluator and type order

- [ ] Tests first: comparisons across types; missing vs `null`; arrays (any element matches)
- [ ] Define one cross-type order (like the MongoDB BSON order) and document it
- [ ] Time values: RFC 3339 strings compare as time when the field is the collection `time`

### E3.3 Index encoding and indexes

- [ ] Tests first: order-preserving keys for every type; range scans; arrays (one entry per element)
- [ ] `_id` index → location
- [ ] Secondary indexes: key = encoded value + id
- [ ] `$in` on an indexed field = one lookup per key, never a scan (needed by [backlog/relationships](../backlog/relationships.md))

### E3.4 Background index build

- [ ] Tests first: queries are correct while an index builds; restart during a build
- [ ] State `building`/`ready`, progress in `stats`

### E3.5 Snapshot

- [ ] Tests first: writes after the query start are not visible to it
- [ ] Snapshot = memtable position (segments are added in M04)

### E3.6 Sort, projection, pagination

- [ ] Tests first: ties on the sort field; inserts between pages (no skip, no repeat); `cursor_mismatch`; snapshot TTL expiry
- [ ] `sort` (id added as the last key), `fields`, `limit`, `skip`
- [ ] Cursor token (base64 JSON: sort values + id + query hash)
- [ ] No-sort cursor = insertion position
- [ ] Snapshot cursors with TTL (default 60 s)
- [ ] `count: true`

## Exit criteria

- [ ] Table tests for every operator and every pagination case pass
- [ ] Benchmark with 1M rows: `find` by id p99 < 1 ms; indexed `find` with 100 results p99 < 5 ms
- [ ] Shared cases for `find` added to `spec/cases/`

## Out of scope

- Segments (M04), pruning and parallel scan (M05), aggregation (M06).
