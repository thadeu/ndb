# M29 — Fast reads

Status: done (E29.1, E29.2, E29.4 shipped; E29.3 and E29.5 skipped, see below) · Depends on: nothing · Docs: `docs/dev/architecture.md`, `docs/dev/configuration.md`, `docs/dev/http.md`

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

## Results

Machine: darwin/arm64, 15 CPU. `ndb bench -mixed` (one client, unix socket,
keep-alive, 3 s per phase, 3 runs) and Go benchmarks (`-benchmem`).

### E29.1 — the "before"

| Line | Before |
|---|---|
| `get.id_us` (p50, alone) | 26–27 µs (p99 80–94 µs) |
| `search.small_us` (p50, alone) | 43–44 µs (p99 149–172 µs) |
| `mixed` GET p50 / p99 | 26–27 µs / 163–303 µs |
| `mixed` _search p50 / p99 | 44–46 µs / 196–244 µs |
| `BenchmarkGetByID` (collection, 20k docs) | 395 ns/op, 102 B/op, 6 allocs/op |
| `BenchmarkGetDocUnixSocket` (server) | 20.3 µs/op, 82 allocs/op |
| `BenchmarkSearchSmallUnixSocket` (server) | 44.8 µs/op, 33.7 KB/op, 276 allocs/op |
| `BenchmarkGetWhileBulk` (collection, writer of 1000-doc batches) | p99 834–916 ns, p99.9 2.5–4.3 µs, max 8–14 ms |

pprof: the CPU of `GET` and of a small `_search` over the socket is
almost all in syscalls (socket read and write, kevent, the WAL fsync of
the writer). The handlers are under 1% of the CPU. `Collection.Get` is
0.4 µs of the 26 µs. The allocations of `_search` are net/http (about
2/3), then `topK.Push` (23%: the boxing of each row in an `any` and
the growth of the slice), `jsondoc.Pick` and `query.Parse` (6%).
The mutex profile with a writer: readers wait only in
`Collection.applied`, and the wait is under 1 µs at p99.

### E29.2 — shipped

`ownID` (`internal/collection/ownid.go`) scans the bytes when the doc
starts with `"_id"` and has it once; any other doc is decoded as before.
A fuzz test (`FuzzOwnID`) checks that the answer is the one of
`json.Unmarshal`.

| Line | Before | After |
|---|---|---|
| `BenchmarkGetByID` | 395 ns, 6 allocs | 308–343 ns, 4 allocs |
| `BenchmarkOwnID` (1 KB doc) | 770 ns, 1 alloc (decode) | 332 ns, 0 allocs (scan) |
| `get.id_us` | 26–27 µs | 26 µs (no change: HTTP is 98% of it) |

The bench line does not move, but the allocs of the check go to 0 and the
risk is low (fuzz), so it ships. The `_v` stays one pass (prepend) and
the server already writes the bytes with no new encode.

### E29.3 — skipped

A read waits for a write under 1 µs at p99 and 2.5–4.3 µs at p99.9
(`BenchmarkGetWhileBulk`). The `mixed` p99 (163–303 µs) is 100x
that: it is the CPU and the GC of the writer (JSON of 1000-doc bulks,
fsync) on the same machine, not the lock. A snapshot removes the lock
wait only, so it can not move the p99. It does not ship.

### E29.4 — shipped (allocs), `search.small_us` within noise

`topK` makes room for a small page (up to 64 rows) in one allocation and
adds a row with `append` + `heap.Fix` (the same moves as
`heap.Push`, without the `any`). A cache of the parsed filter was not
done: parse is 6% of the allocations and a cache adds state.

| Line | Before | After |
|---|---|---|
| `BenchmarkSearchSmallUnixSocket` | 44.8 µs, 33.7 KB, 276 allocs | 39.6–39.9 µs, 25.5 KB, 248 allocs |
| `search.small_us` | 43–44 µs | 41–43 µs |
| `mixed` _search p99 | 196–244 µs | 202–282 µs (noise) |

### E29.5 — skipped

The daemon has no request slots: each request runs when it comes
(`max_conns` limits TCP connections, off by default, and a connection
carries reads and writes). A burst of `_bulk` can not take the slots of
the readers, because there are none. `mixed` shows the GET p50 the same
with and without the writer (26 µs). New limits would be an option with no
problem to solve, so they do not ship.

### Budgets

New checks in `internal/bench/checks.go`: `get.id_us` ≤ 60 µs,
`search.small_us` ≤ 100 µs, `mixed.get_p99_us` and
`mixed.search_p99_us` ≤ 700 µs (about 2x the measured numbers: a p99 of
one 3 s run moves a lot).

`make bench` at the end: every check PASSES (`get.id_us` 25.4 µs,
`search.small_us` 40.5 µs, `mixed` p99 GET 156.8 µs and _search
185.4 µs).

### Exit criteria not met

- p99 of reads in `mixed` within 2x of the numbers without the writer:
  GET 2–3x, _search 1.3–1.8x. The cause is CPU and GC, not the lock (see
  E29.3).

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
