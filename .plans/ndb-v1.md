# ndb — v1 plan

> Working name: `ndb`. A query engine for NDJSON folders: a daemon in Go, with
> queries in Mongo style, for Go, Ruby and JS clients on the same server.
>
> Status: draft for discussion (2026-11-04). Nothing is built yet.

## 1. Problem

The apps (voodu-webui, fsw-esl, anway) write NDJSON files. NDJSON is easy:
no ORM and no library to append a line. But each app has its own
in-memory code to read and filter these files:

- It scans folders and reads files at runtime for each query.
- It has no index and no cache, and it leaks memory on large folders.
- The code is written again in Go, Ruby and JS.
- bbolt helps in Go, but Ruby and JS cannot use it.

Example: the fsw-esl `outbox-sweeper` cronjob globs `outbox/*.ndjson` on
each run. The cost grows with the folder size, even when `pending=0`.

## 2. Goal

The app writes NDJSON. The operator installs `ndb`. The app declares its
collections in code (a Rails model, a Go struct, a TS schema), or the
operator writes a short config. The engine follows the files, builds
indexes and columnar segments, and answers `find`, `where` and `aggregate`
fast, from any language.

### Success criteria

- One binary, static, no CGO. It runs on `scratch` or `distroless/static`.
- Setup: no config file is required. A collection can come from code, from
  `ndb.yml`, or from the first `insert`.
- Each language has an idiomatic model layer: `Post.where(...)` in Rails,
  `ndb.Collection[Call]` in Go, `ndb.model('calls', ...)` in TS.
- A new line in a file is visible to queries in less than 100 ms.
- A restart continues from the saved state. It does not read all data again.
- If the derived data (indexes, segments) is deleted, the engine rebuilds it from the NDJSON.
- The same query works with the same result from Go, Ruby, JS and `curl`.
- Performance targets in §13 are met on the benchmark machine.

### Non-goals (v1)

- More than one server. No replication and no clustering.
- SQL. The query language is JSON in Mongo style.
- Joins between collections.
- Transactions between collections.
- Full-text search (tokenizer, ranking).
- An admin UI.

## 3. Use cases

| App | Collection | Access |
|---|---|---|
| fsw-esl | `outbox` | find pending events, or `watch` with a cursor; mark as published |
| fsw-esl | call logs, CDR-like records | charts and reports: count by status per hour |
| anway | `logs` | find by request id, filter by level and time range |
| anway | outbox steps | `watch` + `ack` |
| voodu-webui | current NDJSON data | replace the in-memory filter classes |

## 4. Architecture

```
 apps (Go / Ruby / JS)                       ndb daemon (Go)
 ─────────────────────                       ───────────────────────────────────
 append NDJSON (direct mode) ─────────────▶  source files (glob per collection)
                                                    │ tailer: fsnotify + poll fallback
 client.insert/update/delete ──┐                    ▼
 (engine mode)                 └──▶ writer ──▶ WAL segments (NDJSON, engine mode)
                                    (group commit)  │
                                                    ▼
                                             memtable (recent rows, in RAM)
                                                    │ seal at size/age limit
                                                    ▼
                                             columnar segments (Parquet)
                                             + zone maps + bloom filters
                                             indexes (bbolt): _id, secondary
                                                    │
 client.find/aggregate/watch ◀── HTTP over unix socket or TCP ──┘
```

### Components

| Component | Job |
|---|---|
| `catalog` | the collection definitions (id, indexes, write mode, source); merges code, YAML and implicit declarations |
| `config` | load and validate the optional `ndb.yml` |
| `tailer` | follow the source files of direct-mode collections; keep a cursor per file |
| `writer` | single writer per engine-mode collection: validate, append, group commit, fsync |
| `memtable` | rows that are not sealed yet; read by queries for freshness |
| `sealer` | convert the memtable to a Parquet segment; write it atomically |
| `index` | bbolt: `_id` → location, secondary indexes, cursors, checkpoint |
| `query` | parse the JSON query, plan, execute over segments + memtable |
| `agg` | aggregation pipeline, column-based where possible |
| `watch` | consumer cursors, `ack`, streaming of new rows |
| `compactor` | merge segments; drop old versions and tombstones |
| `server` | HTTP/JSON API, unix socket and TCP |
| `sdk/` | Ruby: plain client (gem `ndb`) and ActiveModel layer (gem `ndb-rails`); model layers for Go (struct tags + generics), JS/TS (schema objects) |

## 5. Catalog and config

### 5.1 The catalog

The daemon keeps a **catalog** of collections in `catalog.bolt`. Each entry has:
the name, the id field, the indexes, the write mode, the time field, the
source glob (direct mode) and the durability.

Three ways fill the catalog. All three use the same internal call
(`ensure_collection`):

1. **From code.** The model layer calls `ensure_collection` when the app
   boots. The call is idempotent.
2. **From `ndb.yml`.** The daemon reads the file at start. The file is optional.
3. **Implicit.** An `insert` into an unknown collection creates it with
   defaults: write mode `engine`, id field `_id` (generated), no secondary index.

### 5.2 Merge rules

| Field | Rule when two declarations differ |
|---|---|
| `indexes` | union: all requested indexes exist |
| `id` | error `catalog_conflict`; the id changes what the data means |
| `write` | error `catalog_conflict` |
| `path` | error `catalog_conflict` |
| `time` | error `catalog_conflict` |
| `durability`, `retention`, `wal_retention` | `ndb.yml` wins (they are operator decisions); a warning is logged |

- A new index on a collection that has data is built in the background.
  Queries keep working with a scan until the index is ready. `stats`
  shows the build progress.
- v1 never drops an index automatically. Removing an index is an explicit
  call (`drop_index`) or a removal from `ndb.yml` plus a restart.

### 5.3 Optional config file

Use `ndb.yml` for:

- direct-mode collections over files that no code declares (another
  app's files, Nginx logs);
- operator settings: memory limit, listen address, durability, retention, wal_retention.

```yaml
data_dir: /var/lib/ndb
listen:
  unix: /run/ndb.sock
  tcp: 127.0.0.1:7070
memory_limit: 1GiB

collections:
  calls:
    write: direct
    path: /data/fsw-esl/calls/**/*.ndjson
    id: call_id
    time: ts
    indexes: [status, dnis]

  outbox:
    write: engine
    id: event_id
    time: created_at
    indexes: [status]
    durability: strict

  logs:
    write: direct
    path: /data/anway/logs/*.ndjson
    time: ts
    indexes: [request_id, level]
    retention: 30d
```

| Key | Meaning |
|---|---|
| `write` | `direct`: apps append to the source files. `engine`: only ndb writes. |
| `path` | glob of source files (direct mode only) |
| `id` | field that identifies a document. Without it, the id is `file:offset`. |
| `time` | time field; used for zone maps, time buckets and retention |
| `indexes` | fields with a secondary index (dot paths are allowed: `address.city`) |
| `durability` | `strict` (fsync before ack) or `relaxed` (flush every 200 ms). Engine mode only. |
| `retention` | drop derived data older than this. The source NDJSON is never deleted by ndb. |
| `wal_retention` | engine mode: delete WAL files older than this once they are sealed and no watch consumer needs them (§7.6). Default: keep forever. |
| `max_line_size` | largest accepted line. Default 1 MiB, hard maximum 16 MiB (§7.5). |
| `blob_threshold` | engine mode: string fields larger than this move to the blob store. Default off; suggested 64 KiB (§7.5). |
| `ttl` | each document expires this long after its `time` field (§11.5) |
| `expires_field` | each document expires at the time in this field, like Redis `EXPIRE` per key (§11.5) |

The same keys are accepted by `ensure_collection` (§10.5), so code and
YAML declare the same things.

## 6. Write modes

### 6.1 Direct mode

The apps append to their own files. ndb only reads.

Contract for the apps:

1. **One writer per file.** Each process writes to its own file, or the
   file name includes the process (`calls-<host>-<pid>.ndjson`).
2. **One line per `write()` call**, with `\n` at the end.
3. **Updates and deletes are new lines.** When `id` is set, the last line
   with the same id wins. `{"<id>": "...", "_deleted": true}` removes the document.
4. **Rotation is allowed.** Rename or create a new file. Do not truncate a
   file that is still in use.

Guarantees:

| | Direct mode |
|---|---|
| Atomicity | per line; a batch line `{"_batch":[...]}` is all or nothing |
| Consistency | invalid lines go to quarantine; ndb cannot reject them |
| Isolation | snapshot reads; no protection for concurrent writers |
| Durability | the app's responsibility (its own fsync) |

### 6.2 Engine mode

Only ndb writes. Clients call `insert`, `update`, `delete`.

| | Engine mode |
|---|---|
| Atomicity | one transaction = one WAL line; a cut line is dropped on recovery |
| Consistency | schema, required fields and `unique` are checked before the write |
| Isolation | one writer goroutine per collection → serializable; `IfVersion` for optimistic concurrency |
| Durability | `strict`: fsync before ack, with group commit |

## 7. Storage

### 7.1 Layout

```
/var/lib/ndb/
  ndb.yml
  LOCK                              ← flock; one daemon per data_dir
  collections/
    outbox/
      wal/
        000001.ndjson               ← sealed, never changes
        000002.ndjson               ← active, append only
      segments/
        000001-000420.parquet       ← rows from WAL seq 1 to 420
      meta.bolt                     ← indexes, checkpoint, schema per segment
    calls/
      segments/...
      meta.bolt                     ← also stores the source file cursors
  blobs/
    sha256/9f/86/9f86d0...          ← large binary values, content-addressed
  catalog.bolt                      ← collection definitions
  cursors.bolt                      ← watch consumers
  quarantine/
    calls.ndjson                    ← lines that failed to parse
```

### 7.2 WAL line format (engine mode)

One transaction is one line. This keeps atomicity simple: the line is
complete, or it is not there.

```json
{"_tx":42,"_ts":"2026-11-04T12:00:00.123Z","_op":"put","_id":"ev_1","_v":3,"doc":{"status":"pending","payload":{}},"_crc":"9a3f12bc"}
{"_tx":43,"_ts":"2026-11-04T12:00:00.130Z","ops":[{"_op":"put","_id":"a","_v":1,"doc":{}},{"_op":"del","_id":"b"}],"_crc":"77e01d2a"}
```

- `_crc` is CRC32-C of the line bytes before `,"_crc"`. ndb writes the
  line, so the field order is fixed.
- The line stays valid JSON. `jq` and DuckDB can read the WAL.
- Trade-off: engine-mode lines are envelopes, not the plain document.

### 7.3 Columnar segments

- Format: Parquet, written with `github.com/parquet-go/parquet-go` (pure Go).
- Each segment holds a range of rows and keeps the source position of each row.
- Stats per row group: min/max per column (zone maps), null count.
- Bloom filter for each indexed column.
- Compression: zstd. Dictionary encoding for low-cardinality strings.
- Segments are immutable. Compaction writes new ones and deletes old ones.
- DuckDB, Spark and pandas can read the segments directly.

### 7.4 Schema rules

NDJSON has no schema. Parquet has one. Rules:

1. The schema is inferred for each segment, from the rows in that segment.
2. Nested objects are flattened to dot paths up to depth 4: `address.city`.
   Deeper values are kept as a JSON string column.
3. Arrays of scalars become list columns. Arrays of objects become a JSON string column.
4. **Type conflict** in a segment (`age` is `int` in one row and `"31"` in
   another): the column keeps the dominant type. Other values go to the
   `_rest` column (JSON) for that row. The query evaluates both.
5. A field that is missing in a segment is `null` for queries.
6. Every segment also keeps the full row as compressed JSON in `_raw`, so
   `find` returns the exact document. (Open question: cost vs benefit, see §17.)
7. String values larger than 4 KiB do not become query columns. They stay
   only in `_raw` (or in the blob store, §7.5). Nobody filters on a base64 value,
   and large strings make Parquet pages and compression worse.

### 7.5 Line size and blobs

NDJSON has no line limit. ndb sets one, because large lines slow down
parse, group commit, the memtable and Parquet.

| Cost of one 10 MB line | Why |
|---|---|
| parse | about 10–30 ms of CPU; 2–3× the line size in memory |
| group commit | one large `fsync` delays every small commit in the same batch |
| memtable | 100 such lines use 1 GB of RAM |
| base64 | adds 33% to the binary size |

**Limit:**

- `max_line_size` per collection. Default 1 MiB. Hard maximum 16 MiB
  (the MongoDB document limit).
- Engine mode: a larger write returns `line_too_large`.
- Direct mode: a larger line goes to quarantine with its file and offset.
  The tailer never crashes and never stops on a large line.
- The line reader is custom, with a limit. Never `bufio.Scanner`: its
  default limit is 64 KiB and it fails on larger lines.

**Blob store:**

Large binary values (audio, PDF, images) stay out of the line. The
document keeps a reference:

```json
{"call_id":"c1","recording":{"$blob":"sha256:9f86d0...","size":7340032,"type":"audio/wav"}}
```

- The address is the SHA-256 of the content: the same content is stored once.
- A blob is written atomically (tmp + fsync + rename + fsync dir) **before**
  the WAL line that refers to it. A crash leaves an unused blob, never a
  reference to a missing blob.
- `find` returns the reference. The content comes only from
  `GET /v1/blobs/sha256:...` (streamed, with `Range` support).
- Optional auto-externalize (engine mode, `blob_threshold`): the client
  sends the value inline (base64 or raw bytes), ndb stores the blob and
  writes the reference. The app does not need to know.
- Model layers: `attribute :recording, :blob` (Rails, returns an IO-like
  object), `ndb.Blob` with an `io.Reader` (Go), `Blob` with a stream (TS).
- Garbage collection: compaction marks the blobs that live documents refer
  to; unreferenced blobs older than a grace period (default 24 h) are deleted.
- Backup: blobs never change, so they are copied like sealed segments.

### 7.6 WAL truncation

By default WAL files are never deleted (history for `watch`, rebuild from the
WAL). With `wal_retention` on an engine-mode collection, a WAL file is deleted
when **all** of these hold:

1. **Sealed:** the checkpoint in `meta.bolt` (already durable) is past the
   file (`checkpoint.pos.seq` is greater than the file number). Recovery never
   reads it: it starts at the checkpoint position, and the tx-gap check starts
   from the tx stored there, so a first remaining file that starts at a later
   tx is valid.
2. **No consumer needs it:** every stored cursor of the collection in
   `cursors.bolt` is at a tx after the last tx of the file, and no open
   `watch` stream is still reading it (streams pin their start tx). The engine
   gives the collection the stored cursors with a func hook, so the packages
   do not import each other.
3. **Old enough:** the first record of the next file (the moment this file
   was closed) is older than `wal_retention` (`catalog.ParseDuration`).

Files are deleted from the oldest, and the walk stops at the first file that
stays, so the kept files are contiguous. The active file is never deleted.
After the deletes the directory is fsync'd. The sealer runs this on every tick.

A consumer that was never acked (stored position `""`) holds the whole
history back until it acks or the history is already gone.

**Blob GC:** with `wal_retention` set, the blob GC also treats the blobs that
the kept WAL files refer to as live, so a watch replay never points to a
deleted blob. (Without it the GC scans only live documents.)

Trade-off: with truncation on, the WAL alone no longer rebuilds the whole
collection; keep the segments and `meta.bolt` (they are in the backup).

## 8. Durability and recovery

Model: the same as PostgreSQL WAL, but simpler, because data is never
written in place.

### 8.1 Write path (engine mode, strict)

1. The client request goes to the writer channel of the collection.
2. The writer takes all waiting requests (group commit).
3. It validates each one (schema, unique, `IfVersion`).
4. It writes all lines with one `write()`, then calls `fdatasync`.
5. It updates the memtable and the index.
6. It answers each request.

```go
func (w *writer) loop() {
	for first := range w.reqs {
		batch := w.drain(first)

		lines := w.validate(batch)

		if err := w.appendAndSync(lines); err != nil {
			log.Fatalf("wal sync failed, crash and recover: %v", err)
		}

		w.apply(lines)

		w.reply(batch)
	}
}
```

`relaxed` durability: answer after the `write()`; a background loop calls
`fdatasync` every 200 ms. A crash can lose the last 200 ms. It never corrupts data.

### 8.2 Disk rules

1. If `fsync` fails, the process exits. It never retries (the PostgreSQL
   "fsyncgate" lesson: a failed fsync can drop dirty pages on Linux).
2. After a new WAL file is created: fsync the directory.
3. Derived files (Parquet, checkpoint): write `tmp`, fsync `tmp`, `rename`, fsync the directory.
4. Preallocate the active WAL file with `fallocate` (64 MiB).
5. All file access goes through an `fs` interface, so tests can inject faults.

### 8.3 Checkpoint

`meta.bolt` stores, in one bbolt transaction:

- the last WAL position that is in a sealed segment;
- the list of live segments;
- the source file cursors (direct mode).

### 8.4 Recovery on start

1. Take the `LOCK`.
2. Read the checkpoint.
3. Engine mode: read the WAL from the checkpoint position. Stop at the
   first line with no `\n` or a bad `_crc`. Truncate the active file at
   that point. Rebuild the memtable.
4. Direct mode: for each source file, compare the cursor (inode, size,
   fingerprint of the first 1 KiB). Continue from the offset, or read the
   file again if it changed.
5. Delete `tmp` files and segments that are not in the checkpoint.
6. Open the API.

### 8.5 Backup

Sealed WAL files and segments never change. Backup = copy the sealed files
(for example to S3). Restore = copy the files back; ndb rebuilds the rest.

## 9. Ingestion (direct mode)

- Discover files with the glob at start and on fsnotify events. Also poll
  every 5 s, because fsnotify can miss events (overlay filesystems, bind mounts).
- Cursor per file: `inode`, `offset`, fingerprint of the first 1 KiB.
- Read only up to the last `\n`. Keep the rest until the next read.
- Rotation: a new inode at the same path = a new file. Finish the old
  inode first if it is still open.
- Truncation: size < offset → log a warning, read the file again from 0.
- Lines that fail to parse, or that are larger than `max_line_size`, go to
  `quarantine/<collection>.ndjson` with the file, the offset and the reason.
  A large line is not copied in full: only its first 4 KiB. A counter is
  exposed in metrics.
- Backpressure: if the memtable is full and the sealer is behind, the
  tailer waits. It never drops lines.

## 10. Query language

All queries are JSON. The same body works from every client.

### 10.1 find

```json
{
  "where": { "status": "pending", "created_at": { "$lt": "2026-11-04T12:00:00Z" } },
  "fields": ["event_id", "status"],
  "sort": { "created_at": 1 },
  "limit": 500,
  "skip": 0
}
```

`"versions": true` adds `"_v"` (the document version, from the same snapshot
as the document) to each row, so a client can send it back as `if_version`.
The SDKs ask for it on every load (Ruby always; Go when the struct has a
`version` field).

Operators in v1: `$eq`, `$ne`, `$gt`, `$gte`, `$lt`, `$lte`, `$in`, `$nin`,
`$exists`, `$and`, `$or`, `$not`. Later: `$regex`, `$elemMatch`.

### 10.2 aggregate

```json
{
  "pipeline": [
    { "$match": { "ts": { "$gte": "2026-11-01" } } },
    { "$group": {
        "_id": { "status": "$status", "hour": { "$dateTrunc": { "date": "$ts", "unit": "hour" } } },
        "total": { "$count": {} },
        "avg_duration": { "$avg": "$duration" }
    } },
    { "$sort": { "_id.hour": 1 } },
    { "$limit": 1000 }
  ]
}
```

Stages in v1: `$match`, `$group`, `$sort`, `$limit`, `$skip`, `$project`.
Accumulators: `$count`, `$sum`, `$avg`, `$min`, `$max`, `$first`, `$last`.
Expressions: field paths, `$dateTrunc` (needed for charts).

### 10.3 Writes (engine mode)

```json
{ "insert": [{ "event_id": "ev_1", "status": "pending" }] }
{ "update": { "id": "ev_1", "set": { "status": "published" }, "if_version": 3 } }
{ "delete": { "id": "ev_1" } }
```

### 10.4 watch

```json
{ "consumer": "rabbitmq-relay", "where": { "status": "pending" } }
```

- The response is a stream of NDJSON rows from the last `ack` position.
- `ack` stores the position of the consumer.
- Delivery is at least once. Consumers must be idempotent.
- A watcher sees only durable records: with `durability: relaxed`, events wait
  for the background fsync (a crash before it loses the commit, and no
  watcher ever saw it). Reads still see relaxed writes at once.
- Direct mode: a file rotated or deleted before a consumer reaches it loses
  that history for the consumer (ndb never controls the apps' files).
- Engine mode with `wal_retention`: a `watch` that must read WAL files that
  were deleted (no stored position, `from: "beginning"`, or a position before
  the oldest kept file) fails with `history_truncated` (HTTP 410). It never
  skips the gap in silence. A stored position is safe: ndb keeps the file that
  holds it (§7.6).
- `where` uses the query language of `find` (same operators). Rules:
  - a `put` is delivered when its document matches;
  - a `del` has no document, so it matches by the **last known document**:
    the last `put` of that id that this stream saw. It is delivered only if
    that `put` matched. A `del` of an id the stream never saw (it happened
    before the stream started, or the stream forgot it: it keeps the last
    65 536 ids) is **not** delivered when `where` is present. Without `where`
    every `del` is delivered, as before;
  - positions advance over the events that are left out: the daemon stores the
    position of the last filtered-out event itself (every 256 events, when the
    stream is idle, and when it closes), so a filtered consumer does not
    replay them after a restart and does not hold back WAL truncation;
  - a document that stops matching (an update moves it out of the filter) is
    not delivered: `where` selects documents, it does not report "left the
    filter". A consumer that needs this watches without `where`.

### 10.5 Catalog calls

```json
{ "ensure_collection": {
    "name": "posts",
    "id": "call_id",
    "time": "created_at",
    "indexes": ["status", "dnis"],
    "write": "engine"
} }
{ "drop_index": { "name": "posts", "field": "dnis" } }
```

- `ensure_collection` creates the collection, or merges with the current
  definition by the rules in §5.2.
- The response returns the final definition and the state of each index
  (`ready` or `building`).

### 10.6 Pagination

Two modes. Both work on `find` and on `aggregate`.

**Offset** (`skip` + `limit`):

```json
{ "where": { "status": "answered" }, "sort": { "created_at": -1 }, "skip": 100, "limit": 50 }
```

- Simple. Good for page numbers in an admin UI.
- Cost grows with `skip`: the engine reads and drops `skip` rows. Page 1 000
  is slow.
- Rows can move between pages when data changes between requests.

**Cursor** (keyset, `after` + `limit`):

```json
{ "where": { "status": "answered" }, "sort": { "created_at": -1 }, "limit": 50 }
→ { "rows": [...], "next": "eyJzIjpbIjIwMjYtMTEtMDRUMTI6MDA6MDBaIiwiYzk5Il19" }

{ "where": { "status": "answered" }, "sort": { "created_at": -1 }, "limit": 50, "after": "eyJz..." }
```

- `next` is an opaque token (base64 JSON) with the sort values and the id of
  the last row. The next page is a range query: `(created_at, id) < (last values)`.
- The engine always adds the id as the last sort key, so the order is unique
  and no row is skipped or repeated.
- Cost is the same for every page when the first sort field has an index.
  With no index: a scan with a top-k heap (`O(n log k)`) per page.
- Rows inserted during pagination do not move the pages that were already read.
- The token is bound to the query: a token used with a different `where`
  or `sort` returns `cursor_mismatch`.
- With no `sort`, the order is the insertion order and the cursor is the
  WAL or source position. This is the cheapest mode, and the same position
  that `watch` uses.

**Snapshot cursor** (optional, `snapshot: true`): the engine keeps the
snapshot of the first request (segment list + memtable position) for a TTL
(default 60 s), so all pages see the same data, like a MongoDB cursor.
Each open snapshot keeps old segments on disk until it expires.

**Total count:** not returned by default (it can cost a full scan).
`"count": true` adds `total`. With an index on the `where` fields, it is cheap.

## 11. Query execution

### 11.1 Snapshot

Each query takes a snapshot when it starts: the list of live segments and
the memtable position. Writes after this point are not visible to the query.

### 11.2 Planner (rule-based in v1)

1. `where` on `id` → index lookup → read the rows by location.
2. `where` on an indexed field (equality or range) → secondary index → locations.
3. Else → scan segments:
   - drop segments and row groups by zone maps (`time` and other columns);
   - drop by bloom filter for equality on indexed columns;
   - read only the columns that the query uses;
   - evaluate the filter on column vectors.
4. Always scan the memtable as well.
5. Apply "last version wins" and tombstones before results are returned.

### 11.3 Aggregation

- `$match` + `$group` on segments run on column vectors (no JSON parse).
- The group table is in memory, limited by `memory_limit`. Over the limit:
  error in v1, spill to disk later.
- Segments are processed in parallel, then the partial groups are merged.

### 11.4 Memory

- `GOMEMLIMIT` is set from `memory_limit`.
- Segment reads use `mmap` where possible, so the data stays out of the Go heap.
- Memtable size limit: 64 MiB per collection (config).
- LRU of decoded documents for repeated `find` by id.

### 11.5 Document TTL

Per-document expiry, like Redis `EXPIRE`. Uses: sessions, locks, tokens,
webhook dedupe, caches.

**Config** (per collection, one of the two):

- `ttl: 24h` — a document expires 24 h after the value of its `time` field.
  Needs `time`.
- `expires_field: expires_at` — each document carries its own expiry time
  (RFC 3339 or unix seconds). A document without the field never expires.

**Reads:** an expired document is hidden from every read (`find`, `aggregate`,
`watch` snapshots, `count`) at once. The filter runs at read time against
the snapshot time, so it does not wait for any background job. With
`expires_field` indexed, the planner can skip expired rows by range.

**Physical removal:**

- Engine mode: a sweeper writes tombstones to the WAL for expired documents,
  in small batches. The expiry also shows in the change feed (`watch`),
  with `"_expired": true`, so consumers can react (outbox, cache eviction).
  An update that moves `expires_at` forward keeps the document alive.
- Direct mode: the document is only hidden. ndb never deletes or changes the
  apps' files. Compaction drops expired rows from derived data.

**Not the same as `retention`:** `retention` drops old *derived data* in
whole segments to control disk use. TTL is a per-document business rule.

**Model layers:** Rails `ttl 24.hours` / `expires_field :expires_at` in the
model; Go tag `ndb:"expires_at,expires"`; TS `expires: 'expires_at'`.

## 12. API and clients

### 12.1 HTTP API

HTTP/1.1 + JSON on a unix socket (default) and an optional TCP port.

**Decision: HTTP, not a binary protocol.** PostgreSQL, MySQL and MongoDB use
their own binary protocols, and so each language needs its own driver.
ndb avoids that: every language has an HTTP client, and `curl --unix-socket`
is the debug tool (the Docker daemon uses the same model). The extra cost
is about 20–50 µs per request on a unix socket with keep-alive, which fits
the targets in §13.
**Change it when** a benchmark shows that HTTP framing or JSON encoding is
the bottleneck. Then add a binary protocol (for example MessagePack frames)
next to HTTP, like ClickHouse does. Only the transports change; the model
layers stay the same.

| Method | Path | Body |
|---|---|---|
| POST | `/v1/c/{coll}/find` | find query; response is NDJSON rows |
| POST | `/v1/c/{coll}/aggregate` | pipeline; response is NDJSON rows |
| POST | `/v1/c/{coll}/insert` | engine mode |
| POST | `/v1/c/{coll}/update` | engine mode |
| POST | `/v1/c/{coll}/delete` | engine mode |
| POST | `/v1/c/{coll}/watch` | streaming NDJSON response (chunked) |
| POST | `/v1/c/{coll}/ack` | consumer + position |
| PUT | `/v1/c/{coll}` | `ensure_collection` |
| DELETE | `/v1/c/{coll}/indexes/{field}` | `drop_index` |
| GET | `/v1/c` | list the catalog |
| PUT | `/v1/blobs` | upload a blob (raw body); returns the `$blob` reference |
| GET | `/v1/blobs/{ref}` | stream a blob; supports `Range` |
| GET | `/v1/c/{coll}/stats` | rows, segments, lag, quarantine count |
| GET | `/health` | ready after recovery |
| GET | `/metrics` | Prometheus format |

Errors: JSON `{"error":{"code":"version_conflict","message":"..."}}` with an HTTP status.

API paths start with `/v1`, so a new daemon can serve old SDKs during a
migration. `/health` and `/metrics` stay without a version (operations convention).

### 12.2 Model layers

Each language gets two layers:

1. **Transport** (about 100–200 lines): builds JSON, sends HTTP, reads NDJSON lines.
2. **Model layer**: the idiomatic API of the language. It declares the
   collection (`ensure_collection` on boot) and builds query JSON.

**Rule: the model layers have no query semantics.** They only build JSON.
Operators, types, sort order and last-version-wins live in the daemon. This
keeps the result the same in every language.

#### Ruby: gem `ndb` (plain client) and gem `ndb-rails` (Rails)

The Ruby SDK is two gems. The gem `ndb` (`sdk/ruby`, module `Ndb`) is the plain
client: `Ndb::Client`, the connection pool, retry, watch and blobs. It has no
runtime dependency (stdlib only) and works in any script. The gem `ndb-rails`
(`sdk/rails`) is the model layer below. It depends on `ndb ~> 0.1`, uses
`Ndb::Client` inside. Everything lives under the `Ndb` module. The errors are those
of `ndb`; `ndb-rails` adds `Ndb::RecordInvalid`, `Ndb::RecordNotSaved` and the
ActiveRecord-like aliases `Ndb::StaleObjectError = Ndb::VersionConflict` and
`Ndb::RecordNotUnique = Ndb::UniqueViolation`. The core gem never defines
`Ndb::Record`, so it stays free of ActiveModel.

Built on `ActiveModel::Model`, `Attributes`, `Validations`, `Callbacks`,
`Dirty`, so it works with `form_with`, validations and `to_json`.

Naming follows ActiveRecord: the gem is `ndb-rails` (`require "ndb/rails"`, like
rspec-rails), the base class is `Ndb::Record`, and the other classes live in the
same module (`Ndb::Relation`, `Ndb::StaleObjectError`, `Ndb::Rails::Railtie`,
`Ndb::Rails::Configuration`).
The app adds one abstract class, like `ApplicationRecord`:

```ruby
class ApplicationRecord < Ndb::Record
  self.abstract_class = true
end
```

```ruby
class Post < ApplicationRecord
  primary_key :call_id

  attribute :status, :string, index: true
  attribute :dnis, :string, index: true
  attribute :duration, :integer
  attribute :created_at, :datetime

  validates :status, presence: true
end
```

- The collection name follows Rails naming: `Post` → `posts`.
  `self.collection_name = "calls"` changes it.
- `source "/data/fsw-esl/calls/**/*.ndjson"` declares a direct-mode collection.
- `where` returns a lazy relation. The request goes to the daemon on
  `each`, `to_a`, `first`, `count`, `pluck`.
- `update!` sends `if_version` from the loaded record. A conflict raises
  `Ndb::StaleObjectError`.
- `Ndb::Rails::Railtie` reads `config/ndb.yml` (socket path, per environment)
  and calls `ensure_collection` for each model on boot (eager load in
  production; on first use in development).

```ruby
Post.create!(call_id: "c1", status: "answered", duration: 42)

post = Post.find("c1")
post.update!(status: "failed")
post.destroy

Post.where(status: "answered", created_at: 1.day.ago..).order(created_at: :desc).limit(50)
Post.where(dnis: %w[1133 1144]).count
Post.group(:status).count
Post.group(:status).average(:duration)
```

| Ruby | Query JSON |
|---|---|
| `where(status: "x")` | `{"status":"x"}` |
| `where(created_at: t..)` | `{"created_at":{"$gte":t}}` |
| `where(created_at: a...b)` | `{"created_at":{"$gte":a,"$lt":b}}` |
| `where(dnis: [...])` | `{"dnis":{"$in":[...]}}` |
| `where(dnis: nil)` | `{"dnis":{"$exists":false}}` |
| `where.not(status: "x")` | `{"status":{"$ne":"x"}}` |
| `or(Post.where(...))` | `{"$or":[...]}` |
| `order(created_at: :desc)` | `"sort":{"created_at":-1}` |
| `group(:status).count` | `$group` + `$count` |
| `group(:status).average(:duration)` | `$group` + `$avg` |
| `page(3).per(50)` | `"skip":100,"limit":50` |
| `after(token).limit(50)` | `"after":token,"limit":50`; the relation exposes `next_cursor` |
| `find_each` / `in_batches` | cursor pagination inside, batch size 1 000 |

#### Go (module `github.com/<owner>/ndb/sdk/go`, own `go.mod`, stdlib only)

Struct tags + generics. `ndb.Collection[T]` reads the tags and calls
`ensure_collection`.

```go
type Call struct {
	CallID   string    `ndb:"call_id,id"`
	Status   string    `ndb:"status,index"`
	DNIS     string    `ndb:"dnis,index"`
	Duration int       `ndb:"duration"`
	TS       time.Time `ndb:"ts,time"`
}

calls, err := ndb.Collection[Call](db, "calls")
if err != nil {
	return err
}

err = calls.Insert(ctx, Call{CallID: "c1", Status: "answered"})
if err != nil {
	return err
}

answered, err := calls.Where(ndb.Eq("status", "answered")).Limit(50).All(ctx)
```

#### JS / TS (package `ndb`, managed with pnpm)

Schema objects, in the style of Mongoose. Types are inferred from the schema.

```ts
const Call = ndb.model('calls', {
  id: 'call_id',
  time: 'ts',
  fields: {
    status: { type: 'string', index: true },
    dnis: { type: 'string', index: true },
    duration: 'number',
  },
})

await Call.create({ call_id: 'c1', status: 'answered' })

const answered = await Call.where({ status: 'answered' }).limit(50)
```

### 12.3 Shared test suite

One suite of JSON cases (catalog calls, writes, queries, expected results)
runs against the transport and the model layer of all three languages.

## 13. Performance targets

Measured on one NVMe server, 8 cores, data in the page cache unless noted.
These are targets to verify, not promises.

| Operation | Target |
|---|---|
| `find` by id | p99 < 1 ms |
| `find` on an indexed field, 100 results | p99 < 5 ms |
| `find` on a field with no index, 10 GB of raw NDJSON | < 2 s |
| `aggregate` `$group` by 2 fields with `$dateTrunc`, 10 GB raw | < 2 s |
| direct-mode ingestion | ≥ 100 MB/s of NDJSON |
| engine-mode `strict` writes, group commit | ≥ 20 000 commits/s |
| new line visible to queries | < 100 ms |
| restart with 50 GB of sealed data | API ready in < 5 s |

## 14. Testing strategy

Test first (TDD) for each phase.

| Kind | What |
|---|---|
| Unit, table tests | each query operator, each accumulator, schema rules |
| Fuzz (`go test -fuzz`) | line parser, CRC check, recovery of cut lines, query parser |
| Crash tests | a child process writes in a loop and is killed with `SIGKILL` at random points; after restart, every acked write is present and no partial write is visible |
| Fault injection | `fs` interface returns errors on `write`/`fsync`/`rename`; the process must exit, and recovery must be correct |
| Race | `go test -race` on writer, memtable, snapshot, watch |
| Golden results | the same data and queries in DuckDB CLI (test script only, not a dependency); results must match |
| Catalog | merge rules from §5.2: code + YAML + implicit; conflicts; background index build |
| Pagination | cursor pages with inserts between requests: no row skipped or repeated; ties on the sort field; `cursor_mismatch`; snapshot TTL |
| Line size | lines of 64 KiB, 1 MiB, 16 MiB and 16 MiB + 1; quarantine in direct mode; `line_too_large` in engine mode |
| Blobs | crash between blob write and WAL write leaves no broken reference; GC keeps referenced blobs; `Range` reads |
| Client suite | shared JSON cases run with the Go, Ruby and JS model layers |
| Benchmarks | `go test -bench` for each target in §13; a data generator for 1–50 GB |

## 15. Phases

Each phase ends with a usable result.

1. **Skeleton + WAL (engine mode) + catalog**
   Catalog with `ensure_collection` and implicit creation, optional
   `ndb.yml`, `LOCK`, `fs` interface, writer with group commit, WAL format
   with CRC, line reader with `max_line_size`, recovery,
   `insert`/`update`/`delete`, `find` by id from the memtable.
   → verify: crash tests and fault injection pass; fuzz on the line parser
   runs 10 min with no failures; catalog merge tests pass.

2. **Tailer (direct mode)**
   Glob, fsnotify + poll, cursors, rotation, truncation, quarantine.
   → verify: tests for rotation, truncation, cut line at the end, restart in the middle of a file.

3. **Query v1 over the memtable + indexes**
   JSON query parser, operators, bbolt `_id` and secondary indexes,
   background index build, snapshot, sort/limit/fields, offset and cursor pagination.
   → verify: table tests for each operator; benchmark of `find` by id and
   by an indexed field with 1M rows; queries are correct while an index builds.

4. **Sealer + Parquet segments**
   Schema inference, Parquet writer, zone maps, bloom filters, atomic files, checkpoint.
   → verify: segment results equal memtable results for the same rows; DuckDB reads the segments.

5. **Planner + column scan**
   Segment and row-group pruning, column vector filters, parallel scan, last-version-wins.
   → verify: golden results vs DuckDB; benchmark of a scan with no index on 10 GB.

6. **Aggregation**
   `$match`, `$group`, `$sort`, `$limit`, `$project`, `$dateTrunc`, parallel partial groups.
   → verify: golden results vs DuckDB; benchmark of `$group` on 10 GB.

7. **HTTP API + model layers**
   Unix socket + TCP, streaming NDJSON responses, catalog endpoints.
   Transports and model layers: `Ndb::Record` (Rails), `ndb.Collection[T]`
   (Go), `ndb.model` (TS).
   → verify: the shared suite passes in the three languages; a small Rails
   app does `Post.create`, `Post.where`, `Post.group(...).count` with no `ndb.yml`.

8. **watch + ack**
   Consumer cursors, streaming, at-least-once delivery.
   → verify: a consumer killed at random points resumes with no gaps.
   First real use: fsw-esl `outbox-sweeper`.

9. **Compaction + retention + TTL + blobs + metrics**
   Merge segments, drop old versions and tombstones, retention by `time`,
   document TTL (§11.5), blob store with upload, streaming, auto-externalize
   and GC, Prometheus metrics.
   → verify: results are the same before and after compaction; disk use
   goes down; expired documents are hidden at once and swept in engine mode;
   blob crash tests pass; a 10 MB value round-trips through
   `attribute :recording, :blob`.

10. **Packaging**
    Static binary, Docker image on `distroless/static`, systemd unit, docs.
    → verify: fsw-esl, anway and voodu-webui each run one real query in production.

## 16. Risks

| Risk | Mitigation |
|---|---|
| Schema conflicts make the columnar part complex | strict rules in §7.4; `_rest` column; many fuzz cases with mixed types |
| Go GC pauses with a large heap | `GOMEMLIMIT`, mmap for segments, limits on memtable and group tables |
| fsnotify misses events in Docker bind mounts | poll every 5 s as a fallback |
| Two apps append to the same file | documented contract; detect lines that interleave (parse errors) and report them |
| Scope grows into a "full database" | non-goals in §2; each phase must be used by a real app before the next big feature |
| parquet-go gaps or bugs | isolate behind a `SegmentWriter`/`SegmentReader` interface; a custom format is possible later |
| Model layers drift and give different results per language | no query semantics in the model layers; shared JSON suite in CI for all three |
| Two apps declare the same collection in different ways | merge rules in §5.2; `catalog_conflict` error at boot, not at query time |
| Apps put large base64 values in lines | `max_line_size`, quarantine, blob store with auto-externalize (§7.5) |

## 17. Open questions

1. Keep `_raw` (the full JSON) in each segment, or rebuild the document from columns?
   `_raw` makes `find` exact and simple, but uses more disk.
2. Direct mode with `id`: keep only the last version in segments (compaction), or keep history?
3. ~~Is `watch` with a filter (`where`) needed in v1?~~ Yes, decided: §10.4.
4. One daemon for all apps on the server, or one daemon per app?
5. ~~Auth on the TCP port (token), or unix socket only in v1?~~ Resolved: optional
   bearer token. `ndb -token-file <path>` makes the TCP listener require
   `Authorization: Bearer <token>` (constant-time compare, 401 `unauthorized`); the unix
   socket stays token-free and `/v1/backup` stays unix-only. SDKs take `token` or env
   `NDB_TOKEN`, and read `NDB_URL` when no address is given.
6. Final name of the project.
7. Should the Rails model layer support associations (`has_many`, `belongs_to`)
   as two queries, or stay out of v1 (no joins)?
8. Should implicit creation (insert into an unknown collection) be on by
   default, or only when the daemon has `allow_implicit: true`?

## 18. Repository layout

One monorepo: the daemon, the three SDKs and the shared test suite.

**Why one repo:** the shared suite (`spec/cases/`) is the same for every
part, and the protocol and the SDKs change together. One PR changes the
endpoint and the three SDKs, and CI tests all of them. Each registry
(Go modules, RubyGems, npm) publishes only its own folder.

```
ndb/
├── .plans/                     ← plan, milestones, backlog
├── cmd/
│   ├── ndb/                    ← the daemon (main)
│   └── ndbgen/                 ← data generator for tests and benchmarks
├── internal/                   ← the engine; no public Go API
│   ├── config/                 ← optional ndb.yml
│   ├── catalog/                ← collections, merge rules, ensure_collection
│   ├── fs/                     ← fs interface + osfs + faultfs
│   ├── wal/                    ← line format, CRC, line reader with a limit
│   ├── writer/                 ← group commit, durability
│   ├── recovery/
│   ├── memtable/
│   ├── tailer/                 ← direct mode
│   ├── index/                  ← key encoding, _id, secondary, background build
│   ├── query/                  ← parser, evaluator, pagination
│   ├── segment/                ← interface + Parquet implementation, schema
│   ├── sealer/
│   ├── planner/                ← pruning, column scan, explain
│   ├── agg/                    ← pipeline, $group, $dateTrunc
│   ├── watch/                  ← consumer cursors
│   ├── compactor/
│   ├── blob/
│   └── server/                 ← HTTP, endpoints, errors
├── spec/
│   └── cases/                  ← shared JSON suite (engine + 3 SDKs)
├── sdk/
│   ├── go/                     ← own go.mod, stdlib only
│   ├── ruby/                   ← gem ndb: ndb.gemspec, lib/ndb/, test/ (plain client, no deps)
│   ├── rails/                  ← gem ndb-rails: ndb-rails.gemspec, lib/ndb/rails/, test/
│   └── ts/                     ← package.json, src/, test/, eslint.config.mjs
├── test/
│   ├── crash/                  ← SIGKILL harness
│   └── golden/                 ← DuckDB CLI comparison scripts
├── bench/                      ← benchmarks for §13
├── deploy/                     ← Dockerfile, systemd unit
├── docs/
├── go.mod                      ← engine module
├── Makefile
└── README.md
```

Decisions:

1. **The engine is in `internal/`.** The daemon is the product; nobody
   imports the engine. This keeps the inside free to change. If an
   embedded mode (like SQLite) is wanted later, a public package is added then.
2. **`sdk/`, not `clients/` or `packages/`.** "SDK" is the usual name for
   per-language libraries of a service, and the SDKs are more than clients
   (models, relations, validations). `packages/` would be confused with Go
   packages; never use `pkg/` (in Go it means public Go packages).
3. **The Go SDK has its own `go.mod`** (`sdk/go/go.mod`), so an app that
   imports it does not get the engine dependencies (parquet-go, bbolt,
   fsnotify). Tags use a prefix: `sdk/go/v0.1.0`.
4. **CI uses path filters.** A change in `sdk/ruby/` or `sdk/rails/` runs the Ruby tests and
   the shared suite. A change in `internal/` or `spec/` runs everything.

Versioning: the daemon and the SDKs share the **major** version. SDK 1.x
talks to daemon 1.x, through the `/v1` API paths.
