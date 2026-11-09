# M20 — Streams (Redis Streams parity)

Status: E20.1, E20.2 and E20.3 done; E20.4 is M21 (the SDKs) · Depends on: M09 (watch, cursors), M12 (retention) · Branch: `feature/redis-streams-compabitility`

## Goal

An app that uses Redis Streams can change to ndb through a factory, with the
same behavior: append, consumer groups, per message ack, pending entries,
claim. The names are ndb names, under one route prefix, `/{s}/_streams/...`
(a future `_pubsub` gets its own prefix in the same way).

What ndb adds: every acked append and every group change is on disk before
the answer (no `everysec` window), and a stream is a collection, so
`_search`, `_count` and `_aggregate` read it with the MongoDB filters.

What it does not try: the speed of RAM. A strict append costs one fsync.

## Decisions (2026-11-09)

- **A stream is its own collection**, with `"stream": true` in its
  definition. It is made by the first `_streams/append`, or by
  `PUT /{s} {"stream": true}`. A normal collection never becomes a stream,
  and the reverse. `_search` on a stream reads its entries; the other
  collections do not change.
- **Pull, not push.** A worker asks for messages (`groups/{g}/read`), gets a
  batch and acks it, as with `XREADGROUP`. It is not built on `_watch`:
  `block_ms` is a request that waits (long-poll), woken by the next append.
  It can use the internal wake of the change feed, not the `_watch` route.
- **Few routes, one per action**, as Elasticsearch does (not one route with
  a different body per action): the access check stays read or write by
  route.
- **No read of many streams in one call** (`XREAD k1 k2`). The use case is
  one stream per group.
- **No `XREAD` without a group in the first version**: `_search` with a
  sort on `_id` and `after` reads a stream from any position.

## Epics

### E20.1 Contract (protocol)

Done (2026-11-09): `docs/dev/http/streams.md` (routes, bodies, answers,
IDs, errors, access), `docs/dev/streams.md` (use, cost model, Redis map),
`spec/streams/` (12 multi-step cases and their format; outside
`spec/cases`, whose runner reads one request per case). Decisions taken
while writing it:

- Entries get `_ts` (the `ms` of the ID, RFC 3339): it is the `time` of the
  stream, so `retention`, zone maps and time filters work.
- On a stream, `_id` sorts and compares as an ID (`ms`, then `seq`), not as
  text (`1-9` before `1-10`): in `sort`, range filters and cursors.
- `docs` appends many entries with one fsync.
- A read gives 10 entries by default, 10 000 at most.
- The 5 new error codes go into `docs/dev/http/errors.md` in E20.2, with the
  SDK constants: the error code test of M22 fails on a code that an SDK
  does not have.

The wire contract, written as `docs/dev/streams.md` and
`docs/dev/http/streams.md` before any code, with the spec cases that prove it
(`spec/cases/streams/`, failing first).

- [ ] **Entry ID**: `<ms>-<seq>`, two unsigned 64-bit integers, made by the
      server from the clock; it always grows (a clock that goes back keeps
      the last `ms` and adds 1 to `seq`). The ID is the `_id` of the entry,
      so `GET /{s}/_doc/{id}` and the `_id` sort work. An explicit ID must be
      greater than the last one (`400 stream_id_too_small`). The last ID
      survives a restart (read from the WAL at open).
- [ ] **Entry**: a JSON object (fields and values, nested allowed, unlike
      Redis), plus `_id`. `time` of the collection is the `ms` of the ID, so
      the zone maps and `retention` work by time.
- [ ] **Trim**: `max_len` (number of entries) or `min_id`, with `approx`
      (drop whole segments only, the cheap path, like `MAXLEN ~`) or exact.
      On append or with `trim`. What a trim does to the pending entries of a
      group: keep them as references to deleted entries (Redis behavior up to
      8.0) — to decide, see Open questions.
- [ ] **Group**: a name, `last_delivered_id`, `entries_read` (for the lag),
      and the PEL. Made with a start: `$` (new entries only), `0` (all) or
      an ID. Many groups per stream; each reads the whole stream.
- [ ] **PEL**: ID → consumer, delivery time, delivery count. Each consumer
      has its own list. Durable: one bbolt transaction per `read` (a batch),
      per `ack` and per `claim`, fsync'd before the answer — the same store
      as the watch cursors, or one file per stream (decide in E20.2).
- [ ] **Read** of a group: `new` (entries after `last_delivered_id`, like
      `>`) or `pending` (the entries of this consumer in the PEL, like `0`),
      `count`, `block_ms`, `no_ack` (not added to the PEL). The consumer is
      made on its first read.
- [ ] **Ack**: by IDs; gives the number acked (like `XACK`). An unknown ID
      counts 0, not an error.
- [ ] **Pending**: summary (count, lowest and highest ID, per consumer) and
      list (`idle_ms`, `consumer`, `start`, `end`, `count`).
- [ ] **Claim**: by IDs with `min_idle_ms` (like `XCLAIM`), or a scan with a
      cursor (like `XAUTOCLAIM`); `just_ids`; the delivery count goes up
      (not with `just_ids`, as in Redis). Entries deleted from the stream
      come back in `deleted` and leave the PEL.
- [ ] **Info**: stream (length, first and last ID, groups) and groups
      (consumers, pending, last delivered, lag).
- [ ] **How the engine runs it** (the cost model of the contract):
      - *Append* is a normal write: the whole entry goes to the WAL (fsync
        in `strict`), then the memtable, then a segment at seal. `_search`
        reads it like any document.
      - *Read `new`* is one internal range read: `_id > last_delivered_id`,
        sorted by `_id`, limit `count`. The ID grows with the insert order,
        so the next entries are together: at the end of the memtable for a
        group that keeps up, or in row groups that follow each other in a
        segment for a group that is late (the zone maps skip the groups
        before; `read_cache` holds the next ones). IDs and documents come in
        the same pass, not a list of IDs and then one get per ID.
      - *Read `pending`* is a batch get by id of the IDs of the consumer's
        PEL: they can be far apart, and get by id is the fastest path.
      - The answer of a read is the **full entries** (ID and fields), as
        `XREADGROUP`; `fields` keeps only some of them (smaller answers, and
        fewer columns read in a segment). Only `claim` has `just_ids`.
      - *The cost is the PEL write*, not the read: one bbolt transaction per
        read batch, fsync'd before the answer (a few ms, more than reading
        100 entries from the memtable). So: batches (`count`), `no_ack` (no
        PEL write, at most once), and Open question 3 (a `relaxed` PEL).
      - Not from the WAL, as `_watch` does: `wal_retention` deletes WAL
        files, and a late group must read entries that are in segments.
- [ ] **Errors**: `stream_id_too_small`, `group_exists`, `unknown_group`,
      `not_a_stream`, `invalid_stream_id`; the HTTP status of each.
- [ ] **Redis map**: one table, each Redis command → the ndb route and
      body, and the differences (for the factory and the SDK plan).

### E20.2 HTTP API

Routes (all under the collection; `_streams` reserves the prefix):

| Route | Redis |
|---|---|
| `POST /{s}/_streams/append` | `XADD` (with `trim`) |
| `POST /{s}/_streams/trim` | `XTRIM` |
| `GET /{s}/_streams` | `XINFO STREAM`, `XLEN` |
| `PUT /{s}/_streams/groups/{g}` | `XGROUP CREATE` (`MKSTREAM` is the default) |
| `POST /{s}/_streams/groups/{g}/setid` | `XGROUP SETID` |
| `DELETE /{s}/_streams/groups/{g}` | `XGROUP DESTROY` |
| `DELETE /{s}/_streams/groups/{g}/consumers/{c}` | `XGROUP DELCONSUMER` |
| `GET /{s}/_streams/groups` | `XINFO GROUPS` |
| `GET /{s}/_streams/groups/{g}/consumers` | `XINFO CONSUMERS` |
| `POST /{s}/_streams/groups/{g}/read` | `XREADGROUP` |
| `POST /{s}/_streams/groups/{g}/ack` | `XACK` |
| `POST /{s}/_streams/groups/{g}/pending` | `XPENDING` |
| `POST /{s}/_streams/groups/{g}/claim` | `XCLAIM`, `XAUTOCLAIM` |

`XRANGE`, `XREVRANGE`, `XLEN`, `XDEL` are the routes of every collection:
`_search` (sort on `_id`, `after`), `_count`, `DELETE /{s}/_doc/{id}`.

- [x] Catalog: `stream: true`, implicit create on the first append; a
      `_streams` route on a normal collection is `400 not_a_stream`
- [x] ID generator per stream, under the stream lock (`internal/streams`);
      the `_id` on disk is padded (`%020d-%020d`) so the engine text order
      is the ID order with no engine change; the HTTP routes give and take
      `<ms>-<seq>` (`_search`, `_count`, `_doc/{id}` too)
- [x] Group store: `streams.bolt` in the collection dir (one per stream)
- [x] The group store in a backup: a `stream` section after the WAL of the
      stream (a consistent copy from a read transaction); the restore
      writes it back
- PITR restores the entries, not the groups: the PEL has no history, so a
  group at an instant cannot be rebuilt (decision 2026-11-09, documented)
- [x] Wake of blocked reads on append (one channel per stream, closed by
      each append)
- [x] Trim: by delete of IDs in batches of 10 000; `approx` deletes the
      same entries for now (allowed: it may keep more, never fewer)
- [x] Trim `approx` in whole blocks of 1 024 entries (a row group): a
      trim on every append deletes once per block. A drop of whole segments
      would need an engine change; not needed for the `~` behavior
- [x] Access: append, trim, group changes, read, ack, claim are writes (a
      read moves the PEL); info and pending are reads
- [x] Truncate and drop of a stream also clear its groups (and a refused
      truncate keeps them)
- [x] `/_metrics`: `ndb_stream_entries_added_total`, per group
      `ndb_stream_group_pending` and `ndb_stream_group_lag` (streams used
      since the start)
- Console: not in M20 (decision 2026-11-09). The dev tools of the console
  already call every route; a streams page is a plan of its own, later.

### E20.3 Bench and e2e

Done (2026-11-09):

- [x] `ndb bench -streams` (in `make bench` too): appends from 1 and 10
      clients (strict, relaxed) and in batches of 100, group read + ack
      with 1, 3 and 10 workers, append to delivery with `block_ms`, a claim
      of a dead worker, a `kill -9` while apps append and a worker acks
- [x] 13 checks in `internal/bench/checks.go`: no failed append, no
      undelivered or unclaimed entry, no acked append, delivered entry or
      ack lost by a `kill -9`; append p99 (1 client 20 ms, 10 clients
      60 ms), latency p99 60 ms, 10 workers at least 1 500 entries/s
      (about 3x of the first run on a Mac: 4.5 ms, 19 ms, 17-27 ms,
      5 300 entries/s)
- [x] Parity: `TestStreamCasesOnRedis` runs the cases of `spec/streams`
      against a real Redis 7.4 (a service in CI). It found one difference,
      fixed: a trim does not change `max_deleted_id` in Redis
- [x] Fuzz of the ID parser
- [x] From the first run: appends wait in a queue while an insert runs and
      go in one insert (group commit: 10 clients 329 to 860 entries/s), and
      an ack runs out of the stream lock in a bbolt batch (10 workers
      4 000 to 5 300 entries/s)

Known limit: the reads of one group take the stream lock and one fsync of
the group store each, so 10 workers read about as fast as 3 (the fsync of
macOS is slow; Linux is faster).

### E20.4 SDK specification → M21

Its own plan and milestone (`m21-streams-sdk.md`): the stream API of the Go,
Ruby, Rails and TS clients, and a Redis-compatible adapter for the factory of
an app (the same method set as its Redis client, on ndb). Written when E20.1
is done.

## Exit criteria

- Every `spec/cases/streams/` case passes on the daemon and in each SDK
- The e2e driver gives the same results on Redis and on ndb
- `ndb bench -streams` passes its budgets; every other `make bench` check
  still passes
- A `kill -9` loses no acked append and no acked or delivered PEL change
- Docs: `docs/dev/streams.md`, `docs/dev/http/streams.md`, the Redis map

## Out of scope

- Read of many streams in one call; `XREAD` without a group (use `_search`)
- Replication and failover (ndb is one daemon)
- Lua, `MULTI`, keyspace events
- Redis protocol (RESP) on the wire

## Open questions

1. ~~Trim of pending entries~~: keep the PEL references (Redis up to 8.0).
2. ~~Group store~~: one bbolt file per stream.
3. ~~Durability of a group read~~: `strict` by default (one fsync per read
   batch), `relaxed` per stream as an option.
4. The app that will use it first: language, the Redis commands it uses,
   messages per second and number of workers (sets the priority inside
   E20.2 and the budgets of E20.3).
