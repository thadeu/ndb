# M29 — Fast reads

Status: not started · Depends on: nothing · Docs: `docs/dev/architecture.md`, `docs/dev/configuration.md`, `docs/dev/http.md`

## Goal

Reads stay fast when another app writes a lot. Two targets:

- `GET /{coll}/_doc/{_id}` on a hot document: from ms to µs (measured
  at the server, unix socket).
- `_search` with a small answer: less time and fewer allocations; a read
  never waits for a write.

Writes with `durability: strict` keep the cost of the `fsync`. That is
physics, not code, and it is not a goal here.

Each step has a bench line. A step that does not move its number does not
ship.

## Found in the code

- `Collection.Get` (`internal/collection/collection.go`) decodes the whole
  document (`json.Unmarshal`) only to check that `_id` is the key, then
  `withVersion` changes the bytes again. Two passes over the document on
  each `GET`.
- `Collection.mu` is one `sync.RWMutex` for reads and writes. A long
  write (`_bulk`, seal) can make readers wait.

## Epics

### E29.1 — Measure first

- [ ] Bench scenario `mixed`: one client sends `_bulk` without a stop, a
      second client runs `GET` by `_id` and a small `_search`. Record p50,
      p99 and allocs/op of the reads, with and without the writer.
- [ ] Bench lines `get.id_us` and `search.small_us` (alone and `mixed`).
- [ ] pprof (CPU + allocs) of `GET` by `_id` and of `_bulk`: time per phase
      (HTTP, routing, lock, lookup, JSON, write of the answer).
- [ ] Write the numbers here. They are the "before". They decide the order
      of E29.2 to E29.5.

### E29.2 — Fast path of `GET` by `_id`

- [ ] Tests first: a direct-mode line without `_id`, a document whose `_id`
      is not the key, a deleted document, a TTL-expired document, `_v` in
      the answer. Same answers as today.
- [ ] Check `_id` with a scan of the bytes, not a full `json.Unmarshal`
      (or keep a flag in the memtable entry: "the key is the own `_id`").
- [ ] Add `_v` in one pass (or keep the bytes with `_v` already in place).
- [ ] The server writes the bytes to the answer without a new encode.
- [ ] Bench: `get.id_us` lower; allocs/op of `GET` near 0 for the hot path.

### E29.3 — Reads from a snapshot (simple MVCC)

- [ ] Tests first (with `-race`): reads during `_bulk`, seal, compaction and
      TTL see a whole state, never half of a transaction.
- [ ] The writer publishes an immutable view (memtable version + segment
      list) with an atomic pointer after each commit (copy-on-write).
- [ ] Readers take the pointer and no lock. Old views are freed when the
      last reader leaves (Go GC, segment refcount already in `segref.go`).
- [ ] Bench: p99 of `get.id_us` and `search.small_us` in `mixed` near the
      numbers without the writer.

### E29.4 — Small gains in `_search`

- [ ] Only what the pprof of E29.1 shows: parse of the filter (cache the
      compiled filter by body), allocations in the answer, `_id` lookups.
- [ ] Bench: `search.small_us` and allocs/op lower.

### E29.5 — Read and write pools (protection)

- [ ] Separate limits in `internal/server/limit.go` for reads and writes, so
      a burst of `_bulk` does not take the slots of the readers.
- [ ] Test: with the write limit full, a read still answers.
- [ ] Docs: the new limits in `docs/dev/configuration.md`.

## Exit criteria

- Every bench check PASSES; the new lines have budgets in
  `internal/bench/checks.go`, lowered to the measured numbers.
- `get.id_us` (hot, unix socket) in µs; the number is in this file.
- p99 of reads in `mixed` within 2x of the numbers without the writer.
- No change to answers: `spec/cases`, `test/smoke.sh` and every SDK pass.
- Compatibility: a data dir and the SDKs of the last release work with no
  change (the golden test of M30).
- Docs say what changed (`architecture.md`: reads from a snapshot).

## Out of scope

- A second process for reads (the router + writer + reader design): it
  doubles the memtable and applies each write twice. The snapshot gives the
  same isolation in one process.
- Faster `strict` writes.
- Replication (a separate design, still in discussion).
