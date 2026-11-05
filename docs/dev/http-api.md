# HTTP API

HTTP/1.1 and JSON on a unix socket, and on TCP when `listen.tcp` is set. With `-token-file`, every TCP request needs `Authorization: Bearer <token>` (else `401 unauthorized`); the unix socket needs none. Debug with `curl --unix-socket /run/ndb/ndb.sock http://ndb/health`. The host name in the URL does not matter on a unix socket.

Paths start with `/v1`. `/health` and `/metrics` have no version. Every data endpoint answers `503 not_ready` until recovery ends.

| Method | Path | Body | Answer |
|---|---|---|---|
| POST | `/v1/c/{coll}/find` | find query | NDJSON rows + `X-Ndb-*` headers |
| POST | `/v1/c/{coll}/aggregate` | `{"pipeline":[...]}` | NDJSON rows + `X-Ndb-*` headers |
| POST | `/v1/c/{coll}/insert` | `{"insert":[{...},...],"expire_in"}` | `{"results":[{"id","v"}]}` |
| POST | `/v1/c/{coll}/update` | `{"update":{"id","set","unset","if_version","expire_in"}}` | `{"id","v"}` |
| POST | `/v1/c/{coll}/expire` | `{"id","in"\|"at","if_version"}` | `{"id","v","expires_at"}` |
| POST | `/v1/c/{coll}/persist` | `{"id"}` | `{"id","v"}` |
| GET | `/v1/c/{coll}/ttl/{id}` | none | `{"id","ttl","expires_at"}` |
| POST | `/v1/c/{coll}/delete` | `{"delete":{"id","if_version"}}` | `{"id"}` |
| POST | `/v1/c/{coll}/watch` | `{"consumer","from","limit","where"}` | NDJSON stream |
| POST | `/v1/c/{coll}/ack` | `{"consumer","pos"}` | `{"ok":true}` |
| PUT | `/v1/c/{coll}` | collection definition | `{"definition","indexes"}` |
| GET | `/v1/c` | none | `{"collections":[...]}` |
| DELETE | `/v1/c/{coll}/indexes/{field}` | none | `{"collection","indexes"}` |
| GET | `/v1/c/{coll}` | none | `{"definition","indexes"}` |
| GET | `/v1/c/{coll}/stats` | none | rows, quarantined, index state, files and bytes |
| POST | `/v1/c/{coll}/compact` | none | before and after segments and bytes |
| GET | `/v1/c/{coll}/quarantine?limit=N` | none | the last N bad lines of a direct collection |
| GET | `/v1/stats` | none | every collection, totals, version, uptime |
| GET | `/v1/size` | none | disk usage per collection and per part |
| PUT | `/v1/blobs` | raw bytes | the `$blob` reference |
| GET | `/v1/blobs/{ref}` | none | the bytes; supports `Range` |
| POST | `/v1/backup` | `{"dest":"/path"}` | unix socket only |
| GET | `/health` | none | `{"status":"ok"}`, or 503 `recovering` |
| GET | `/metrics` | none | Prometheus text |

Details:

- Request bodies are limited by the collection `max_line_size` (default 1 MiB). Unknown JSON fields are `invalid_request`.
- `insert` creates an unknown collection (unless `allow_implicit: false`). A document without the id field gets a generated id. Insert on a direct-mode collection is `direct_collection`.
- `update` merges `set` (dot paths allowed) and removes the paths in `unset`. The id cannot change (`immutable_id`). `if_version` makes the write conditional: a different current version is `version_conflict`.
- `PUT /v1/c/{coll}` is idempotent. Keys: `id`, `time`, `write`, `path`, `indexes`, `unique`, `durability`, `retention`, `wal_retention`, `ttl`, `expires_field`, `max_line_size`, `blob_threshold` (bytes). Merge rules: `indexes` are united; a different `id`, `write`, `path` or `time` is `catalog_conflict`; `ndb.yml` wins for `durability`, `retention` and `wal_retention`.
- `GET /v1/c/{coll}` answers like `PUT`: the final `definition` and the state of each index. An unknown name is `404 unknown_collection`.
- Errors are described in [query.md](query.md#errors).
- `where` operators are in [query.md](query.md#operators). `{"field": {"$regex": "^abc", "$options": "i"}}` matches strings with Go RE2 (no backreferences, no look-around; flags `i`, `m`, `s`, `x`); a bad pattern or flag is `400 invalid_query`. An anchored literal prefix without `i`/`m` can use an index and zone maps, see [Regular expressions](query.md#regular-expressions).

## Expiry (TTL like Redis)

These calls need a collection with `expires_field`. They are normal writes: the daemon sets or removes that field in one transaction, so the document gets a new version, a WAL record and a `watch` event. See [configuration.md](configuration.md#ttl-like-redis) for the model and the disk notes.

- `expire_in` is a Go duration string (`"90m"`, `"1h30m"`, plus `d` and `w`: `"1d"`) or a positive number of seconds. On `insert` it is a field of the body and applies to every document of the call. On `update` it is a field of the `update` object. The daemon sets `expires_field` to now + `expire_in` (RFC 3339 UTC). **`expire_in` wins:** a value of `expires_field` in the document, in `set`, or in `unset` of the same call is ignored.
- `POST /v1/c/{coll}/expire` takes `id` and exactly one of `in` (same format as `expire_in`) or `at` (RFC 3339; a time in the past expires the document at once). `if_version` is optional. It answers `{"id","v","expires_at"}`. A missing or already expired document is `404 not_found`.
- `POST /v1/c/{coll}/persist` takes `{"id"}`, removes `expires_field`, and answers `{"id","v"}`. A missing or expired document is `404 not_found`.
- `GET /v1/c/{coll}/ttl/{id}` answers `{"id","ttl","expires_at"}`. `ttl` is the whole seconds left, rounded up. A document that never expires has `"ttl": -1` and `"expires_at": null`. A missing or expired document is `404 not_found`. With `ttl` + `time` in the catalog the expiry is computed from the time field; if both `ttl` and `expires_field` are set, the earlier time counts.
- `400 ttl_not_enabled`: the collection has no `expires_field`. `400 ttl_derived`: the collection only has `ttl` + `time`, so the expiry comes from the time field and cannot be set or removed (`expire_in`, `expire`, `persist`). `ttl` still works. A bad `expire_in`, or an `expire` without exactly one of `in` and `at`, is `400 invalid_request`.
- Direct-mode collections: the write calls are `409 direct_collection`, and `ttl` is `400 not_supported`.

```bash
curl --unix-socket /run/ndb/ndb.sock -X PUT http://ndb/v1/c/sessions -d '{"id":"sid","expires_field":"exp"}'
curl --unix-socket /run/ndb/ndb.sock http://ndb/v1/c/sessions/insert -d '{"insert":[{"sid":"s1"}],"expire_in":"1h"}'
curl --unix-socket /run/ndb/ndb.sock http://ndb/v1/c/sessions/expire -d '{"id":"s1","in":"30m"}'
curl --unix-socket /run/ndb/ndb.sock http://ndb/v1/c/sessions/ttl/s1
curl --unix-socket /run/ndb/ndb.sock http://ndb/v1/c/sessions/persist -d '{"id":"s1"}'
```

## Stats, size and admin

The CLI (`ndb stats`, `ndb size`, `ndb collections`, `ndb compact`, `ndb quarantine`) only calls these endpoints, so a web console can do the same. All of them follow the token rules above (TCP needs the bearer token).

`GET /v1/stats` has one element per collection, totals, and the files that belong to no collection:

```json
{
  "version": "0.2.0",
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

- `GET /v1/c/{coll}/stats` is one element of that list. It keeps `name`, `write`, `rows`, `quarantined` and `indexes` (each `building` or `ready`, with `done` and `total`), and adds `segments`, `wal_files` (file counts on disk), `bytes` and `total`.
- `segments` and `wal_files` count files, not live rows. A direct collection has no segments and no WAL: its `meta` is `meta.bolt` and `quarantine/`.
- `totals.bytes` includes `shared_bytes` (blobs, `cursors.bolt`, catalog and other files of the data dir).
- `GET /v1/size` is the disk scan of `ndb size`: `{"data_dir","total","collections":[{"name","bytes":{"wal","segments","meta","blobs","cursors"},"total","wal_files","segment_files"}],"shared":{...}}`. It only stats files.
- `POST /v1/c/{coll}/compact` seals the memtable, then merges every segment into one, and waits for the end: `{"collection","before":{"segments","bytes"},"after":{"segments","bytes"},"duration_ms"}`. A direct collection is `400 not_supported`. Compaction is also done by the sealer in the background; see [operations.md](operations.md#compaction-retention-and-ttl).
- `GET /v1/c/{coll}/quarantine?limit=N` returns `{"collection","total","rows":[{"ts","file","offset","error","line","truncated"}]}`: the last `limit` bad lines (default 100, at most 1000), oldest first. `total` is how many distinct lines the quarantine holds; a line that was quarantined again by a restart counts once. `line` is cut to 2 KiB. An engine-mode collection has no quarantine: `rows` is `[]`. A `limit` that is not a positive integer is `400 invalid_request`.

## watch and ack

`watch` is a long-lived stream, one JSON object per line. `from` is `beginning` or `now` and is used only for a consumer with no saved position. A consumer that acked before resumes after its last ack.

```json
{"pos":"e1.1","op":"put","id":"c1","v":1,"doc":{"_id":"c1","status":"answered"}}
{"pos":"e1.2","op":"del","id":"c2","v":2}
{"heartbeat":true}
{"error":{"code":"watch_lagged","message":"..."}}
```

- `op` is `put` or `del`. A delete by the TTL sweeper has `"_expired": true`.
- A heartbeat line comes after 15 s without events.
- `where` has the same operators as `find`. A `put` is sent when its document matches. A `del` has no document, so it is sent only if the last `put` of that id seen by this stream matched (the stream remembers the last 65 536 ids). Events that are left out still move the saved position.
- `limit` closes the stream after that many events (0 = no limit).
- `ack` stores the `pos` of an event the consumer has processed. Delivery is at least once.
- `watch_lagged` means the consumer was too slow. Reconnect: the stream resumes from the last ack.
- `history_truncated` (HTTP 410) means the WAL files that the watch must read were deleted by `wal_retention` (no saved position with `from: "beginning"`, or a position before the oldest kept file). The stream never skips the gap in silence. Start from `now`, or rebuild from `find`. A saved position is safe: ndb keeps the file that holds it.

## Backup endpoint

`POST /v1/backup` writes to the disk of the daemon host, so it works on the unix socket only. Over TCP the answer is `403 forbidden`. Use `ndb backup` instead of calling it by hand. See [operations.md](operations.md#backup-and-restore).

## Blobs

`PUT /v1/blobs` with the raw body returns the reference to keep in a document: `{"$blob":"sha256:<hex>","size":N,"type":"..."}`. `GET /v1/blobs/sha256:<hex>` streams the content (`?type=` sets the content type). The limit is `blob_max_size` (default 1 GiB).
