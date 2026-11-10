# HTTP API

The HTTP API is the contract of ndb with the world. Every SDK, the CLI and the console are built on it, and anything a client can see on the wire is written on these pages. A behavior that is not written here is a bug in the docs.

The routes have pages by group:

| Page | Routes |
|---|---|
| [System](http/system.md) | `/_health`, `/_metrics`, `/_collections`, `/_stats`, `/_size`, `/_backup`, `/_restore`, `/_pitr`, `/_blobs`, `/_log` |
| [Collections](http/collections.md) | `PUT`, `GET`, `DELETE /{coll}`, `_truncate`, `_rename`, `_restore`, `_pitr`, `_indexes`, `_stats`, `_compact`, `_quarantine` |
| [Documents](http/documents.md) | `_doc`, `_bulk`, `_update/{id}`, `_expire`, `_persist`, `_ttl` |
| [Queries](http/queries.md) | `_search`, `_count`, `_aggregate`, `_update_by_query`, `_delete_by_query`, patterns |
| [Watch](http/watch.md) | `_watch`, `_ack` |
| [Streams](http/streams.md) | `_streams`, `append`, `trim`, `groups`, `read`, `ack`, `nack`, `pending`, `claim` (Redis Streams; `nack` is ndb only) |
| [Errors](http/errors.md) | the error body and every code |

The query language (operators, date math, `$elemMatch`) is on [Query](query.md), the pipeline stages are on [Aggregate](aggregate.md), the events are on [Watch](watch.md), and the stream model is on [Streams](streams.md). These pages link there and do not repeat it.

## Rules

1. **`_` is the system namespace.** A path whose first segment starts with `_` is the daemon (`/_health`). Any other first segment is a collection name, which never starts with `_`. Data and system routes never collide.
2. **A document id always comes after an action:** `/{coll}/_doc/{id}`, `_update/{id}`, `_expire/{id}`, `_persist/{id}`, `_ttl/{id}`. An `_id` such as `"stats"` or `"_search"` is a normal id. Escape `/` in an id as `%2F`.
3. **Query is what matches; search is the whole request.** The filter is always the body key `query`: in `_search`, `_count`, `_watch`, `_update_by_query`, `_delete_by_query` and `_update/{id}`.
4. **The write operators go in `update`,** apart from `query`.
5. **Options go in the body** (`many`, `upsert`, `expire_in`). Only `confirm` is in the URL, on purpose, for drop and truncate.
6. **The version of a document is `_v`,** everywhere: rows, write results, watch events.
7. **No version in the path.** A breaking change ships as a major release, with a migration table in the CHANGELOG.

## Every route

| Method | Path | Page |
|---|---|---|
| GET | `/_health` | [System](http/system.md#get-health) |
| GET | `/_metrics` | [System](http/system.md#get-metrics) |
| GET | `/_collections` | [System](http/system.md#get-collections) |
| GET | `/_stats` | [System](http/system.md#get-stats) |
| GET | `/_size` | [System](http/system.md#get-size) |
| POST | `/_backup` | [System](http/system.md#post-backup) |
| POST | `/_restore` | [System](http/system.md#post-restore) |
| GET | `/_pitr` | [System](http/system.md#get-pitr) |
| POST | `/_blobs` | [System](http/system.md#post-blobs) |
| GET | `/_blobs/{ref}` | [System](http/system.md#get-blobs-ref) |
| GET | `/_log` | [System](http/system.md#get-log) |
| PUT | `/_log` | [System](http/system.md#put-log) |
| PUT | `/{coll}` | [Collections](http/collections.md#put-coll) |
| GET | `/{coll}` | [Collections](http/collections.md#get-coll) |
| DELETE | `/{coll}?confirm={coll}` | [Collections](http/collections.md#delete-coll) |
| POST | `/{coll}/_truncate?confirm={coll}` | [Collections](http/collections.md#post-coll-truncate) |
| POST | `/{coll}/_rename` | [Collections](http/collections.md#post-coll-rename) |
| POST | `/{coll}/_restore` | [Collections](http/collections.md#post-coll-restore) |
| GET | `/{coll}/_pitr` | [Collections](http/collections.md#get-coll-pitr) |
| PUT | `/{coll}/_indexes/{field}` | [Collections](http/collections.md#put-coll-indexes-field) |
| DELETE | `/{coll}/_indexes/{field}` | [Collections](http/collections.md#delete-coll-indexes-field) |
| GET | `/{coll}/_stats` | [Collections](http/collections.md#get-coll-stats) |
| POST | `/{coll}/_compact` | [Collections](http/collections.md#post-coll-compact) |
| GET | `/{coll}/_quarantine` | [Collections](http/collections.md#get-coll-quarantine) |
| POST | `/{coll}/_doc` | [Documents](http/documents.md#post-coll-doc) |
| POST | `/{coll}/_bulk` | [Documents](http/documents.md#post-coll-bulk) |
| GET | `/{coll}/_doc/{id}` | [Documents](http/documents.md#get-coll-doc-id) |
| DELETE | `/{coll}/_doc/{id}` | [Documents](http/documents.md#delete-coll-doc-id) |
| POST | `/{coll}/_update/{id}` | [Documents](http/documents.md#post-coll-update-id) |
| POST | `/{coll}/_expire/{id}` | [Documents](http/documents.md#post-coll-expire-id) |
| POST | `/{coll}/_persist/{id}` | [Documents](http/documents.md#post-coll-persist-id) |
| GET | `/{coll}/_ttl/{id}` | [Documents](http/documents.md#get-coll-ttl-id) |
| POST | `/{coll}/_search` | [Queries](http/queries.md#post-coll-search) |
| POST | `/{coll}/_count` | [Queries](http/queries.md#post-coll-count) |
| POST | `/{coll}/_aggregate` | [Queries](http/queries.md#post-coll-aggregate) |
| POST | `/{coll}/_update_by_query` | [Queries](http/queries.md#post-coll-update-by-query) |
| POST | `/{coll}/_delete_by_query` | [Queries](http/queries.md#post-coll-delete-by-query) |
| POST | `/{coll}/_watch` | [Watch](http/watch.md#post-coll-watch) |
| POST | `/{coll}/_ack` | [Watch](http/watch.md#post-coll-ack) |
| GET | `/{s}/_streams` | [Streams](http/streams.md#get-s-streams) |
| POST | `/{s}/_streams/append` | [Streams](http/streams.md#post-s-streams-append) |
| POST | `/{s}/_streams/trim` | [Streams](http/streams.md#post-s-streams-trim) |
| POST | `/{s}/_streams/read` | [Streams](http/streams.md#post-s-streams-read) |
| GET | `/{s}/_streams/groups` | [Streams](http/streams.md#get-s-streams-groups) |
| PUT | `/{s}/_streams/groups/{g}` | [Streams](http/streams.md#put-s-streams-groups-g) |
| DELETE | `/{s}/_streams/groups/{g}` | [Streams](http/streams.md#delete-s-streams-groups-g) |
| POST | `/{s}/_streams/groups/{g}/setid` | [Streams](http/streams.md#post-s-streams-groups-g-setid) |
| GET | `/{s}/_streams/groups/{g}/consumers` | [Streams](http/streams.md#get-s-streams-groups-g-consumers) |
| PUT | `/{s}/_streams/groups/{g}/consumers/{c}` | [Streams](http/streams.md#put-s-streams-groups-g-consumers-c) |
| DELETE | `/{s}/_streams/groups/{g}/consumers/{c}` | [Streams](http/streams.md#delete-s-streams-groups-g-consumers-c) |
| POST | `/{s}/_streams/groups/{g}/read` | [Streams](http/streams.md#post-s-streams-groups-g-read) |
| POST | `/{s}/_streams/groups/{g}/ack` | [Streams](http/streams.md#post-s-streams-groups-g-ack) |
| POST | `/{s}/_streams/groups/{g}/nack` | [Streams](http/streams.md#post-s-streams-groups-g-nack) (ndb only) |
| POST | `/{s}/_streams/groups/{g}/pending` | [Streams](http/streams.md#post-s-streams-groups-g-pending) |
| POST | `/{s}/_streams/groups/{g}/claim` | [Streams](http/streams.md#post-s-streams-groups-g-claim) |

Any other path is `404 unknown_route`. A known path with another method is `405 method_not_allowed`, with an `Allow` header.

## Transport

- **HTTP/1.1**, on a unix socket (`listen.unix`, default `/run/ndb/ndb.sock`) and, when `listen.tcp` is set, on TCP. Connections are kept alive; the SDKs keep a pool.
- On a unix socket the host name in the URL does not matter: `curl --unix-socket /run/ndb/ndb.sock http://ndb/_health`.
- The daemon has no TLS. Across machines, put it on a private network (WireGuard) or behind a TLS proxy. See [Deployment](deployment.md).
- **No browsers by default.** The daemon is for servers: apps, the console server, the CLI. A request with an `Origin` header, or with `Sec-Fetch-Site: cross-site` or `same-site`, is `403 forbidden` on every listener (CSRF guard). An origin in `cors_origins` gets the CORS headers (`Access-Control-Allow-Origin`, `Vary: Origin`) and a `204` answer to its preflight `OPTIONS`; any other `OPTIONS` is `405`.
- **Host.** On TCP with no token configured, the `Host` must be an IP, `localhost`, a name with no dot, or a name in `allowed_hosts`; else `403 forbidden` (DNS rebinding guard). With a token, and on the unix socket, the `Host` is not checked.
- **Limits.** `max_conns` limits the open TCP connections (a new one waits). `max_watches` limits the open `_watch` streams (`429 too_many_requests`, with `Retry-After`).

## Authentication

- With `-token-file` or `tokens:`, every TCP request needs `Authorization: Bearer <token>`, or the answer is `401 unauthorized`. The scheme is case-insensitive (`bearer` works too, RFC 9110 11.1); the token is exact.
- A scoped token without the right for the route and collection is `403 forbidden`. See [Scoped tokens](configuration.md#scoped-tokens).
- The unix socket needs no token: file permissions protect it.
- ndb serves plain HTTP; TLS comes from a proxy in front (Caddy). See [TLS](configuration.md#tls).
- `POST /_backup` works on the unix socket only; over TCP it is `403 forbidden`.
- After `auth_failures` failed token checks (default 10 in 1 min) one IP gets `429 too_many_requests` with `Retry-After` for `auth_lockout` (default 5 min), also with the right token. The unix socket has no lockout.

See [Authentication](configuration.md#authentication) and [Security](security.md): the threat model, the browser guards, TLS and the limits.

## Requests

- **Body:** one JSON object, UTF-8, `Content-Type: application/json`. A blob upload is raw bytes.
- **Unknown keys are errors** (`invalid_request` or `invalid_query`; `invalid_pipeline` on `_aggregate`), so a typo never passes silently.
- **Size:** a body is at most the `max_line_size` of the collection (default 1 MiB), else `413 request_too_large`. A system route has its own limit: `POST /_backup` takes at most 64 KiB, `POST /_restore` at most 1 MiB (the engine default), `PUT /_log` at most 4 KiB. A blob is at most `blob_max_size` (default 1 GiB).
- **Numbers:** JSON numbers. Queries and sorts compare them as 64-bit floats; an integer in a document keeps its exact text.
- **Times:** on the collection `time` field (and `expires_field`), RFC 3339 strings compare as instants, and a string that starts with `now` is [date math](query.md#date-math) (`now-7d/d`).
- **Ids:** `_id` is a string. A document without one gets a UUIDv7 (time ordered). See [Document id](configuration.md#document-id).
- **Collection names:** `^[a-z0-9][a-z0-9_-]{0,63}$`. A name with one `*` at the end is a [collection pattern](http/queries.md#collection-patterns), for reads.

## Answers

Every answer is JSON, except the rows of `_search` and `_aggregate`, the `_watch` stream, `/_metrics`, `/_backup` and blob bytes.

**Rows** (`_search`, `_aggregate`) are a JSON array by default. With `Accept: application/x-ndjson` they are NDJSON, one row per line, for exports and `jq`. The rows are the same in both formats; only the framing changes:

```
Accept: application/json (default)   [{"_id":"c1","status":"answered"},{"_id":"c2","status":"failed"}]
Accept: application/x-ndjson         {"_id":"c1","status":"answered"}
                                     {"_id":"c2","status":"failed"}
```

A row is the document. `_v` is added with `"versions": true`, and `_coll` on a row of a collection pattern.

**Page data** is in headers, in both formats, sent before the body:

| Header | When | Meaning |
|---|---|---|
| `X-Ndb-Next` | more rows | the cursor of the next page (`after`). Absent on the last page. |
| `X-Ndb-Total` | `"count": true` | how many rows match, whatever `limit` is |
| `X-Ndb-Plan` | always | `id`, `index:<field>`, `scan`, `snapshot` or `pattern` |
| `X-Ndb-Examined` | always | candidate rows whose query was evaluated |
| `X-Ndb-Took-Ms` | always | time spent by the daemon, in ms |
| `X-Ndb-Explain` | `"explain": true` | JSON of the plan counters, see [Search](query.md#search) |

**Request id:** every answer has `X-Request-Id`: the one of the request when it is 1 to 128 of `A-Z a-z 0-9 - _ . :`, else a new one. The log lines of the request have it as `request_id` (see [Logs](operations.md#logs)).

**Status codes:** `200` for every success, the codes of [Errors](http/errors.md) otherwise. A write that matches nothing is `200` with `"matched": 0`, not an error.

## Order and pagination

- `sort` is an object, `field: 1` or `-1`, in key order. `_id` is always the last tie-break, in the direction of the last sort key (`-1` gives `_id` descending), so the order is total. With no `sort`, the order is insertion order.
- `skip` + `limit` give offset pages; the cost grows with `skip`.
- `after` takes the `X-Ndb-Next` of the previous page. The cursor is opaque and bound to the `query` and `sort`: another query gets `cursor_mismatch`. Rows inserted during the walk do not move pages already read.
- `"snapshot": true` keeps the rows of the first page for 60 s of idle time, so every page sees the same data; after that, `cursor_expired`.
- A cursor keeps the `now` of its first page, so date math stays the same window across pages.

See [Pagination](query.md#pagination).

## Consistency

- **A write is acked after the WAL.** With `durability: strict` (the default) the WAL is synced to disk before the answer; with `relaxed` it is synced in the background. See [Durability](configuration.md#durability).
- **Read your writes:** a read that starts after a write was acked sees it.
- **One write is atomic:** a `_doc`, a `_bulk` (all or nothing), an `_update/{id}`. A `many` update or delete is atomic per WAL record (about half of `max_line_size` of documents), not as a whole.
- **Update and delete by query** match and write in one step, against the newest state: two concurrent updates never interleave. A guard in `query` (`{"_v": 7}`, `{"qty": {"$gte": 1}}`) makes a conditional write.
- **Collection patterns** read each collection at its own point in time; there is no snapshot over many collections.

## Readiness and retries

- The daemon listens before recovery ends. Until then every route except `/_health` and `/_metrics` answers `503 not_ready`, and `/_health` answers `503 {"status":"recovering"}`.
- On shutdown, new calls get `503 closed`; calls in flight finish.
- A client that leaves before the answer gets `499 canceled` in the logs and metrics.
- **Safe to retry:** every `GET`, `_search`, `_count`, `_aggregate`, and a write that certainly did not reach the daemon (`not_ready`, a refused connection).
- **Not safe to retry blindly:** a write whose connection dropped after it was sent; it may have been applied. Use an explicit `_id` (a second insert is `409 duplicate_id`) or a `_v` guard.
- The SDKs retry `not_ready` and refused connections for 10 s by default (`readyTimeout`), with backoff and jitter, and repeat a read whose connection dropped. They never repeat such a write.

## Long-lived requests

Two routes keep the request open. They are not the same feature.

**`_watch`** sends the changes of a collection as NDJSON, one event per line. It stays open until the client closes it, `limit` events were sent, or the daemon ends it.

- After 15 s with no event, the daemon writes a heartbeat line `{"heartbeat":true}`. Ignore it.
- The response ends on a truncate or a drop of the collection, on shutdown, and when the consumer is too slow (an error line `watch_lagged`). Reconnect: the consumer continues after its last `_ack`.
- An error after the response started is a line `{"error":{"code","message"}}`.
- See [Watch routes](http/watch.md).

**A stream read with `block_ms`** (`POST /{s}/_streams/groups/{g}/read`, or `POST /{s}/_streams/read` without a group) is a normal JSON request that waits up to `block_ms` for new entries. With none, the answer is `[]`. It sends no heartbeat.

- Set the client read timeout above `block_ms`. The SDKs do this for you.
- See [Stream routes](http/streams.md#post-s-streams-groups-g-read).

## Limits

| Limit | Value | Error |
|---|---|---|
| Body size | `max_line_size` of the collection, default 1 MiB | `413 request_too_large` |
| Blob size | `blob_max_size`, default 1 GiB | `413 request_too_large` |
| Collection name | 64 characters, `^[a-z0-9][a-z0-9_-]*$` | `400 catalog_invalid` |
| Collections of one pattern | 256 | `400 pattern_too_wide` |
| `$regex` pattern | 1 KiB | `400 invalid_query` |
| Group table of one aggregate | 256 MiB | `422 group_limit_exceeded` |
| Snapshot cursor idle time | 60 s | `400 cursor_expired` |
| Watch heartbeat | 15 s of silence | — |
| Open `_watch` streams | `max_watches`, default no limit | `429 too_many_requests` |
| Open TCP connections | `max_conns`, default no limit | a new connection waits |
| `_quarantine` `limit` | 1000 | — |
| Stream read or claim `count` | 10 000 | `400 invalid_request` |

## Versioning

There is no version in the path. The API follows the major version of ndb: a route or a key changes only in a major release, and the CHANGELOG has a table from the old form to the new one. Additions (a new route, a new optional key, a new header) can come in a minor release.
