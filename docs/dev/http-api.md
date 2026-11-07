# HTTP API

HTTP/1.1 and JSON on a unix socket, and on TCP when `listen.tcp` is set. With `-token-file`, every TCP request needs `Authorization: Bearer <token>` (else `401 unauthorized`); the unix socket needs none. Debug with `curl --unix-socket /run/ndb/ndb.sock http://ndb/health`. The host name in the URL does not matter on a unix socket.

Paths start with `/v1`. `/health` and `/metrics` have no version. Every data endpoint answers `503 not_ready` until recovery ends.

| Method | Path | Body | Answer |
|---|---|---|---|
| POST | `/v1/c/{coll}/find` | find query | NDJSON rows + `X-Ndb-*` headers |
| POST | `/v1/c/{coll}/aggregate` | `{"pipeline":[...]}` | NDJSON rows + `X-Ndb-*` headers |
| POST | `/v1/c/{coll}/insert` | `{"insert":[{...},...],"expire_in"}` | `{"results":[{"_id","v"}]}` |
| POST | `/v1/c/{coll}/update?many&upsert&expire_in` | filter and write operators in one object | `{"matched","modified","doc"}` |
| GET | `/v1/c/{coll}/{_id}` | none | the document with `_v` |
| POST | `/v1/c/{coll}/{_id}/update?upsert&expire_in` | write operators, plus optional guard filters | `{"matched","modified","doc"}` |
| DELETE | `/v1/c/{coll}/{_id}` | none | `{"deleted"}` |
| POST | `/v1/c/{coll}/expire` | `{"_id","in"\|"at"}` | `{"_id","v","expires_at"}` |
| POST | `/v1/c/{coll}/persist` | `{"_id"}` | `{"_id","v"}` |
| GET | `/v1/c/{coll}/{_id}/ttl` | none | `{"_id","ttl","expires_at"}` |
| POST | `/v1/c/{coll}/delete?many` | a filter | `{"deleted"}` |
| POST | `/v1/c/{coll}/watch` | `{"consumer","from","limit","where"}` | NDJSON stream |
| POST | `/v1/c/{coll}/ack` | `{"consumer","pos"}` | `{"ok":true}` |
| PUT | `/v1/c/{coll}` | collection definition | `{"definition","indexes"}` |
| GET | `/v1/c` | none | `{"collections":[...]}` |
| DELETE | `/v1/c/{coll}/indexes/{field}` | none | `{"collection","indexes"}` |
| GET | `/v1/c/{coll}` | none | `{"definition","indexes"}` |
| GET | `/v1/c/{coll}/stats` | none | rows, quarantined, index state, files and bytes |
| POST | `/v1/c/{coll}/compact` | none | before and after segments and bytes |
| POST | `/v1/c/{coll}/truncate?confirm={coll}` | none | `{"collection","truncated":true}` |
| DELETE | `/v1/c/{coll}?confirm={coll}` | none | `{"collection","dropped":true}` |
| GET | `/v1/c/{coll}/quarantine?limit=N` | none | the last N bad lines of a direct collection |
| GET | `/v1/stats` | none | every collection, totals, version, uptime |
| GET | `/v1/size` | none | disk usage per collection and per part |
| PUT | `/v1/blobs` | raw bytes | the `$blob` reference |
| GET | `/v1/blobs/{ref}` | none | the bytes; supports `Range` |
| POST | `/v1/backup` | `{"no_blobs":false}` | the `.ndb` stream; unix socket only |
| GET | `/health` | none | `{"status":"ok"}`, or 503 `recovering` |
| GET | `/metrics` | none | Prometheus text |

Details:

- Request bodies are limited by the collection `max_line_size` (default 1 MiB). Unknown JSON fields are `invalid_request`.
- `/{_id}` routes: a fixed route (`find`, `stats`, `insert`...) wins over an `_id` with the same name; reach such a document with `find`. A direct-mode document without `_id` is never found by them (404). `/{_id}/update` and `DELETE /{_id}` are engine mode only.
- `insert` creates an unknown collection (unless `allow_implicit: false`). A document without `_id` gets a generated UUIDv7 (see [Document id](configuration.md#document-id)). Insert on a direct-mode collection is `direct_collection`.
- `update` and `delete` are [atomic update by filter](query.md#update-by-filter).
- `PUT /v1/c/{coll}` is idempotent. Keys: `time`, `write`, `path`, `indexes`, `unique`, `durability`, `retention`, `wal_retention`, `ttl`, `expires_field`, `max_line_size`, `blob_threshold` (bytes). Merge rules: `indexes` are united; a different `write`, `path` or `time` is `catalog_conflict`; `id` is `catalog_invalid` (see [Document id](configuration.md#document-id)); `ndb.yml` wins for `durability`, `retention` and `wal_retention`.
- `GET /v1/c/{coll}` answers like `PUT`: the final `definition` and the state of each index. An unknown name is `404 unknown_collection`.
- Errors are described in [Errors](#errors).
- `where` operators are in [Filters](query.md#filters). `{"field": {"$regex": "^abc", "$options": "i"}}` matches strings with Go RE2 (no backreferences, no look-around; flags `i`, `m`, `s`, `x`); a bad pattern or flag is `400 invalid_query`. An anchored literal prefix without `i`/`m` can use an index and zone maps, see [Regular expressions](query.md#regular-expressions).

## Update and delete

`update` and `delete` take a filter, and `update` also takes write operators (`$set`, `$setOnInsert`, `$unset`, `$inc`, `$push`, `$pull`, `$min`, `$max`) in the same body. See [Update by filter](query.md#update-by-filter) and [Delete by filter](query.md#delete-by-filter).

## Expiry (TTL like Redis)

These calls need a collection with `expires_field`. They are normal writes: the daemon sets or removes that field in one transaction, so the document gets a new version, a WAL record and a `watch` event. See [configuration.md](configuration.md#ttl-like-redis) for the model and the disk notes.

- `expire_in` is a Go duration string (`"90m"`, `"1h30m"`, plus `d` and `w`: `"1d"`) or a positive number of seconds. On `insert` it is a field of the body and applies to every document of the call. On `update` it is the query parameter `expire_in`. The daemon sets `expires_field` to now + `expire_in` (RFC 3339 UTC). **`expire_in` wins:** a value of `expires_field` in the document or in the operators of the same call is ignored.
- `POST /v1/c/{coll}/expire` takes `_id` and exactly one of `in` (same format as `expire_in`) or `at` (RFC 3339; a time in the past expires the document at once). It answers `{"_id","v","expires_at"}`. A missing or already expired document is `404 not_found`.
- `POST /v1/c/{coll}/persist` takes `{"_id"}`, removes `expires_field`, and answers `{"_id","v"}`. A missing or expired document is `404 not_found`.
- `GET /v1/c/{coll}/{_id}/ttl` answers `{"_id","ttl","expires_at"}`. `ttl` is the whole seconds left, rounded up. A document that never expires has `"ttl": -1` and `"expires_at": null`. A missing or expired document is `404 not_found`. With `ttl` + `time` in the catalog the expiry is computed from the time field; if both `ttl` and `expires_field` are set, the earlier time counts.
- `400 ttl_not_enabled`: the collection has no `expires_field`. `400 ttl_derived`: the collection only has `ttl` + `time`, so the expiry comes from the time field and cannot be set or removed (`expire_in`, `expire`, `persist`). `ttl` still works. A bad `expire_in`, or an `expire` without exactly one of `in` and `at`, is `400 invalid_request`.
- Direct-mode collections: the write calls are `409 direct_collection`, and `ttl` is `400 not_supported`.

```bash
curl --unix-socket /run/ndb/ndb.sock -X PUT http://ndb/v1/c/sessions -d '{"expires_field":"exp"}'
curl --unix-socket /run/ndb/ndb.sock http://ndb/v1/c/sessions/insert -d '{"insert":[{"_id":"s1"}],"expire_in":"1h"}'
curl --unix-socket /run/ndb/ndb.sock http://ndb/v1/c/sessions/expire -d '{"_id":"s1","in":"30m"}'
curl --unix-socket /run/ndb/ndb.sock http://ndb/v1/c/sessions/s1/ttl
curl --unix-socket /run/ndb/ndb.sock http://ndb/v1/c/sessions/persist -d '{"_id":"s1"}'
```

## Stats, size and admin

The CLI (`ndb stats`, `ndb size`, `ndb collections`, `ndb compact`, `ndb truncate`, `ndb drop`, `ndb quarantine`) only calls these endpoints, so a web console can do the same. All of them follow the token rules above (TCP needs the bearer token).

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
- `POST /v1/c/{coll}/truncate?confirm={coll}` removes every document and keeps the definition and the indexes. `DELETE /v1/c/{coll}?confirm={coll}` drops the collection: its documents, its definition and the positions of its watch consumers. See [Truncate and drop](operations.md#truncate-and-drop).
  - `confirm` must be the collection name, or the answer is `400 confirm_required`. A direct collection is `409 direct_collection`, and an unknown one is `404 unknown_collection`.
  - Both take the same time for 10 or 10 million documents (a few ms). Calls on the collection that are running finish first; new calls wait for the swap, they do not fail.
- `GET /v1/c/{coll}/quarantine?limit=N` returns `{"collection","total","rows":[{"ts","file","offset","error","line","truncated"}]}`: the last `limit` bad lines (default 100, at most 1000), oldest first. `total` is how many distinct lines the quarantine holds; a line that was quarantined again by a restart counts once. `line` is cut to 2 KiB. An engine-mode collection has no quarantine: `rows` is `[]`. A `limit` that is not a positive integer is `400 invalid_request`.

## watch and ack

`watch` is a long-lived NDJSON stream of the changes of a collection, for a named consumer, and `ack` stores its position. See [Watch](watch.md).

## Backup endpoint

`POST /v1/backup` streams the backup file (NDJSON, `application/x-ndjson`) taken at a mark between two commits. It works on the unix socket only. Over TCP the answer is `403 forbidden`. Use `ndb backup` instead of calling it by hand. See [operations.md](operations.md#backup-and-restore).

## Blobs

`PUT /v1/blobs` with the raw body returns the reference to keep in a document: `{"$blob":"sha256:<hex>","size":N,"type":"..."}`. `GET /v1/blobs/sha256:<hex>` streams the content (`?type=` sets the content type). The limit is `blob_max_size` (default 1 GiB).

## Errors

The body is `{"error":{"code":"...","message":"..."}}`.

| Code | HTTP | Meaning |
|---|---|---|
| `invalid_request` | 400 | Bad body, unknown field, empty `insert`. |
| `invalid_query` | 400 | Bad `where`, `sort`, operator or key. |
| `invalid_pipeline` | 400 | Bad aggregate pipeline. |
| `cursor_invalid` | 400 | The `after` token is broken. |
| `cursor_mismatch` | 400 | The token was made for another query. |
| `cursor_expired` | 400 | The snapshot of the cursor expired. |
| `group_limit_exceeded` | 422 | The group table is over its memory limit. |
| `not_found` | 404 | No document (or blob) with this id. |
| `unknown_collection` | 404 | The collection does not exist. |
| `unknown_operator`, `no_update`, `conflicting_update`, `invalid_field`, `unindexed_many`, `empty_filter` | 400 | The body of an update or delete breaks the [contract](query.md#contract-errors). |
| `duplicate_id` | 409 | A document with this id exists. |
| `unique_violation` | 409 | A `unique` field value exists. |
| `confirm_required` | 400 | `truncate` or `drop` without `confirm` equal to the collection name. |
| `catalog_conflict` | 409 | Two declarations of the collection disagree (`write`, `path`, `time`). |
| `direct_collection` | 409 | Write call on a direct-mode collection. |
| `catalog_invalid` | 400 | The collection definition is not valid (for example, it has `id`). |
| `ttl_not_enabled` | 400 | `expire_in`, `expire` or `persist` on a collection without `expires_field`. |
| `ttl_derived` | 400 | `expire_in`, `expire` or `persist` on a collection whose expiry comes from `ttl` + `time`. |
| `invalid_doc`, `missing_id` (a direct-mode tombstone without `_id`), `invalid_id`, `immutable_id`, `invalid_batch`, `invalid_path`, `invalid_index` | 400 | A document or definition is not valid. |
| `request_too_large`, `line_too_large` | 413 | Body or line is over `max_line_size` (or the blob limit). |
| `invalid_blob_ref` | 400 | The blob reference is not `sha256:<64 hex>`. |
| `unauthorized` | 401 | TCP request without the right bearer token (`-token-file`). |
| `forbidden` | 403 | For example, backup over TCP. |
| `history_truncated` | 410 | A `watch` needs WAL files that `wal_retention` deleted. |
| `not_ready` | 503 | The daemon is still recovering. Retry. |
| `closed` | 503 | The daemon is shutting down. |
| `canceled` | 499 | The client left before the answer. |
| `internal` | 500 | A bug or disk error. The message stays in the daemon log. |

A `watch` stream reports `watch_lagged` and `invalid_position` as an error line in the stream. See [Watch](watch.md#errors).
