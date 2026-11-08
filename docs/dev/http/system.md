# System routes

The routes of the daemon itself. Their path starts with `_`, which a collection name never does. The rules that apply to every route (token, formats, errors) are on [HTTP API](../http.md).

In the examples, `db` is a connected handle: `ndb.Connect(addr)` in Go, `Ndb.connect(addr)` in Ruby, `connect(addr)` in TypeScript.

## `GET /_health`

Tells whether the daemon is ready. Use it for health checks and to wait for the end of recovery.

**Answer:** `200 {"status":"ok"}`, or `503 {"status":"recovering"}` while recovery runs.

**Errors:** none other than 503. With `-token-file`, a TCP request needs the token, like every route.

::: code-group

```bash [curl]
curl --unix-socket /run/ndb/ndb.sock http://ndb/_health
# {"status":"ok"}
```

```go [Go]
err := db.Health(ctx) // nil when ready; ndb.ErrNotReady while recovering
```

```ruby [Ruby]
db.health # => true; raises Ndb::NotReady while recovering
```

```ts [TypeScript]
await db.health() // throws NotReadyError while recovering
```

:::

`ndb health` (CLI) exits 0 when the daemon is ready: use it as a container health check.

## `GET /_metrics`

Prometheus text: requests, errors, latency of `_search` and `_aggregate`, rows, writes, WAL and segment counters, blob store size. It is open to scrapers like any route (token on TCP).

```bash
curl --unix-socket /run/ndb/ndb.sock http://ndb/_metrics
```

## `GET /_collections`

The catalog: one definition per collection, sorted by name.

**Answer:** `{"collections":[{"name","write","time","indexes","unique","durability",...}]}`. The keys of a definition are on [PUT /{coll}](collections.md#put-coll).

::: code-group

```bash [curl]
curl --unix-socket /run/ndb/ndb.sock http://ndb/_collections
```

```go [Go]
defs, err := db.Collections(ctx) // []map[string]any
```

```ruby [Ruby]
db.collections # => [{"name" => "calls", ...}]
```

```ts [TypeScript]
const defs = await db.collections()
```

:::

## `GET /_stats`

Every collection, the totals, the version and the uptime.

**Answer:**

```json
{
  "version": "1.0.0",
  "uptime_seconds": 50.1,
  "collections": [
    {
      "name": "posts", "write": "engine", "rows": 4, "quarantined": 0,
      "indexes": [{"field": "author", "state": "ready", "done": 0, "total": 0}],
      "segments": 1, "wal_files": 1,
      "bytes": {"wal": 805, "segments": 3965, "meta": 131072}, "total": 135842
    }
  ],
  "totals": {"collections": 2, "rows": 7, "quarantined": 1, "segments": 1, "wal_files": 1, "bytes": 529315},
  "blobs_bytes": 0, "cursors_bytes": 131072, "shared_bytes": 262144
}
```

- `segments` and `wal_files` count files, not live rows. A direct collection has no segments and no WAL; its `meta` is `meta.bolt` and `quarantine/`.
- `totals.bytes` includes `shared_bytes` (blobs, `cursors.bolt`, catalog and the other files of the data dir).
- One element of `collections` is [GET /{coll}/_stats](collections.md#get-coll-stats).

::: code-group

```bash [curl]
curl --unix-socket /run/ndb/ndb.sock http://ndb/_stats
```

```go [Go]
stats, err := db.Stats(ctx)
```

```ruby [Ruby]
db.stats
```

```ts [TypeScript]
const stats = await db.stats()
```

:::

`ndb stats` (CLI) prints the same, and reads the data dir when no daemon answers.

## `GET /_size`

The disk use per collection and per part: the scan of `ndb size`. It only stats files.

**Answer:** `{"data_dir","total","collections":[{"name","bytes":{"wal","segments","meta","blobs","cursors"},"total","wal_files","segment_files"}],"shared":{...}}`.

::: code-group

```bash [curl]
curl --unix-socket /run/ndb/ndb.sock http://ndb/_size
```

```go [Go]
size, err := db.Size(ctx)
```

```ruby [Ruby]
db.size
```

```ts [TypeScript]
const size = await db.size()
```

:::

## `POST /_backup`

Streams a backup file (`.ndb`, NDJSON, `application/x-ndjson`) taken at a mark between two commits. Writes go on during the backup.

| Body key | Type | Default | Meaning |
|---|---|---|---|
| `no_blobs` | bool | `false` | leave the blob contents out (the references stay) |

**Answer:** the stream. **Errors:** `403 forbidden` over TCP: the backup works on the unix socket only.

Use `ndb backup` instead of calling it by hand; `ndb restore` reads the file. See [Backup and restore](../operations.md#backup-and-restore).

```bash
curl --unix-socket /run/ndb/ndb.sock http://ndb/_backup -d '{}' > ndb.ndb
```

## `POST /_restore`

`POST /{coll}/_restore` with the collection name in the body: `{"collection":"calls","at":"2026-11-12T14:04:00Z","to":"calls_restored"}`. Same answer and errors. See [PITR](../pitr.md).

## `GET /_pitr`

The default window and every collection with one, dropped ones included: `{"default":"1d","collections":[{"collection":"calls","window":"1d","oldest":"…",…}]}`. Partitions are left out: their logical collection stands for them.

## `POST /_blobs`

Stores bytes in the blob store (content addressed, deduplicated) and returns the reference to keep in a document field.

**Body:** the raw bytes. `Content-Type` is kept as the type of the blob.

**Answer:** `{"$blob":"sha256:<hex>","size":N,"type":"audio/wav"}`.

**Errors:** `413 request_too_large` over `blob_max_size` (default 1 GiB).

::: code-group

```bash [curl]
curl --unix-socket /run/ndb/ndb.sock http://ndb/_blobs \
  -H 'Content-Type: audio/wav' --data-binary @call.wav
# {"$blob":"sha256:9f86d0...","size":48213,"type":"audio/wav"}
```

```go [Go]
blob, err := db.PutBlob(ctx, file, "audio/wav")
```

```ruby [Ruby]
ref = db.put_blob(File.open("call.wav"), "audio/wav")
```

```ts [TypeScript]
const blob = await db.putBlob(bytes, 'audio/wav')
```

:::

A field of a document can hold the reference: `{"_id":"c1","recording":{"$blob":"sha256:..."}}`. The blob GC removes blobs that no document references any more.

## `GET /_blobs/{ref}`

The bytes of a blob. `Range: bytes=0-1023` reads a part (`206`). `?type=` sets the content type of the answer.

**Errors:** `404 not_found`, `400 invalid_blob_ref` when the reference is not `sha256:<64 hex>`.

::: code-group

```bash [curl]
curl --unix-socket /run/ndb/ndb.sock http://ndb/_blobs/sha256:9f86d0... > call.wav
```

```go [Go]
rc, err := db.GetBlob(ctx, blob) // io.ReadCloser
```

```ruby [Ruby]
bytes = db.get_blob("sha256:9f86d0...", from: 0, to: 1023)
```

```ts [TypeScript]
const bytes = await db.getBlob(blob) // Uint8Array
```

:::
