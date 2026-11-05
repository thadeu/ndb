# HTTP API

HTTP/1.1 and JSON on a unix socket, and on TCP when `listen.tcp` is set. With `-token-file`, every TCP request needs `Authorization: Bearer <token>` (else `401 unauthorized`); the unix socket needs none. Debug with `curl --unix-socket /run/ndb/ndb.sock http://ndb/health`. The host name in the URL does not matter on a unix socket.

Paths start with `/v1`. `/health` and `/metrics` have no version. Every data endpoint answers `503 not_ready` until recovery ends.

| Method | Path | Body | Answer |
|---|---|---|---|
| POST | `/v1/c/{coll}/find` | find query | NDJSON rows + `X-Ndb-*` headers |
| POST | `/v1/c/{coll}/aggregate` | `{"pipeline":[...]}` | NDJSON rows + `X-Ndb-*` headers |
| POST | `/v1/c/{coll}/insert` | `{"insert":[{...},...]}` | `{"results":[{"id","v"}]}` |
| POST | `/v1/c/{coll}/update` | `{"update":{"id","set","unset","if_version"}}` | `{"id","v"}` |
| POST | `/v1/c/{coll}/delete` | `{"delete":{"id","if_version"}}` | `{"id"}` |
| POST | `/v1/c/{coll}/watch` | `{"consumer","from","limit","where"}` | NDJSON stream |
| POST | `/v1/c/{coll}/ack` | `{"consumer","pos"}` | `{"ok":true}` |
| PUT | `/v1/c/{coll}` | collection definition | `{"definition","indexes"}` |
| GET | `/v1/c` | none | `{"collections":[...]}` |
| DELETE | `/v1/c/{coll}/indexes/{field}` | none | `{"collection","indexes"}` |
| GET | `/v1/c/{coll}/stats` | none | rows, quarantined, index state |
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
- `stats` shows `rows`, `quarantined` and each index as `building` or `ready`, with `done` and `total`.
- Errors are described in [query.md](query.md#errors).
- `where` operators are in [query.md](query.md#operators). `{"field": {"$regex": "^abc", "$options": "i"}}` matches strings with Go RE2 (no backreferences, no look-around; flags `i`, `m`, `s`, `x`); a bad pattern or flag is `400 invalid_query`. An anchored literal prefix without `i`/`m` can use an index and zone maps, see [Regular expressions](query.md#regular-expressions).

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
