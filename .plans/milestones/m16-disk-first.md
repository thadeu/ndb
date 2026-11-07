# M16: Disk first (bounded memory)

> Status: not started. Plan written 2026-11-07, after the 2M × 4 KB bench.

## Why

ndb is a second database for small and medium apps: filters over NDJSON and
a hot cache, the way Solr sat next to a primary database, but simpler. It
never is the primary database. It runs on small VMs.

**Rule: the daemon stays under 512 MB of RSS, whatever the number of
documents, collections or partitions.** Data lives on disk; RAM is a cache
with a fixed size. If a use needs the data in RAM, that use is Redis.

The bench (2M docs × ~4 KB, 5 monthly partitions, 2026-11-07) breaks it:

| Phase | RSS | Disk |
|---|---|---|
| load (2,796 docs/s, ~11 MB/s) | ~7 GB peak | 12 GB (8.9 GB WAL + 2.6 GB segments) |
| idle after load | 3.1 GB | |
| 362 B docs, after compaction | 5.7 GB | 1.5 GB |

## Reference workload: the ESL outbox

The first real use (M09 E9.4): an ESL app that talks to FreeSWITCH writes
the call events to an outbox, and workers process them. Today it is Redis,
with the fear of losing data or running out of memory.

| Item | Value |
|---|---|
| Customers | ~400 |
| Calls | up to millions a day; bench with **1M calls/day** |
| Docs per call | 6 events + 1 `finishcall` = **7M docs/day** (~81/s average, ~400/s peak) |
| Doc size | ~1 KB per event, ~4 KB per `finishcall` (assumed) |
| Retention | **7 days** → ~49M live docs, ~70 GB of raw JSON |
| Durability | `strict` (fsync before the answer) |
| Consumer | `_watch` + `ack` with a stored cursor |
| Layout | `partition_by: day`, `retention: 7d` |

With today's id map alone, 49M docs need over 10 GB of RAM. The target is
512 MB at any point of the 7 days.

## Where the RAM goes today

1. **Every document id is in a Go map.** `state.docs map[string]*entry`
   (`internal/collection/state.go`) holds one entry per live document, also
   the sealed ones (a stub with the segment and row). Two maps (`head`,
   `durable`), ~200 B per doc plus GC headroom. It grows with the data.
2. **Secondary indexes are in-memory B-trees over all documents**
   (`index.Mem`, built in `read.go`): one key + id per doc per index.
3. **One 64 MB memtable per collection** (`defaultSealBytes`). Each
   partition is a collection: 5 partitions = 320 MB, daily partitions = 30×.
4. **Seal and compaction build the whole segment in memory**, in every
   collection at the same time. This is the GB peak.
5. **Every query walks the id map** to build the live bits of the segments
   (`scanSource`), and `_count` decodes every document.
6. **No memory ceiling** (no `GOMEMLIMIT`, default `GOGC`).
7. **The WAL is never deleted** without `wal_retention`: disk is ~1.5× the
   raw JSON.

## Design: segments like Lucene

Lucene (under Solr and Elasticsearch) has the same problem and a known
answer: immutable segments, a small write buffer, per-segment structures on
disk, and live-docs bitmaps. ndb already has immutable Parquet segments and a
WAL; what is missing is to stop mirroring the segments in Go maps.

### What stays in RAM (bounded)

| Item | Size |
|---|---|
| Write buffer (memtable + its index), **one budget for the daemon** | default 32 MB |
| Live bitmap per segment (1 bit per row) | 2M docs = 250 KB |
| Bloom filter of ids per segment (~10 bits per id) | 2M docs = 2.5 MB |
| Segment footers (Parquet metadata, zone maps) | KB per segment |
| Decoded doc cache (LRU, by bytes) | default 16 MB |
| Row groups being read or written (streaming) | a few MB per worker |

Everything else is read from disk with `pread`, through the OS page cache
(which is not the daemon's RSS and is given back under pressure).

### What moves to disk

1. **Id → location.** Each segment stores its ids sorted, with the row of
   each (a sorted block, binary search by `pread`), plus a bloom filter.
   `get(id)`: memtable first, then segments from newest to oldest, skipping a
   segment when its bloom says no. Same for upsert, update and unique checks.
2. **Live bits.** A segment row is dead when a newer version or a delete
   exists. On an update or delete of a sealed doc, the writer finds its
   location and clears the bit. Bitmaps are written with the checkpoint
   (`{segment}.liv`); after a crash, the WAL tail replay clears the bits
   again (idempotent).
3. **Secondary indexes per segment.** At seal, each indexed field gets a
   sorted (key, row) block in the segment, like the id block. A lookup reads
   the key range from each segment and keeps the live rows. The memtable
   keeps a small in-memory index of its own docs only. `ensure_index` on an
   existing collection builds the blocks segment by segment (streaming), or
   at the next compaction.
4. **Order.** Segments are written in `_seq` order, so the default order is
   a merge of the segments, not a sort of a map.

### Streaming seal and compaction

- Seal writes one row group at a time; the memtable is the only full copy.
- Compaction is a k-way merge of the segments in `_seq` order that skips
  dead rows, writes row group by row group, and builds the id and index
  blocks as it goes. Memory: one row group per input segment.
- One compaction at a time in the daemon (a global semaphore), not one per
  collection.

### Global write budget

- One write buffer budget for the whole daemon (`memory.write_buffer`,
  default 32 MB). When the sum of the memtables passes it, the largest
  memtable is sealed. Time-based seal stays (`seal_age`).
- With partitions, only the current period gets writes, so in practice one
  memtable is full and the old partitions hold nothing in RAM.

### Ceiling

- `memory.limit` in the config (default 384 MB), applied with
  `debug.SetMemoryLimit`. The GC works harder near the limit instead of
  growing past it.
- `/_stats` shows the heap, the write buffer, the cache and the page-cache
  hint, so a user can see where the memory goes.

### Fast paths that come for free

- `_count` with no query: the sum of the live bits.
- `_count` with an indexed `$eq`/`$in`: the index rows ∩ the live bits, no
  decode.
- Partitions read in parallel (they are only segments now).

### WAL on disk

Today, without `wal_retention`, the WAL is kept forever. Proposal: delete the
WAL files that are sealed and that no stored watch cursor needs (rules 1–2 of
`waltrunc.go`), by default. `wal_retention` keeps meaning "keep at least this
long" for consumers that start later. **Decision needed** (it changes what a
new watch consumer can replay).

## What changes for users

- Nothing in the API or the SDKs.
- The on-disk format changes (new blocks in segments, `.liv` files). The
  first start of the new version rewrites the old segments once (a
  migration compaction), or we ship it as 2.0 with "export, upgrade, import".
  **Decision needed.**
- New config keys: `memory.limit`, `memory.write_buffer`, `memory.doc_cache`.

## Order of work (tests first, one commit each, each layer green)

1. **Memory test harness.** A Go test that loads N and 4N docs and asserts
   the heap does not grow with N (e.g. 50k vs 200k docs: heap diff < 16 MB).
   It fails today. A bench mode in the scratch bench prints RSS per phase.
2. `memory.limit` + one global write budget + one compaction at a time.
   (Quick win, cuts the peaks.)
3. Streaming seal and compaction.
4. Segment id block + bloom + live bitmaps; `get`, upsert, update, delete
   and unique without the id map. Remove `state.docs` for sealed docs.
5. Per-segment index blocks; the memtable index; drop `index.Mem` over all
   docs.
6. Scan, count and sort over segments + live bits; parallel partitions.
7. WAL default (after the decision), `/_stats` memory fields, docs (config,
   sizing page: "ndb runs in 512 MB"), CHANGELOG, migration.

## Exit criteria

- The 2M × 4 KB bench, 5 monthly partitions: **RSS ≤ 512 MB in every
  phase** (load, reads, compaction, deletes, drop, restart).
- The memory test passes: the heap does not grow with the number of docs.
- `GET _doc/{id}` < 2 ms with a cold page cache, < 0.5 ms warm.
- Load ≥ today's 2.8k docs/s at 4 KB.
- Query times recorded next to today's; a regression is recorded, not
  hidden.
- Restart time recorded (it should drop: no map to rebuild).
