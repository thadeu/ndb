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
      "bytes": {"wal": 805, "segments": 3965, "meta": 131072}, "total": 135842,
      "asleep": false
    }
  ],
  "totals": {"collections": 2, "asleep": 0, "rows": 7, "quarantined": 1, "segments": 1, "wal_files": 1, "bytes": 529315},
  "blobs_bytes": 0, "cursors_bytes": 131072, "shared_bytes": 262144
}
```

- `asleep` is a collection closed by `idle_close` (no call for 10 minutes by default): it holds no open file and no memory, and the next call opens it again. Its `rows`, `indexes` and `segments` are as of when it slept; reading `_stats` does not open it.
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

**Body:** the raw bytes. `Content-Type` goes into the reference as its `type`.

**Answer:** `{"$blob":"sha256:<hex>","size":N,"type":"audio/wav"}`. The answer has `type` only when the request sent a `Content-Type`.

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

The reference is `sha256:<64 hex>` or the bare `<64 hex>` (lowercase hex in both forms).

**Errors:** `404 not_found`, `400 invalid_blob_ref` when the reference is not one of these forms, `416 invalid_range` when the `Range` is out of the blob (the error body of every route, with `Content-Range: bytes */<size>`).

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

## `GET /_cluster/settings`

The settings of the daemon that live in its data dir, not in `ndb.yml`: the encryption key and the log settings. Each setting has its value and where it comes from. Over TCP it needs a token with admin of `*`.

```bash
curl --unix-socket /run/ndb/ndb.sock http://ndb/_cluster/settings
```

```json
{
  "encryption": {"key": "e5af****…****edff", "key_id": "k1", "fingerprint": "eca62a9e574bcd0a", "source": "made", "created_at": "2026-11-11T23:03:03Z"},
  "log": {
    "level": {"value": "info", "source": "default"},
    "body_max": {"value": 2048, "source": "default"},
    "slow_ms": {"value": 1000, "source": "yml"}
  }
}
```

| `source` | Meaning |
|---|---|
| `flag`, `env`, `yml` | set by a flag, the env (`NDB_LOG_LEVEL`) or `ndb.yml` at start: it wins over a stored value at the next start |
| `stored` | set by `PUT /_cluster/settings`, kept in the data dir |
| `default` | the default |
| `made`, `stored`, `env` (the key) | the key was made by this daemon, read from the data dir, or given in `NDB_ENCRYPTION_KEY` |

The key is never in an answer: only its first and last 4 hex characters, and a fingerprint (the first 8 bytes of its SHA-256). See [The encryption key](../security.md#the-encryption-key).

During a [key rotation](../encryption.md#key-rotation), `encryption` also has `"rotation":{"from":"k1","pending":["users","events"]}`: the old key, and the collections that still have values of it. It goes away when the rotation ends.

## `PUT /_cluster/settings`

Changes settings at run time and stores them in the data dir, so they hold across a restart and go in the backups. Today the settings are the log ones:

```bash
curl --unix-socket /run/ndb/ndb.sock -X PUT http://ndb/_cluster/settings -d '{"log":{"level":"debug","slow_ms":250}}'
```

| Body key | Type | Meaning |
|---|---|---|
| `log.level` | string | `debug`, `info`, `warn`, `error` or `quiet` |
| `log.body_max` | int | the largest body or document in a log line, in bytes |
| `log.slow_ms` | int | a request over this is a `warn` line |

**Answer:** the settings, as `GET`. When `ndb.yml`, the env or a flag also sets a setting you changed, `warnings` says that it wins at the next start. **Errors:** `400 invalid_request` for an unknown key, a bad value, or `encryption` (the key changes with [`POST /_cluster/key`](#post-cluster-key)); `403 forbidden` over TCP without admin of `*`.

`PUT /_log` changes the same log settings until the daemon stops, with nothing stored.

## `POST /_cluster/key`

Starts a [key rotation](../encryption.md#key-rotation). **On the unix socket only:** over TCP it is `403 forbidden`, also with the full token, so only a user with a shell on the host rotates the key. The CLI is `ndb key rotate`. No SDK has it.

| Body | What it does |
|---|---|
| `{}` | For a key kept in the data dir: a new key `k<n+1>` is the current key at once, and every encrypted value is sealed again in the background. **Answer:** `{"key_id":"k2","from":"k1"}`. |
| `{"env":true}` | Makes the key for `NDB_ENCRYPTION_KEY` and keeps only its fingerprint. The rotation starts at the next start with that key. **Answer:** `{"key":"<64 hex>","key_id":"k2","from":"k1"}`: the only time the key is shown. |

**Errors:** `400 invalid_request` for `{}` when the key comes from `NDB_ENCRYPTION_KEY` (use `{"env":true}`); `403 forbidden` over TCP; `409 key_rotating` while a rotation is not finished.

## `GET /_log`

The log settings of the daemon: `{"level":"info","format":"text","body_max":2048,"slow_ms":1000}`. Over TCP it needs a token with admin of `*`, else `403 forbidden`. See [Logs](../operations.md#logs).

```bash
curl --unix-socket /run/ndb/ndb.sock http://ndb/_log
```

## `PUT /_log`

Changes the log settings at run time, without a restart. Every key is optional. The change lasts until the daemon stops; `ndb.yml` keeps the settings of the next start.

| Body key | Type | Meaning |
|---|---|---|
| `level` | string | `debug`, `info`, `warn`, `error` or `quiet` |
| `body_max` | int | the largest body or document in a log line, in bytes; `0` logs no bodies |
| `slow_ms` | int | a request over this is a `warn` line; `0` turns it off |

The format (`text` or `json`) stays the one of the start.

**Answer:** the new settings, as `GET /_log`. **Errors:** `400 invalid_request` for an unknown level, a negative number or an unknown key (`format` too); `403 forbidden` over TCP without admin of `*`.

```bash
curl --unix-socket /run/ndb/ndb.sock -X PUT http://ndb/_log -d '{"level":"debug","body_max":512}'
# {"body_max":512,"format":"text","level":"debug","slow_ms":1000}
```
