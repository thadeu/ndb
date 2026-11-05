# M01 — WAL, catalog, engine-mode writes

Status: done (light verification, see Result) · Depends on: M00 · Plan: §5, §6.2, §7.1, §7.2, §7.5 (limit), §8, phase 1

## Goal

Durable writes with full ACID on one collection: the WAL, group commit,
recovery, and the catalog. Library API only (no HTTP yet).

## Epics

### E1.1 Line reader

- [ ] Tests first: lines of 0 B, 64 KiB, 1 MiB, 16 MiB, 16 MiB + 1; no final `\n`; `\r\n`
- [ ] Reader with `max_line_size`; never `bufio.Scanner`
- [ ] Returns line + offset; reports a line that is too large without loading it fully
- [ ] Fuzz target

### E1.2 WAL line format

- [ ] Tests first: round-trip of single-op and multi-op transactions; bad CRC; cut line
- [ ] Encoder with fixed field order: `_tx`, `_ts`, `_op`/`ops`, `_id`, `_v`, `doc`, `_crc`
- [ ] CRC32-C over the bytes before `,"_crc"`
- [ ] Decoder + validator
- [ ] Fuzz target

### E1.3 Writer

- [ ] Tests first (with `faultfs`): failed `Sync` exits the process; partial write; batch of 1 and of 1 000
- [ ] One writer goroutine per collection; request channel; drain = group commit
- [ ] `durability: strict` (fdatasync before reply) and `relaxed` (flush every 200 ms)
- [ ] WAL file rotation at 64 MiB; `Fallocate`; `SyncDir` after create
- [ ] `max_line_size` check → `line_too_large`

### E1.4 Recovery

- [ ] Tests first: crash harness with random `SIGKILL` during writes; every acked write is present after restart; no partial write is visible
- [ ] `LOCK` with `flock`
- [ ] Read checkpoint; replay WAL; stop at the first bad line; truncate the active file there
- [ ] Remove `tmp` files

### E1.5 Catalog

- [ ] Tests first: every merge rule of §5.2; conflict errors; implicit creation
- [ ] `catalog.bolt`; `ensure_collection`; `drop_index`
- [ ] Optional `ndb.yml` loader and validation
- [ ] Implicit creation on insert (behind `allow_implicit`, open question 8)

### E1.6 Memtable and CRUD

- [ ] Tests first: insert, update `$set`/`$unset`, delete, versions, `IfVersion` conflict, `unique` violation
- [ ] Memtable keyed by id, with version `_v`
- [ ] `_id` generation when the collection has no id field
- [ ] `find` by id from the memtable

## Exit criteria

- [ ] Crash harness: 10 000 kills with no lost acked write and no partial write
- [ ] Fault injection: every injected failure ends in exit + correct recovery
- [ ] Fuzz: line reader and WAL decoder run 10 min each with no failure
- [ ] Catalog merge tests pass
- [ ] Benchmark: ≥ 20 000 strict commits/s with group commit (target from §13)

## Out of scope

- Queries other than by id (M03), HTTP (M07), sealing (M04).

## Result (2026-11-05)

Packages: `internal/wal` (line reader, record format), `internal/writer`,
`internal/recovery`, `internal/lock`, `internal/catalog`, `internal/config`,
`internal/collection` (head/durable state, CRUD, unique, IfVersion).

Verified, light runs (user rule: never 100% CPU, CI light too):

- Crash harness: 200 kills on the WAL writer and 200 on a collection (target 10 000 → later, on request)
- Fuzz: line reader and WAL decoder/round trip, 10 s each with 2 workers (target 10 min → later)
- Fault injection: write, sync, partial write, rotation failure → exit + correct recovery
- Strict commits: ~8 700/s on macOS (F_FULLFSYNC). The 20 000/s target must be measured on Linux NVMe.

Bugs found by the tests: line reader panic (fuzz), non-atomic rewrite in the
demo writer (crash harness), fallocate growing the file (review), rotation
failure not fatal (test first).

Design change: `writer.Prepare(req any) (Prepared, error)` with a `Stage`
callback, so commits in one batch see each other (head state) while readers
see only durable data. `catalog.Def` gained `Unique`. A `path` without
`write` means direct mode.
