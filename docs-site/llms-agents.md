## For AI agents: read this first

ndb is one Go daemon. It stores documents (JSON objects) in collections and answers HTTP/JSON over a unix socket or TCP. Every SDK (Go, Ruby, Rails, TypeScript) and the CLI are thin layers over the same HTTP API: when an SDK and the HTTP docs differ, the HTTP docs are the contract.

### Run and connect

- `ndb serve` with no flags (not as root): data in `~/.ndb/data`, unix socket `~/.ndb/ndb.sock`, TCP `127.0.0.1:7070`. As root, or with `/var/lib/ndb`: socket `/run/ndb/ndb.sock`, no TCP.
- The SDKs read `NDB_URL` (`unix:///path/ndb.sock`, `http://host:port`). Over TCP with a token: `Authorization: Bearer <token>`. The unix socket needs no token.
- `GET /_health` is `{"status":"ok"}` when ready; `503 not_ready` while it recovers. The SDKs retry that for 10 s.
- Logs: `ndb serve -log-level debug|info|warn|error|quiet`, or `PUT /_log {"level":"debug"}` with an admin token.

### Rules that agents get wrong

- **Collection names** match `^[a-z0-9][a-z0-9_-]{0,63}$`. A path segment that starts with `_` is the daemon, never a collection.
- **`_id`** is the primary key, a non-empty string; ndb makes a UUIDv7 when it is missing. An existing `_id` on insert is `409 duplicate_id`.
- **`_v`** is the version, set by the daemon and added to every answer. A `_v` that you send in a document is dropped. To write only on a given version, use the guard: `POST /{coll}/_update/{id}` with `{"query":{"_v":3},"update":{...}}` (no match = `{"matched":0}`). The SDKs wrap it in `with_retry` (`WithRetry`, `withRetry`): read, change in a function, write with the guard, and retry on a conflict (`stale_object` after 5 tries); the function can run more than once.
- **`$unwind`** in `_aggregate`, before `$group`: `{"$unwind": "$items"}` makes one row per array element (MongoDB rules; options `preserveNullAndEmptyArrays`, `includeArrayIndex`). One per pipeline; a `$match` after it filters the elements.
- **Encrypted fields:** `PUT /users {"encrypted":["cpf"]}`: the daemon stores `"ndb:e1:k1:..."` (AES-256-GCM), never the plain value, also in logs. The unix socket, the full token and a token with `decrypt: [users]` read the plain value; other tokens read the sealed string. Only `$set`/`$unset` change the field; a filter on its value finds nothing (blind index: next).
- **Settings:** `GET` and `PUT /_cluster/settings` (admin): the log settings, stored in the data dir (`{"log":{"level":"debug"}}`), and the encryption key, masked. The key comes from `NDB_ENCRYPTION_KEY` (64 hex), or is made at the first start and kept in the data dir and the backups.
- **Indexes:** `indexes: ["status", "account_id,created_at"]`. A name with commas is a compound index (2 to 4 fields): it serves equality on the first fields, then a range on the next one; it does not order the rows. `unique: ["account_id,email"]` is a compound unique key. `X-Ndb-Plan` names the index used.
- **Optimistic locking (the default):** `_v` needs no setup. Rails models guard every save with it (`Ndb::StaleObjectError`); in Go, TypeScript and Ruby pass it as a guard or use `with_retry`. Overview of optimistic locking, pessimistic locking and transactions: `docs/dev/concurrency.md`.
- **Pessimistic locking** (one holder): `POST /{coll}/_lock/{id}` `{"ttl":"30s","wait":"10s"}` answers `{"token","expires_at","doc"}`. Write with `?lock=<token>`, then `POST /{coll}/_unlock/{id}` `{"token"}`. Other writes of the document wait; another `_lock` waits, then `423 locked`; a stale token is `409 lock_expired`. SDKs: `lock(id) { }` (`Lock`, `lock`), Rails `Model.lock.find(id) { }`. Use it when the code has an effect outside ndb.
- **Transaction (one collection):** `POST /{coll}/_lock` `{"ids":[...],"ttl":"5s"}` locks a set (all or none, one token, no deadlock). `POST /{coll}/_tx` `{"ops":[{"op":"insert","doc":{}},{"op":"update","id","update","query"},{"op":"delete","id"}],"locks":[token],"unlock":true}` writes all ops in one WAL record or none; an update or delete that matches nothing is `409 tx_conflict`; an expired lock is `409 lock_expired`. `POST /{coll}/_unlock` `{"token"}` releases a set. No transaction across collections. SDKs: Rails `Model.transaction(lock: [a, b], ttl: 5) { ... }` (writes collected, `Ndb::Rollback`), Go `coll.Tx(ctx, ids, fn)` (`ndb.ErrRollback`), TS `coll.transaction(ids, fn)` (`Rollback`), Ruby `coll.transaction(lock: ids) { |tx, docs| }`.
- **Field names** that start with `$` are refused (`invalid_field`), except `$blob` (a blob reference). Names that start with `_` are reserved.
- **Writes go through `update` operators**: `$set`, `$unset`, `$inc`, `$push`, `$pull`, `$min`, `$max`, `$setOnInsert`. Integers stay exact (an int64 overflow is `invalid_field`).
- **Queries** are JSON in the body key `query`, with MongoDB-like filters (`$eq`, `$in`, `$gte`, `$regex`, `$elemMatch`, `$and`, `$or`, `$not`). `fields` keeps only the listed paths (`_id` too only if you list it). `_id` is the last tie-break of a sort, in the direction of the last sort key.
- **Unknown body keys are errors** (`invalid_request`, `invalid_query`, `invalid_pipeline`): a typo never passes silently.
- **Only `_doc` and `_bulk` create a collection.** `_update/{id}` (also with upsert), `_expire` and `_persist` on an unknown collection are `404 unknown_collection`: `PUT /{coll}` first.
- **`_bulk` is all or nothing**, also on a partitioned collection (all checks run before any write).
- **Partitioned collections** (`partition_by`, set only when the collection is made): `_id` is unique across the partitions, and a partition name (`calls-2026-11`) belongs to its logical collection.
- **Patterns** (`logs-*`, `a,b`) work only on `GET /{coll}`, `_search`, `_count`, `_aggregate`. Every other route is `pattern_read_only`.
- **Drop and truncate** need `?confirm={coll}`.

### Streams (Redis Streams model, on disk)

- A stream is a collection made with `"stream": true` (or by its first append). Routes are under `/{s}/_streams/...`.
- Entries are **append only**: `POST _doc`, `_bulk`, `_update*`, `_delete_by_query`, `_expire`, `_persist` on a stream are `400 stream_append_only`. Delete one entry with `DELETE /{s}/_doc/{id}`, or trim.
- An entry ID is `<ms>-<seq>`. Read with a group (`groups/{g}/read`, then `ack`), without a group (`_streams/read`, XREAD), or with `_search`.
- Delivery is **at least once**: make the work safe to repeat. A pending read counts as a delivery, as in Redis.
- Delayed retry is **ndb only**: `nack` with `delay_ms`, then `claim {"due": true}`. The SDK workers do it with a retry schedule (`Retry` / `retry:` / `retry`) and a max age; a poison entry goes to `<stream>_dead`, and nothing sends it back by itself.
- The Redis commands exist in each SDK for a move from Redis: Ruby `Ndb::Client#xadd` and the others, Go `sdk/go/ndbredis`, TypeScript `@thadeu/ndb/ioredis` and `@thadeu/ndb/node-redis`.

### Pick the SDK page

- **Rails app**: `ndb-rails`. Models with `Ndb::Record` (`where` for a list, `find_by` for one, `find(id)` by id, `to_ndb` gives the JSON body). Streams with `include Ndb::Streams` in `app/streams`, run with `bundle exec ndb streams`, test with `Ndb::Streams.inline!` or `perform_now`.
- **Plain Ruby**: the gem `ndb` (`Ndb::Client`).
- **Go**: `github.com/thadeu/ndb/sdk/go` (standard library only).
- **TypeScript (Node, Bun, Hono)**: `@thadeu/ndb`.

### Errors

Every error has the body `{"error":{"code","message"}}`. The SDKs turn each `code` into a typed error (Go `ndb.ErrUnknownCollection`, Ruby `Ndb::UnknownCollection`, TypeScript `UnknownCollectionError`). The full list is on the Errors page.
