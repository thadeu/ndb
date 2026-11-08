# M19: Lean at rest

Status: not started

## Goal

A collection that is not used costs almost nothing: no CPU, no open files,
no WAL reserve, little memory. A daemon with 1 000 collections (or a few
partitioned collections with hundreds of days) at rest is as cheap as one
with 10. This is also the base of an embedded ndb (a later milestone): the
same engine, with nothing running for data nobody touches.

## Where we are (ndb bench -idle, 300 collections of 10 docs)

| Cost per collection at rest | Now | Why |
|---|---|---|
| disk | 80 KB | WAL reserve proportional to the data (1.4.6; it was 4 MiB) |
| CPU | 2.6% of a core for 300, linear | a sealer and a compactor goroutine, each with a 1 s ticker; a 200 ms flush ticker in the WAL writer of a `relaxed` collection; a TTL sweeper when ttl is set |
| open files | 2 | the WAL segment and meta.bolt stay open |
| RSS | ~70 KB | writer buffers, caches, goroutine stacks |

## Epics

### E19.1 One scheduler

One goroutine in the engine runs the periodic work of every collection
(seal due, compaction due, WAL truncation, TTL sweep, PITR snapshots,
partition retention) from one timer, instead of one ticker per collection
per job.

- A collection registers a deadline ("next check at T") when it changes:
  a write marks it dirty, a seal or compaction sets the next one. The
  scheduler keeps a heap of deadlines and sleeps until the earliest.
- A collection that did not change has no deadline: it costs nothing.
- The relaxed flush stays per write burst (a timer armed by the first
  unflushed write, not a ticker).
- The work stays bounded as today (one compaction at a time, the write
  budget), and a slow job does not delay the others: the scheduler hands
  jobs to a small pool.

### E19.2 Close idle collections

A collection not read or written for `idle_close` (default 10 min) closes
its WAL file (truncated to its size: no reserve), its meta.bolt and its
caches, and opens again on the next request.

- Open is lazy and safe under concurrent requests (one opener, the others
  wait); a `_watch` or a pending seal keeps a collection open.
- Reopen cost is measured: the first request after a close stays under
  10 ms for a collection of 1 GB.
- `GET /_stats` shows open and closed collections.

## Exit criteria

- `ndb bench -idle -collections 1000` and a partitioned collection with
  365 daily partitions at rest: CPU under 0.5% of a core, open files under
  20 + 2 per open collection, disk per idle collection under 16 KB above
  its data. The idle budgets in `internal/bench/checks.go` are lowered to
  these numbers.
- `make bench` passes, with no other check worse than before.
- Crash safety unchanged: the crash phase loses 0 acked writes; a kill -9
  while collections close and reopen loses nothing (a new test).
- Docs: architecture.md (the scheduler), configuration.md (`idle_close`),
  operations.md (stats).
