# Changelog

All notable changes to this project are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project follows [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- `ndb bench -compound` (also in `make bench`): 60 000 orders of 60 accounts, each with items, read with an index of the account and then with compound indexes (`account,amount`, `account,status`); each read shows its plan and the documents it examined, and two of them have an `$unwind`. On the benchmark machine, an account with amounts of a 1% range goes from 973 documents examined in 10.1 ms to 9 in 2.2 ms. Budget: the slowest of these reads 66 ms.
- Go `ndb.Index` and `ndb.Unique`, and Rails `index`, take one field too, for a path that is not a field of the struct or an attribute: `ndb.Index("items.sku")`, `index "items.sku"`.
- **`$unwind` in `_aggregate`.** `{"$unwind": "$items"}` before `$group` makes one row per element of an array, so a pipeline can group by the elements (quantity sold per sku of the `items` of the orders). The rules are those of MongoDB: an empty array, `null` or a missing field gives no row, a value that is not an array gives one; the long form has `preserveNullAndEmptyArrays` and `includeArrayIndex`. A `$match` after it filters the rows; `$first` and `$last` follow the order of the elements. One `$unwind` per pipeline, and it needs a `$group`. On a collection, a partitioned collection and a pattern. In the SDKs it is a stage of the pipeline; Go has `ndb.UnwindStage("items")`, and Rails `Order.unwind(:items).group("items.sku").sum("items.qty")`.
- **Compound indexes.** An entry of `indexes` with commas, `"account_id,created_at"`, is one index on 2 to 4 fields (also `PUT` and `DELETE /{coll}/_indexes/account_id,created_at`). It serves a filter with equality or `$in` on its first fields and, at most, a range on the next one; a filter on the first field alone uses it too, and a lookup whose foreign key is its first field. The other conditions are tested on the documents it gives, so the answer is the same as with no index (a test compares 2400 random queries with a scan, in the memtable and in segments). On 300 000 documents of 300 accounts, `account_id = x and amount >= 100 and amount < 200` goes from 1003 documents read in 36 ms (index of `account_id`) to 5 documents in 1 ms. The index does not order the rows: a `sort` still reads the documents the filter keeps.
- **Compound unique keys.** `"unique": ["account_id,email"]` allows one live document per tuple of values (`409 unique_violation`): an email is unique inside an account. A document where one of the fields is missing or `null` is not checked.
- In the SDKs: Go `ndb.Collection[T](db, name, ndb.Index("a", "b"), ndb.Unique("a", "b"))`; TypeScript `indexes: [['a', 'b']]` and `unique: [['a', 'b']]` in the schema; Ruby `indexes: [%w[a b]]` (or `"a,b"`); Rails `index :a, :b` and `index :a, :b, unique: true`.

### Fixed

- **An index of one field gave fewer documents than a scan for a range of two sides on an array field.** `{"b": {"$gt": 1, "$lte": 2}}` matches `{"b": [1, 3]}` (one element passes each side; they need not be the same element, as in MongoDB), and a scan found it, but with an index on `b` the read took only the keys between the two bounds and missed it. Such a read now also takes the documents with more than one key in the index, and the filter tests them. A collection with no array in the field gets the same documents as before; the first range of two sides on a segment reads its key file once to find the rows with arrays (about 50 ms for 300 000 rows, then kept while the collection is open). Ranges of one side, `$eq` and `$in` were right.

## [1.19.0] - 2026-11-11

### Added

- **Transactions of one collection.** `POST /{coll}/_tx` writes a list of inserts, updates and deletes in one WAL record: all of them, or none, also after a `kill -9` (200 kills in the crash test, never half a transaction). An update or a delete that matches nothing (no such document, or its `_v` guard does not match) is `409 tx_conflict` and nothing is written; the error names the op. `POST /{coll}/_lock` `{"ids":[...]}` takes the locks of a set of documents under one token, all or none, so two callers never deadlock; `POST /{coll}/_unlock` `{"token"}` releases them. With the tokens in `locks`, a lock that expired refuses the whole transaction (`lock_expired`): the `ttl` is its time limit, and `"unlock": true` releases the locks in the same call. In the SDKs: Rails `Model.transaction { }` as in ActiveRecord (the writes of the block are collected and sent in one call at its end; `raise Ndb::Rollback`; `lock: [records], ttl:, wait:` takes every lock in one call, and `lock!` inside the block joins it; another model in the block is `Ndb::TransactionError`), Go `coll.Tx(ctx, ids, fn)` with `ndb.ErrRollback`, TypeScript `coll.transaction(ids, fn)` with `Rollback`, Ruby `coll.transaction(lock: ids) { |tx, docs| }` with `Ndb::Rollback`.
- **Pessimistic locks.** `POST /{coll}/_lock/{id}` gives one caller the document and a token; until `POST /{coll}/_unlock/{id}` or the `ttl` (default 30 s, up to 5 min), only a write with `?lock=<token>` changes the document. Another `_lock` waits (`wait`, default 10 s), then `423 locked`; a write without the token waits for the release; a `many` write that meets a locked document stops with `423 locked`; a token that holds no lock is `409 lock_expired`, and nothing is written. The lock and the read of the document are one step in the writer. Locks are in memory: a restart holds none. In the SDKs: `Lock` (Go), `lock` (TypeScript, Ruby), and in Rails `Model.lock.find(id)` (with a block, or held until the first save), `lock!`, `unlock!` and `with_lock`, as in ActiveRecord. Use a lock when the code has an effect outside ndb that must not run twice.
- Docs: the page Concurrency (`docs/dev/concurrency.md`) in three parts: optimistic locking (the default: the version `_v`, then `with_retry`), pessimistic locking (`lock`) and transactions, with the same example in each client.
- **`with_retry` in every SDK** (`WithRetry` in Go, `withRetry` in TypeScript, `with_retry` in Ruby and on a Rails record), the optimistic lock with the retry done: it reads the document, calls your function to change it, and writes the changed fields with the `_v` guard; when another write came first, it reads again and calls the function again (5 tries, `retries`), then fails with `stale_object` (`ErrStaleObject`, `StaleObjectError`, `Ndb::StaleObjectError`). An error in the function writes nothing, and no change writes nothing. No lock waits on the daemon. The docs show three cases: two debits of one balance, a payment webhook that arrives twice, and a seat limit.

## [1.18.2] - 2026-11-10

## [1.18.1] - 2026-11-10

## [1.18.0] - 2026-11-10

### Security

- **CSRF and DNS rebinding on the TCP listener with no token.** A user install (`ndb serve` on a laptop) listens on `127.0.0.1:7070` with no token. Before this release, any web page open in a browser on the same machine could write, change and delete data there: a `POST` with `Content-Type: text/plain` needs no preflight, and ndb did not look at `Origin` (`_doc`, `_bulk`, `_update`, `_delete_by_query`, `_truncate?confirm=`, stream appends...). With DNS rebinding (a name that points to `127.0.0.1`), a page could also read all data, because ndb did not look at `Host`. Now a request with an `Origin` header, or with `Sec-Fetch-Site: cross-site` or `same-site`, is `403 forbidden` on every listener unless the origin is in `cors_origins`. On TCP with no token configured, the `Host` must be an IP, `localhost`, a name with no dot (a Compose service such as `ndb`) or a name in `allowed_hosts`, else `403 forbidden`. curl, the SDKs, the CLI and the console server send no `Origin` and work as before. A daemon with a token was not open to these attacks: a page cannot send the token. Upgrade every user install. See `docs/dev/security.md`.
- **Token brute force.** `auth_failures` failed token checks (default 10) from one IP in `auth_window` (default `1m`) lock that IP out for `auth_lockout` (default `5m`): `429 too_many_requests` with `Retry-After`, a `warn` line `token lockout`. During the lockout the right token is refused too, so a guess cannot be tested. At most 10 000 IPs in memory; an IPv6 client counts by its `/64`. The unix socket has no lockout.

### Added

- **Settings `cors_origins`, `allowed_hosts`, `trust_proxy`, `auth_failures`, `auth_window`, `auth_lockout`, `max_conns` and `max_watches`** in `ndb.yml`, and the flags `-cors-origin` and `-allowed-host` (repeat them), `-max-conns` and `-max-watches` (`docs/dev/configuration.md`). An origin of `cors_origins` gets `Access-Control-Allow-Origin`, `Vary: Origin` and a `204` preflight that allows `Authorization` and `Content-Type`. `trust_proxy: true` takes the client IP of the lockout from the last `X-Forwarded-For` hop. `max_conns` limits the open TCP connections (a new one waits; default no limit). `max_watches` limits the open `_watch` streams (`429 too_many_requests` before the stream starts; default no limit).
- **Error code `too_many_requests` (429)**, with a constant in each SDK (Go `ndb.ErrTooManyRequests`, Ruby `Ndb::TooManyRequests`, TypeScript `'too_many_requests'`).
- **Metric `ndb_watch_streams`**: the open `_watch` streams of the daemon.
- **`user_agent` on the request log line**, so the line shows which SDK or tool sent the request (`ndb-go/…`, `ndb-ruby/…`, `ndb-ts/…`, `ndb-cli/…`).
- **Page `docs/dev/security.md`**: the threat model, listeners, tokens, the browser guards, the lockout, TLS with a proxy, data at rest, debug logs, the images, the DoS limits and operator injection in apps.
- **Image `ghcr.io/thadeu/ndb-stack`**: the daemon and the console in one image, for development (like `redis-stack`). `docker run -p 7070:7070 -p 4321:4321 ghcr.io/thadeu/ndb-stack` needs no other parameter: the daemon listens on the unix socket and on TCP `0.0.0.0:7070` with no token, and the console listens on `0.0.0.0:4321` and opens the local daemon. `NDB_TOKEN` sets a token on TCP. Data is in `/var/lib/ndb`. The image is built FROM `ghcr.io/thadeu/ndb` and `ghcr.io/thadeu/ndb-ui` of the same version (`deploy/docker/stack/`, job `ndb-stack` in `ghcr.yml`). Do not use it for a public server (`docs/dev/deploy/docker.md`, "ndb-stack (development)").

## [1.17.2] - 2026-11-10

## [1.17.1] - 2026-11-10

### Fixed

- Streams: a nacked entry could come back up to 1 ms before its `delay_ms`, because the retry time was the millisecond of now plus the delay. It is now rounded up to the next millisecond, so no entry comes before its wait (`TestNackConcurrentWorkers` caught it on the CI runner).

## [1.17.0] - 2026-11-10

## [1.16.0] - 2026-11-10

### Added

- **Rails: `perform_now`** on a consumer class (`PostCallJob.perform_now(call_id: 42)`) runs `perform` on one payload now, with no request, through the reloader, `around_perform` and `discard_on`, and returns the value of `perform`. `_id:` and `_deliveries:` set `event.id` and `event.deliveries`.
- **Delayed retry in the stream workers** (Go, Ruby, TypeScript, Rails; `docs/dev/streams.md`, "Delayed retry in the SDKs"). A retry schedule (`Retry` / `retry:` / `retry`: a function of the delivery count, or tiers with `Tiers`/`TierSteps`, `Ndb::Stream.tiers`, `tiers()`) nacks a failed entry with the wait of its try; a max age (`MaxAge` / `max_age:` / `maxAgeMs`) sends an entry older than that to the dead letter with the reason `max_age`; the worker claims the due entries every `due_every` (1 s). New calls: `Nack`/`nack`, `ClaimDue`/`claim_due`/`claimDue`, and `retry_at`, `due` and `waiting` in `pending`. The dead entry has `reason` (`max_deliveries` or `max_age`). Rails: `stream :name, retry: [...], max_age: 15.days`, and `ndb streams` logs the schedule of each consumer at start.
- **Streams: delayed retry with `POST /{s}/_streams/groups/{g}/nack`** (ndb only; `docs/dev/streams.md`, "Delayed retry"). `{"consumer", "ids", "delay_ms"}` → `{"nacked": n}`: each ID pending for the consumer stays in the PEL with no owner and a retry time (now + `delay_ms`, 0 to 30 days), with its delivery count. No read or claim gives it before that time, whatever `min_idle_ms` or `force`; after it, a claim takes it at once. `claim {"due": true}` is a scan of the due entries only (an index of the nacked entries, not the whole PEL), with `cursor` and `count`. The `pending` list has `retry_at` (Unix ms, `null` when not nacked) and a `due` filter; its summary has `waiting`. New metric `ndb_stream_group_waiting{stream,group}`. The retry time is on disk in the same transaction as the PEL (a restart, a backup and a restore keep it); a PEL written before opens as it is. Access: write. The spec cases of it are `"ndb_only": true`, which the Redis run skips.
- **Rails: `to_ndb`** on a relation and a model gives the JSON body of the search, as a String, with no request (like `to_sql` of ActiveRecord). `to_ndb(pretty: true)` gives it on many lines.
- **Logs with levels in `ndb serve`: `debug`, `info`, `warn`, `error` and `quiet`** (`docs/dev/operations.md`, "Logs"). Set by `-log-level`, `NDB_LOG_LEVEL` or `log_level` (in this order; default `info`), with `-log-format`, `NDB_LOG_FORMAT` or `log_format` for `text` or `json`. `quiet` writes nothing. `error`: each 5xx with its real error, recovered panics with the stack, failed background work. `warn`: slow requests (`slow_ms`, default 1000), lagged watch consumers, quarantined lines. `info`: start, recovery, ready, stop, collection and index changes, compactions, backups, stream groups. `debug`: one line per request (request id, status, ms, bytes, rows, token name, the query body) and one `write` line per persisting request with the ids, `_v` and the documents. A body in a line is cut to `log_body_max` (default 2048 bytes; `0` = no bodies), and a batch logs its first ids and documents only. No line has the `Authorization` header or a token value. The `log` calls of the collections and the WAL writer use the same logger, so `quiet` silences them too.
- **`GET /_log` and `PUT /_log`**: the log level, `body_max` and `slow_ms`, changed at run time without a restart. Over TCP they need a token with admin of `*`.
- **`X-Request-Id` on every answer.** ndb keeps the id of the request when it is safe (1 to 128 of `A-Z a-z 0-9 - _ . :`), else makes one, and puts it on every log line of the request.

- **A stream read without a group: `POST /{s}/_streams/read`** (`XREAD`). It gives the entries after an ID (`after`, or `$` for only later entries), up to `count`, and `block_ms` waits for an append. It changes no group and no PEL, so it is a read for a scoped token. One stream for each request.
- **Streams in Rails: `Ndb::Streams`** (gem `ndb-rails`, `docs/dev/clients/rails.md`). A class in `app/streams` with `include Ndb::Streams` appends to a stream (`append`, with the aliases `enqueue` and `perform_async`; in an ActiveRecord transaction it waits for the commit), reads it (`last`, `find`, `range`, `count`) and, with `stream :name, group: :g` and a `perform(event)` method, consumes it. Options of `stream`: `concurrency`, `claim_idle`, `max_deliveries` and `dead_letter`, `discard_on`, `start`, `max_len`, `retain`. Also: `around_perform`, inheritance (an `ApplicationStream`), stable consumer names (`NDB_CONSUMER`), the groups made at boot, `ActiveSupport::Notifications` events (`append.ndb_streams`, `perform.ndb_streams`), a Puma plugin (`plugin :ndb`, `NDB_STREAMS_IN_PUMA`) and the `streams:` block of `config/ndb.yml`. For tests: `Ndb::Streams.inline!`, `assert_streamed`, `assert_no_streamed` and the RSpec matcher `have_streamed`.
- **The `ndb` executable of the Ruby gem.** `bundle exec ndb streams` (`-r`, `-e`, `--only`, `-c`) runs the stream consumers of an app until `SIGTERM` or `SIGINT`. Every other command (`ndb serve`, ...) goes to the daemon binary of `PATH`, or of `NDB_BIN`.
- **The Redis Streams commands in each SDK**, so an app that uses Redis Streams picks its backend in one place and keeps its calls (`docs/dev/streams.md`, "Move from Redis Streams"). Ruby: the `x*` methods of redis-rb on `Ndb::Client` (`xadd`, `xreadgroup`, `xack`, ... `xinfo`). Go: the module `sdk/go/ndbredis`, with the stream methods of go-redis v9 (`redis.StreamCmdable`). TypeScript: the subpaths `@thadeu/ndb/ioredis` and `@thadeu/ndb/node-redis`, with no dependency. The errors have the text of Redis (`BUSYGROUP`, `NOGROUP`, ...). The daemon does not speak the Redis protocol.
- New stream methods in the SDKs. Ruby: `Stream#read` (a read without a group), `group.create(start, no_create: true)`, `claim` with `idle:`, `retry_count:`, `force:` and `just_ids:`, `auto_claim` with `just_ids:`, and a worker block with two parameters gets the delivery count. Go: `Stream.Tail`, `Group.ClaimIDs`, `Group.AutoClaimIDs`, `ndb.GroupNoCreate()`, and `"(id"` in `Range` and `RevRange`. TypeScript: `Stream.read`.

### Fixed

- Streams: `claim` with a negative `idle_ms` set the delivery time in the future, so the entry was hidden from later claims (a delay by accident). As Redis 7.4 does with `XCLAIM ... IDLE` and `TIME`, a delivery time in the future, or before 1970, is now (`spec/streams/claim_idle_negative.json`, checked on Redis).
- HTTP API review (a contract test for each promise of the docs; 45 new shared cases in `spec/cases`):
  - Documents: a `_v` in a written document is dropped (it was stored, and a read gave two `_v` keys, so a `_v` guard never matched). A field name that starts with `$` (except `$blob`) is `invalid_field` on every write. `$inc` on an integer above 2^53 was a silent no-op; integers are now exact, and an int64 overflow is `invalid_field`. `$min`/`$max` compare exactly. `_ttl` of a far expiry no longer overflows. `_delete_by_query` with no query is `empty_filter`.
  - Partitioned collections: `partition_by` is set only when the collection is made; `partition_of` is not a client key; a partition name belongs to its logical collection (a plain collection cannot take it, and a write never goes to one); a rename checks every partition name first; `_id` is unique across the partitions (an upsert merges into the partition that holds it); a PUT reaches every partition with the whole definition; `_bulk` is all or nothing (all checks before any write) and makes no empty partition.
  - Collections: a `unique` added by a later PUT is enforced at once (409 `unique_violation` when the data has duplicates); a restart after such a PUT no longer fails. `max_line_size` is the document size. `_restore` with `to` equal to the collection has a clear message.
  - System: a write after the database closed was applied without the data-dir lock; every call is now `503 closed`. A bad blob `Range` gives the JSON error body (`416 invalid_range`, a new code in each SDK). The `Bearer` scheme is case-insensitive. `_ack` past the end is `400 invalid_position`.
- Rails: `last` and `last(n)` loaded the whole relation to take its last rows. They now turn the order around and ask the daemon for `n` rows. With no `order`, `first` and `last` use the `_id` order (the two ends of one order), as ActiveRecord does with the primary key.
- Streams, access: a read-scoped token could create, delete or read a group named `pending`, or delete a consumer named `pending`. The read right now follows the shape of the route.
- Streams: the write routes of every collection (`POST _doc`, `_bulk`, `_update`, `_update_by_query`, `_delete_by_query`, `_expire`, `_persist`) worked on a stream and could break the position of its groups. They now give `400 stream_append_only` (a new code, with a constant in each SDK).
- Streams: a truncate, drop or rename during other stream requests could keep the old groups or give 500. `entries_read` and `lag` follow Redis after a delete, a trim and a group made at an ID (a group made without `entries_read` now shows `null`, as in Redis). A `min_id` trim keeps to `limit`, and `limit` without `approx` is an error. `_aggregate` and `_watch` on a stream give `<ms>-<seq>` IDs; a pattern read or a `$lookup` into a stream is refused. An append after the highest ID is an error, not `0-0`. A blocked read without a group waits for a stream that does not exist yet. Groups and consumers may have any name.
- Stream workers (Go, Ruby, TypeScript, Rails): an entry that fails on its last allowed delivery goes to the dead letter at once; an entry left by a crash past the limit goes to the dead letter without the handler; a stop during the handler leaves the entry pending; a failed first read is retried. Go: an entry that does not decode no longer fails its batch (`Entry.Err`).
- Ruby client: after an exception that is not a `StandardError` (`Timeout.timeout`, rack-timeout), the pool could give a connection with an unread answer to the next call. A read timeout is `Ndb::TimeoutError` and is not sent again.
- Rails streams: `ndb streams` loads `app/streams` before it starts; any exception in `perform` is a failed entry, and a dead thread starts again; the daemon down at start does not end the process; a second signal in 2 s does not drop acks; the Puma plugin starts the child in its own process group; the code reloads in development; the `ndb` executable never runs itself through a version-manager shim.
- Redis commands: `COUNT 0` and exclusive ranges with an ID of only the ms (`(5`) give what Redis gives; `xreadgroup` on many streams delivers nothing when a group is missing.
- Streams: a pending read of a group (`"pending": true`, `XREADGROUP ... 0`) counts as a delivery, as in Redis: delivery count + 1 and a new delivery time. A deleted entry does not change. Before, the count stayed the same, and the Redis parity test failed.

## [1.15.0] - 2026-11-09

### Added

- **Streams: the Redis Streams model on disk** (`docs/dev/streams.md`). A collection with `"stream": true` is an append-only log with consumer groups, under `/{s}/_streams/...`: `append` (`XADD`, with `trim`, one ID `<ms>-<seq>` per entry, many entries in one fsync), `trim` (`XTRIM`, exact or in blocks of 1 024), groups (`XGROUP`), `read` (`XREADGROUP`, new or pending entries, `block_ms`, `no_ack`), `ack`, `pending`, `claim` (`XCLAIM` and `XAUTOCLAIM`) and info (`XINFO`). Every acked append and every group change is on disk before the answer; a stream is a collection, so `_search`, `_count` and `_aggregate` read it, and `_id` sorts as an ID. The group state is in backups (not in PITR). The cases of `spec/streams` give the same results on ndb and on a real Redis 7.4 (a CI test). `ndb bench -streams` measures it, with checks that a `kill -9` loses no acked append, delivered entry or ack.
- **Streams in the Go, Ruby and TypeScript SDKs.** Each Redis Streams command has a call (`Append`, `Read`, `Ack`, `Pending`, `Claim`, `AutoClaim`, `Trim`, `Info`, ...), and a group worker (`Work` / `worker` / `work`) runs the loop: own pending entries first, new entries with a block, one ack per batch, claim of idle entries, and poison messages to a dead-letter stream after `MaxDeliveries`. A blocked read is not cut by the client timeout.
- Ruby errors `NotAStream`, `InvalidStreamID`, `StreamIDTooSmall`; TypeScript errors `NotAStreamError`, `StreamIdError`, `UnknownGroupError`, `GroupExistsError`.

### Changed

- **`ndb serve` with no flags works with no root.** When ndb does not run as root and `/var/lib/ndb` does not exist (a laptop), the defaults are `~/.ndb/data`, `~/.ndb/ndb.sock` and TCP on `127.0.0.1:7070`; the CLI commands find the daemon there too. A server (root, `--systemd`, Docker) keeps `/var/lib/ndb`, `/run/ndb/ndb.sock` and no TCP. The daemon makes the dir of the unix socket.
- `ndb` alone in a terminal prints the help; `ndb serve` (or `ndb s`) runs the daemon. Without a terminal (systemd, Docker) `ndb` alone still runs the daemon, so old units keep working; the unit and the image now say `serve`.
- `ndb -h` and `ndb serve -h` print the full help, now with the daemon flags and a "Try it" block at the end. The home page of the docs has a "Run it in one minute" section.

## [1.14.0] - 2026-11-09

### Added

- **The Go SDK limits its connections** (from the fsw-esl POC). Without a limit, a peak of goroutines opened hundreds of connections at once: over a Docker network calls took 1 s and 4 s, and over TCP some failed. `ndb.Connect` now opens at most `ndb.DefaultMaxConns` (64); over that a call waits for a free connection. `ndb.WithMaxConns(n)` and `ndb.WithIdleTimeout(d)` change it; a `Watch` has its own connections, out of the limit. With 1000 goroutines over the unix socket, p99 went from 35 ms to 9.5 ms and max from 137 ms to 12 ms.
- **`ndb.Testing.Clean` in Go and `clean` in TypeScript**, like `Ndb::Testing` in Rails: truncate the collections a test used, `CleanAll` / `cleanAll` for every collection with a prefix, `WithPrefix` / `prefix` for collections per parallel worker. A clean runs only in a test run (`go test`; `NODE_ENV=test`, vitest, jest, `node --test`), else it fails; `NDB_ALLOW_CLEAN=1` forces it.
- **A constant for every error code in every SDK.** Go has a sentinel per code (`ErrInvalidPath`, `ErrInvalidDoc`, `ErrInvalidRequest`, `ErrForbidden`, ... 18 new); the TypeScript `ErrorCode` union has every code; Ruby has a class per code (`InvalidDocument`, `InvalidPattern`, `Forbidden`, `RequestTooLarge`, `GroupLimitExceeded`, `Closed`, ...). A test of the repository fails when the daemon answers a code that the error table or an SDK does not have. The error table now lists `stopping` and no longer lists `forbidden` twice.

### Changed

- **A bloom filter check reads no file.** The filter of each column chunk is read into memory on first use (a few KB) and checked there; the filter of parquet-go read 32 bytes from the file on each check, so every lookup made one `pread` per segment. In the 10 client phase of `ndb bench`, these preads were 12.9 s of the 92 s of CPU in 10 s, and they are gone (reads/s +5 to 8%).

### Fixed

- **`Close` of a collection no longer races with a compaction started by a call** (`POST /{coll}/_compact`): it closed the segment readers while the compaction still read them. It now waits for the compaction, which stops at its next step. `TestCloseStopsACompaction` failed under `-race` in CI now and then; the CI of 1.13.2 failed on it.
- **`ndb bench` measures the reads on the same state in every run.** After the load, 24 to 28 MB of documents could stay in the memtable of the loaded partition (it is sealed at `write_buffer` or after 10 minutes), or none: two runs of one binary then gave `count of all` 0.4 ms or 15 ms, and `events per type` 3 ms or 23 ms. The bench now seals that memtable with `_compact` after the load and waits again; the settle takes about 10 s more. The `ndb` daemon does not change.
- The Go SDK test of `ErrInvalidPipeline` used a pipeline with no `$group`, which is valid since 1.13.2.

## [1.13.2] - 2026-11-09

### Added

- **`_aggregate` takes a pipeline with no `$group`, as MongoDB does.** Such a pipeline reads documents: it runs as a find, with the same indexes, cursor and headers as `_search`. The stages are `$match` and one `$sort`, then `$skip` and `$limit`, and one `$project` (keep or remove fields); `$lookup` goes at the end, as before. A `$match` or a `$sort` after `$skip`, `$limit` or `$project` is still `invalid_pipeline`, because it would give other rows than MongoDB. Before, every pipeline with no `$group` was `invalid_pipeline`.

- **The docs site serves `/sitemap.xml` and `/robots.txt`** (they were 404), so search crawlers find every page. `robots.txt` lets every crawler in and points to the sitemap.
- **`context7.json`** at the root of the repo: the docs and SDK READMEs that [Context7](https://context7.com) indexes for coding agents, with the main rules of ndb.

### Changed

- The gems and the npm package link to the docs site: `homepage` is https://ndb.clowk.in, and `documentation_uri` (gems) or `homepage` (npm) is the client page.

### Fixed

- **The Ruby SDK default socket is `/run/ndb/ndb.sock`, the one of the daemon.** It was `/var/run/ndb/ndb.sock`, which works only where `/var/run` links to `/run` (most Linux systems, not macOS). The Ruby and Rails pages said the old path, against the configuration page.
- The collection keys table of the configuration page listed `unique` twice.
- **A collection on macOS (Apple Silicon) used 4x the disk for its `meta.bolt`.** bbolt takes the page of the OS for a new file, 16 KiB there, so each `meta.bolt` was 128 KiB (32 KiB on Linux). New ones now use 4 KiB pages on every OS: in `ndb bench`, an idle collection went from 142.8 KB to 46.8 KB of disk on a Mac. An existing `meta.bolt` keeps the page size of its header.
- **`ndb bench` on macOS measures the physical footprint of the daemon, not the RSS of `ps`.** There, the RSS still counts the memory that Go gave back until the system takes it, so it stayed near the peak of the load: the idle part showed 127 MB of RSS for a footprint of 35 MB, and three checks failed on a Mac (RSS with 10 clients 488 MB, RSS per idle collection 177 KB, disk per idle collection 142.8 KB). They now pass: 384 MB, 87.9 KB and 46.8 KB. Linux still reads the RSS of `ps`.
- **Links between the pages of the docs site no longer open GitHub (404).** A link from one page to another (`[Aggregate](../aggregate.md)`) went to `github.com/.../blob/main/aggregate.md`, a path that does not exist; 70 links of the site did. They now stay on the site; a link to any other file of the repo still opens it on GitHub. The site no longer builds stray pages for `CHANGELOG.md` and `benchmark/README.md`.

## [1.13.1] - 2026-11-09

### Added

- **The console and the docs site behave like an app on a phone**: a web app manifest and the iOS home screen tags (they open full screen, with their icon), no pinch or double tap zoom, no tap flash, no rubber band scroll, and the safe areas of the screen. The console top bar stays at the top while the page scrolls; the docs site hides its scrollbars (it still scrolls).

### Fixed

- **The console Watch tab no longer stops after about 10 seconds.** With no change to show, the stream sent nothing for 15 s (ndb's heartbeat), and Bun closes a connection after 10 s without bytes; the console now sends its own keep-alive every 5 s.

## [1.13.0] - 2026-11-09

### Changed

- **Aggregations and scans are 2 to 3 times faster** (2 million documents in one partition): `events per type` 422 ms → 123 ms, `talk time per customer` 591 ms → 220 ms, `calls per customer` 495 ms → 179 ms, `long calls` (a scan) 412 ms → 281 ms, a `$lookup` dashboard 500 ms → 175 ms. A read of a row group for an aggregation decodes no `_id` (unless `$first` or `$last` needs the order), builds cells only for the rows a filter or an index kept, and reads `_rest` only when a kept row needs it; the strings of a column chunk are shared (a status or a type is one string per value, not per row); `$group` then `$sort` and `$limit` keeps only the first groups instead of sorting them all; reads use every core, and a collection with fewer segments than cores splits its segments into units of row groups. `make bench` budgets: slowest aggregation 160 ms, lookup 150 ms.

- **Memtable reads decode only the fields the query uses** (the recent documents, not sealed yet): each document skips the fields that no filter, sort, `per` or `$group` reads. On 2 million documents over six monthly partitions, with 22.8k unsealed documents: `count of all` 272 ms → 40 ms, the dashboards 0.6–2.2 s → 0.4–0.6 s, a `has_many` lookup 1.2 s → 0.5 s, 10 clients 1 → 3 reads/s.
- **A count whose only condition is a time range counts the row groups wholly inside it without reading them** (from their time zone map and live bits); only the groups at the two edges are read. Up to the previous month over six monthly partitions: 500 ms → 40 ms (5.6 ms with the list of the five past months in the path), one month 165 ms → 32 ms.

### Added

- **A list of collections in the path**: `POST /calls-2026-11,calls-2026-10/_search` reads the listed collections, like a pattern, and the list mixes with patterns (`calls-2026-1*,calls-2025-12`). The caller chooses the collections a read touches; ndb does not choose them from the query. The name of a partitioned collection in a list reads its live partitions; a name that does not exist is `404 unknown_collection`; a scoped token needs read access to every item. `_search`, `_count`, `_aggregate` and `GET`.

- `ndb bench -concurrent`: apps that share the daemon, 1, 3 and 10 clients reading at the same time (the queries, aggregations and lookups, in a random order), with reads per second, p50, p99, errors and the RSS peak. On the benchmark machine (4 CPU): 24, 53 and 59 reads/s, p99 119, 157 and 550 ms, RSS peak 170, 203 and 224 MB, no error. Budgets: no failed read, p99 with 3 clients 500 ms, RSS peak with 10 clients 450 MB.
- With a bigger load than the default (`-calls`), the time budgets of the reads are shown but not judged; memory, lost writes and errors still are.
- `ndb bench -data <dir>` keeps the loaded calls and reads them again on the next runs, without loading; `make bench-read` reads 2 million documents kept in `.bench/2m` (8 minutes the first time, about 3 after).
- `ndb bench -direct`: direct mode with 500k cdrs and their users in NDJSON files, read at start and appended live (64 KiB writes that cut lines, a new file every 100k lines), with every line checked (counts and 1000 ids), then the reads of the cdrs and their users (`belongs_to`, `has_many`).
- `ndb bench -partition month`: the calls over six monthly partitions, with reads up to the previous month (also by the `events-*` alias) and of one month; `make bench-read-month`.

### Fixed

- **A shutdown no longer waits for a compaction.** On SIGTERM the seals and compactions stop first, then the requests in flight finish: a daemon stopped during the compaction of a big partition took as long as the merge (15 s for 210k documents, minutes for millions); it now stops in 0.05 s, and a `_compact` cut by the shutdown answers `503 stopping` (retry). Nothing is lost: the WAL has every acked write.
- **A second daemon no longer takes the socket of a running one.** At start ndb removed any socket file at its path, then stopped on the lock of the data dir: the running daemon kept working but nobody could reach it. A socket that answers is now an error (`another ndb is listening on it`); only a dead one is removed.

## [1.12.1] - 2026-11-08

## [1.12.0] - 2026-11-08

### Added

- **`per` in `_search`**: `{"per": {"field": "customer_id", "limit": 5}}` gives the first `limit` rows (in `sort` order) of each value of the field, in one search: the last 5 orders of each customer. Each value keeps its own top rows while the rows are read, and only the rows returned read their document, across segments and partitions.

### Changed

- **`has_many` and `has_one` lookups read a whole page in one search** (`$in` on the foreign field, with `per`), not one search per row: 20 customers with their last 5 events went from 190 ms to 67 ms in `make bench`, a 2-level lookup from 97 ms to 66 ms. Budget: slowest lookup 250 ms.

### Fixed

- The writer metrics (`/metrics`) count a commit before the client gets its answer, so they are never behind an acked write.

## [1.11.1] - 2026-11-08

## [1.11.0] - 2026-11-08

### Changed

- **Counts, aggregations and the wake are faster** (`make bench`, 210k events):
  - A count whose only condition is an equality on an index counts the hits of the index, without reading the rows: `count of the finishcalls` 111 ms → 4 ms, `count of a customer` 33 ms → 1.3 ms.
  - A range on the time field skips the row groups outside it (the zone maps of the time field are its instants, read once per segment): `count of one hour` 113 ms → 12 ms, `a customer in one hour` 29 ms → 3.5 ms.
  - A segment wholly inside the `retention` (or TTL) is read without checking the time of each row, and its time column is not read unless the query uses it: `events per type` 137 ms → 47 ms, `talk time per customer` 150 ms → 60 ms, `long calls, not indexed` 116 ms → 46 ms.
  - A segment read copies each column value once, and a group of a plain column is one allocation.
  - A segment opens with its footer only (the page index and the bloom filters are read on first use of a column, then kept), and the segments of a collection open in parallel: the first call on the loaded partition after it slept went from 30 ms to 17 ms.
  - `make bench` budgets: slowest query 150 ms, aggregation 200 ms, lookup 600 ms, wake of the loaded partition 50 ms.

## [1.10.0] - 2026-11-08

### Changed

- **Sorted searches read only the documents of the page.** A sort against the insertion order (newest first) made every matching row the best so far, so each one read its document, and a `skip` read the rows it passed. The rows are now sorted from the columns and only the rows of the page read a document, across segments and partitions: `last 20 of a customer` went from 185 ms to 29 ms, page 3 of a customer's finishcalls from 200 ms to 42 ms, a `has_many` lookup of 20 rows from 1.2 s to 0.32 s, a 2-level lookup from 580 ms to 160 ms. `make bench` budgets: slowest query 350 ms, slowest lookup 1 s.

### Added

- `read_cache` in ndb.yml: the memory of the decoded row groups (default 32 MiB).

### Added

- `ndb bench -yml 'key: value'` adds a line to the ndb.yml of the daemon (repeat it), to compare settings such as `memory_limit`; the report header lists them.

## [1.9.0] - 2026-11-08

### Changed

- **Concurrent reads of the same segments are 5 to 12 times faster.** Each segment kept one decoded row group, so queries that read rows of different groups at once decoded them again and again: 4 sorted searches at once took 3.1 s (0.6 s one after another), and a `has_many` lookup of 20 rows took 12.3 s. The decoded groups now share one cache of the daemon, at most 32 MiB: 4 at once take 0.26 s, the lookup 1.1 s. Memory stays bounded.
- A collection that sleeps gives the memory it freed back to the OS at once: 48 KB of RSS per sleeping collection in `ndb bench` (it was 101 KB).

### Fixed

- After a crash, the open WAL file kept its disk reserve (up to 4 MiB) until it rotated. The next start gives it back.

### Added

- `ndb bench -lookup` measures the lookups (`belongs_to`, `has_many`, `has_one`, 2 levels, `$lookup`). `ndb bench` also times the first call on the loaded partition after it sleeps (30 ms for 210k rows, 481 MB) and measures the disk reserved past the data of an idle collection (0 KB). It works with a long `-dir` (the socket goes to the temp dir).

## [1.8.0] - 2026-11-08

### Added

- **Idle collections close.** An engine collection with no call for `idle_close` (ndb.yml, default `10m`, `off` to keep all open) seals its memtable and closes its files; the next call opens it again (0.6 ms in `ndb bench`). 1 000 collections and 365 daily partitions at rest hold no open file (it was 2 per collection) and use no CPU. A `_watch` consumer keeps a collection open; `_stats` and `/metrics` read a sleeping collection without opening it (`asleep` in `_stats`, with a total), and the daily PITR check opens only the ones written since their newest snapshot. A `kill -9` while writes wake collections loses no acked write.
- `ndb bench -idle` adds a partitioned collection with 365 daily partitions, the time of the first call on a sleeping collection, and a `kill -9` while collections wake; its budgets are 0 open files per idle collection and 128 KB of disk.
- **`ndb bench` logs as it runs.** Each step logs to stderr when it starts, every 5 s while it runs (docs written, docs loaded, collections made) and when it ends with what it found, with the elapsed time and the ETA of the run. The report stays on stdout.

### Fixed

- A data race (1.7.0) on the period of the write buffer check, when a collection opened while others were written.

## [1.7.0] - 2026-11-08

### Changed

- **Idle collections use no CPU.** The seals, compactions, WAL truncation and TTL sweeps of every collection run from one scheduler with a deadline per job, instead of two 1 s timers per collection (and a 200 ms flush timer per `relaxed` WAL). A collection that is not written has no deadline: 300 idle collections went from 280 ms of CPU every 10 s (2.8% of a core) to 0 ms, and 1 000 use 0 ms too. A memtable is still sealed at its size, at `write_buffer` (one daemon-wide check, while some memtable holds writes) and at 10 minutes; `make bench` is the same for writes, load and reads. The `ndb bench` budget for idle CPU is now 0.5% of a core.
- A `relaxed` collection fsyncs at most 200 ms after the first write that is not on disk, instead of on a 200 ms timer that ran even when idle.

### Fixed

- In `strict` mode a write is in the change feed before it is answered: `_watch` from the current position and the PITR snapshot check saw an acked write a moment late.

## [1.6.0] - 2026-11-08

## [1.5.0] - 2026-11-08

## [1.4.0] - 2026-11-08

## [1.4.6] - 2026-11-08

### Added

- **`ndb bench` checks its numbers.** After the parts, every measurement is compared with a budget (lost writes, write p99, load, query and aggregation time, RSS, and the cost of idle collections), and the bench prints a PASS/FAIL table and exits 1 on a FAIL. `make bench` fails with it. A new part, `-idle`, measures what idle collections cost: 300 collections of 10 documents (`-collections`), then disk, CPU, RSS and open files at rest.

### Fixed

- **The WAL reserve is proportional to the data.** 1.4.5 reserved 4 MiB ahead of the data of every open WAL file, so 300 small collections held 1.2 GB. The reserve is now as big as the data, between 64 KiB and 4 MiB: 300 collections of 10 documents went from 4 112 KB to 80 KB each. A collection under load still gets 4 MiB ahead, and write p99 and load in `make bench` are the same.

## [1.4.5] - 2026-11-08

### Fixed

- **The WAL reserved 64 MiB of disk per collection.** Each open WAL file reserved its whole size with `fallocate` at creation, so 10 small collections held 640 MB, and every partition of a partitioned collection kept 64 MiB. The writer now reserves 4 MiB ahead of the data, growing with the file, and gives the rest back when the file is closed: 10 collections of 1 000 documents went from 641 MB to 41 MB while running and 1.2 MB after a stop. Load throughput is the same in `ndb bench`; write p99 moved within noise (2.65 ms → 2.98 ms on average of 4 runs, p99.9 lower in 3 of 4).

## [1.4.4] - 2026-11-08

### Changed

- Rails: the Minitest integration is `require "ndb/rails/minitest"` (it was `ndb/rails/test_help` in 1.4.3), next to `ndb/rails/rspec`.

## [1.4.3] - 2026-11-08

### Added

- **Rails test support.** ndb has no transactions, so the gem truncates the collections a test used, after it (a few ms each; definitions and indexes stay): `require "ndb/rails/test_help"` (Minitest, every `ActiveSupport::TestCase`, `self.ndb_clean = false` to opt out), `require "ndb/rails/rspec"` (`ndb_clean: false`), and `require "ndb/rails/database_cleaner"` (`DatabaseCleaner[:ndb]` with `:touched` and `:truncation`). Parallel workers get collections of their own (`test<n>_`, from `parallelize` or `TEST_ENV_NUMBER`; `config.collection_prefix`). A clean refuses to run outside the test environment.

## [1.4.2] - 2026-11-08

### Changed

- **Rails associations behave like ActiveRecord.** `user.cdrs` (a `has_many`) is an `Ndb::Relation`: lazy and chainable (`user.cdrs.where(status: "answered").count`); after an `includes` it holds the loaded records, and a builder on it queries again. An association is kept for the key it was read with: changing the foreign key reads it again.

### Added

- Rails: `reload` (the attributes, the version and the associations) and `last` on a relation and on the model; a relation's `size` uses the loaded records, else counts.

## [1.4.1] - 2026-11-08

### Added

- Rails associations take `fk:` and `pk:` as short names of `foreign_key:` and `primary_key:`.

## [1.4.0] - 2026-11-08

### Added

- **Lookup.** `lookup` in `_search` reads, in the same call, the documents of other collections that the rows point to, inside the daemon: `belongs_to` (the foreign key is here; an object or `null`), `has_many` (the foreign key is there; an array, with its own `query`, `sort` and `limit`) and `has_one`, with `foreign_key`/`fk` and `primary_key`/`pk` (default `_id`), `fields`, and a nested `lookup` (two levels). An array foreign key matches each element. The other side must be indexed (`lookup_unindexed`), one answer attaches 10 000 documents at most (`lookup_too_large`, 422), and a scoped token must read every collection of it. Works on plain, partitioned and pattern searches. See [Lookup](docs/dev/query.md#lookup).
- **`$lookup` in `_aggregate`**, at the end of the pipeline, with the MongoDB names (`from`, `localField`, `foreignField`, `as`) or one relation of the Rails names.
- **SDKs:** Go `Lookup` with `BelongsTo`/`HasMany`/`HasOne` and the `lookup` tag option; TS `lookup` with `belongsTo`/`hasMany`/`hasOne`; Ruby `lookup`. **Rails:** `belongs_to`, `has_many` and `has_one` on `Ndb::Record` with the Rails defaults, and `includes(:customer, items: :product)` in one call.

## [1.3.0] - 2026-11-08

## [1.2.9] - 2026-11-08

### Added

- **`@tdu/ui/stimulus`** for Rails and Hotwire: `DatePickerController` (`data-controller="tdu-datepicker"`) grows the picker around the input of the form builder (`text_field`, `date_field`, `datetime_field`; two inputs make a range), with values for mode, min, max, presets, locale and labels, the actions `open`, `close`, `clear`, and the event `tdu-datepicker:change`. Disconnect restores the markup, so the Turbo cache stays clean, and a cached copy is never doubled. Without Stimulus, `data-tdu-datepicker` and `observeDatePickers()` follow Turbo Drive, Frames and Streams. `enhance(root)` is the vanilla API under both.

## [1.2.8] - 2026-11-08

### Changed

- The UI library is `@tdu/ui` (it was named `@thadeu/ui`; it was never published under that name).

## [1.2.7] - 2026-11-08

### Added

- **`@tdu/ui` as a library.** React bindings in `@tdu/ui/react`: `useDatePicker` (a `ref`, `value`, `date`, `from`, `to`, `setValue`, `open`, `close`) and `<DatePicker>` (controlled with `value` + `onChange`), React as an optional peer. The vanilla controller has `value()`, `date()`, `setValue()`, `update({ min, max, presets })` and `destroy()`; every change calls `onChange` and dispatches a bubbling `datepicker:change`. Every color, radius, size, space, font and shadow is a `--dp-*` variable (the README has the table, a blue theme and a dark one), and the fields have a default style of their own.

### Fixed

- A `last` preset (`Last 7 days`) in a single date picker cleared the field; it takes the start of the range now.

## [1.2.6] - 2026-11-08

### Added

- **`@tdu/ui`** (`console/lib/ui`): the date picker and the popover of the console as a package of their own, for npm: plain TypeScript and DOM, ESM + CommonJS + types (tsup), its own CSS themed by `--dp-*` variables, months and weekdays from `Intl` (`locale`), labels for i18n, `datePickerHTML` for a server and `createDatePicker`/`mountDatePickers` for the browser. The console imports it as `@/lib/ui/...`.
- **Time in the date picker popover:** a `HH:MM:SS` row with the mask, Now and Done. A day click keeps the popover open for the time; Enter or Done closes it; a time out of `min`/`max` is moved inside.
- **On a phone** the popover is a sheet on the bottom edge, full width, so the time row and Done fit.

## [1.2.5] - 2026-11-08

### Added

- **Console date picker of its own** (`DatePicker` in the design system, in the `/ui` gallery): a positional mask for `YYYY-MM-DD` and `YYYY-MM-DD HH:MM:SS` (digits fill their places and skip the separators, a 4 as the first digit of a month becomes 04, parts stay in range, ArrowUp/Down step the part at the caret, a paste is read), a calendar in a `Popover` that goes below or above its trigger by the room there (`getBoundingClientRect`) in the native top layer, min/max days, keyboard moves, presets on the left (`5 minutes ago`; `Last 7 days` in range mode) and a range mode. Its colors are `--dp-*` variables, for a library later. The PITR restore dialog uses it (presets up to the start of the window).
- **Console tabs** (`Tabs`, radios, no script needed) in the Backups tab: backup and restore of a file for systemd, CLI, Docker and Voodu; PITR for CLI, Docker, Voodu, curl, Go, Ruby and TypeScript.

## [1.2.4] - 2026-11-08

### Changed

- **PITR uses `at` and `to`, like the rest of the contract.** `POST /{coll}/_restore {"at","to"}` (was `until`, `into`); CLI `ndb restore <coll> -at T -to NAME`; Go `Restore(ctx, at, to)`, Ruby `restore(coll, at:, to:)`, TS `restore({ at, to })`. The old keys are refused (`invalid_request`).
- **Console:** PITR is a block of the **Backups** tab, as in DynamoDB, instead of a tab of its own: the windows paged by 10, a Restore dialog on each row, and a Rename dialog that opens filled in after a restore.

## [1.2.3] - 2026-11-08

### Added

- **Point in time recovery.** `pitr: 1d` (the default) keeps one snapshot a day of every engine collection (hard links of its segments and a copy of `meta.bolt`, no data copied) plus the WAL from it. `POST /{coll}/_restore {"until","into"}` makes a copy of the collection as it was at any instant of the window, in a new collection; the source is not touched. Truncate, drop and `rename -replace` keep the old dir in the trash for the window, so they can be restored too. `pitr: off` or up to `7d`, per daemon or per collection. CLI `ndb restore <coll> -until T -into NAME` and `ndb pitr`; `GET /{coll}/_pitr` and `GET /_pitr`; a PITR tab in the console; `Restore`/`PITR` in the SDKs. See [PITR](docs/dev/pitr.md).
- **Rename.** `POST /{coll}/_rename {"to","replace"}` moves a collection, its indexes and its watch positions to a new name (partitions too); `replace` puts it in place of an existing one. CLI `ndb rename`, SDKs `Rename`/`rename`. Crash safe.
- `ndb size` has a `pitr` column.

### Changed

- Console: the native forms (no script) were refused with 403: `Referrer-Policy: no-referrer` made the browser send `Origin: null`. It is `same-origin` now.
- A truncated or dropped collection no longer frees its disk at once when PITR is on: its dir stays in `<data_dir>/trash/` for the window. Set `pitr: off` for the old behavior.

## [1.2.2] - 2026-11-08

### Added

- **Scoped tokens.** `tokens:` in `ndb.yml` gives each app a token with the collections (names or globs, such as `calls*`) it may `read`, `write` or `admin`. A token without the right is `403 forbidden`. The token of `-token-file` keeps every right. See [Scoped tokens](docs/dev/configuration.md#scoped-tokens).
- **SDKs over HTTPS.** The Go, Ruby and TypeScript SDKs take `https://host[:port]` (and `http://`), for ndb behind a TLS proxy such as Caddy. The Ruby SDK checks the certificate with the system CAs (`SSL_CERT_FILE` adds a private CA). See [TLS](docs/dev/configuration.md#tls).

## [1.2.1] - 2026-11-07

## [1.2.0] - 2026-11-07

### Changed

- **Direct mode is disk first.** A direct collection (apps append NDJSON files, ndb tails them) kept every row in memory and read every file again from offset 0 at each start. It now seals its rows to segments like an engine collection, with the same bounded memory (~20 bytes per sealed row), and each checkpoint saves the read position of every file. After a restart the tailer goes on from the last checkpoint. While the memtable is full the tailer waits for a seal. The first start of this version reads the files once, as before, and seals them.
- `ndb compact` and `POST /{coll}/_compact` work on direct collections (they were `not_supported`).

## [1.1.4] - 2026-11-07

## [1.1.3] - 2026-11-07

## [1.1.2] - 2026-11-08

The releases of 1.1.0 and 1.1.1 did not publish the `ndb-ui` binaries, and the SDKs of 1.1.1 still had version 1.1.0. 1.1.2 publishes everything with one version. The daemon and the SDKs do not change.

### Added

- **Console:** edit a saved connection. The Edit drawer is the New connection form, filled in, with Discover. An empty Token field keeps the saved token.

### Fixed

- **Console:** Dev Tools adds the leading slash to a path, as in Kibana: `POST orders/_search` sends `POST /orders/_search`.
- **CI:** the release builds the console client bundles before it compiles `ndb-ui`.

## [1.1.0] - 2026-11-07

ndb 1.1 keeps its data on disk: the memory of the daemon has a fixed size (about 512 MB) whatever the number of documents. The API and the SDKs do not change. The first start writes the key files of the existing segments, once.

### Changed

- **Memory has a fixed size (M16).** Sealed documents stay on disk: each segment has key files next to it (its ids with a bloom filter, the keys of each index field, the values of each unique field) and one bitmap of live rows, saved with the checkpoint. The memtable is the only map of documents. A sealed document costs ~20–40 bytes of memory instead of ~340, and the daemon stays near 512 MB of RSS whatever the data size. See [Storage: memory](docs/dev/storage.md#memory).
- One write buffer for the whole daemon: `write_buffer` (default 32 MiB) is shared by every collection and partition; over it, the largest memtable is sealed. Writes wait while a memtable is over twice its seal size, instead of growing the memory.
- `memory_limit` defaults to 384 MiB (the Go heap soft limit) when neither it nor `GOMEMLIMIT` is set.
- Seals and compactions write one row group at a time. A seal runs while a compaction builds its segment; one compaction runs at a time in the daemon, at most 512k rows; a segment whose rows are mostly replaced is rewritten alone.
- The collections of a pattern, and the partitions of a partitioned collection, are read in parallel (up to 4 at once).
- A scan reads the document of a row only when the row enters the result: a `_count` or a search with a `limit` no longer decodes every match. A `_count` with no conditions adds whole row groups from the live bits.
- The stored documents are decoded with Sonic on the read paths (queries, aggregations, seals, compactions); the API input and writes keep `encoding/json`.
- A backup that needs a snapshot streams it from the segments instead of listing every document in memory.
- Upgrade: the first start of 1.1 writes the key files and live bits of the existing segments, once.

### Added

- `ndb bench`: the ESL outbox benchmark (live writes with a `_watch` consumer, `kill -9`, bulk load, reads) on a daemon of its own, in about two minutes; `make bench` runs it. See [Measure](docs/dev/operations.md#measure).
- `write_buffer` config key.

### Fixed

- The blob GC ran after each compaction and read every document of every collection, even with no blob stored. It now skips when the store is empty.

- TypeScript SDK on Bun: the undici `Pool` of Bun cannot dial a unix socket, so every call failed (`statusCode` of undefined). On Bun the transport now uses its `fetch` (unix socket and TCP); Node keeps undici.

### Added

- Partitioned collections: `partition_by: day | week | month | year` (with `time`) makes a logical collection whose documents go to one partition per period (`calls-2026-11`), created at first use. Reads (`_search`, `_count`, `_aggregate`) go over every live partition; writes go to the partition of the time field; routes by id find the partition that holds the id; update and delete by query run on each partition. With `retention`, a partition whose period ended more than `retention` ago leaves reads at once and a background sweep drops it. See [Partitioned collections](docs/dev/http/collections.md#partitioned-collections).

## [1.0.0] - 2026-11-07

ndb 1.0 is the stable API. It breaks the 0.x API on purpose, with no compatibility layer: the routes take the shape of Elasticsearch (`/{coll}/_search`), the body keeps the query language of MongoDB, and the SDKs share one vocabulary. Upgrade the daemon, the SDKs and the console together. The data directory, the WAL and backups do not change.

### Changed

- **Routes.** No `/v1/c` prefix. A path whose first segment starts with `_` is the system (`/_health`, `/_stats`); any other is a collection, and its actions start with `_` (`/{coll}/_search`). A document id always comes after an action (`/{coll}/_doc/{id}`), so an id never collides with a route. An unknown path is `404 unknown_route`; a known path with another method is `405 method_not_allowed`.
- **`query` replaces `where`** in every body: `_search`, `_count`, `_watch`, `_update_by_query`, `_delete_by_query`, `_update/{id}` (guards). `where` is `invalid_query`.
- **Update bodies** put the filter in `query` and the write operators in `update`. Options (`many`, `upsert`, `expire_in`) moved from the query string into the body. `$not` is allowed at the top of an update or delete query.
- **Rows** of `_search` and `_aggregate` are a JSON array by default; `Accept: application/x-ndjson` gives NDJSON, as before. The page data stays in `X-Ndb-*` headers in both formats.
- **`_v`** is the version everywhere: write results and watch events (was `v`).
- **SDKs:** one vocabulary in Go, Ruby and TypeScript (table below). Rails keeps the Rails way.

### Added

- `POST /{coll}/_count` (`{"count": n}`), `POST /{coll}/_doc` (insert one document), `PUT /{coll}/_indexes/{field}`, the `X-Ndb-Took-Ms` header.
- Upsert: `POST /{coll}/_bulk` with `"upsert": true` creates each document by its `_id`, or merges its fields into the stored one (the other fields stay), like `doc_as_upsert` in Elasticsearch, in one transaction. SDKs: Go `Upsert`/`UpsertMany`, Ruby `upsert`/`upsert_all`, Rails `Model.upsert`/`Model.upsert_all` (only the given attributes, no callbacks), TypeScript `upsert`/`upsertAll`.
- Ruby: `Ndb::Page`, `Ndb::Response` and `Ndb::Rails::SearchResult` are `Data` objects (immutable) instead of `Struct`. Watch events of `Ndb::Collection#watch` carry `"consumer"`, so `ack(event)` needs nothing else.
- Go: `Coll.Stats`, `Compact`, `EnsureIndex`, `DropIndex`, `Count`, `Name`; `DB.Health`, `Collections`, `Stats`, `Size`.
- TypeScript: blobs (`putBlob`, `getBlob`), `collections`, `stats`, `size`, `compact`, `ensureIndex`, `dropIndex`.
- Ruby: `Ndb.connect` and `Ndb::Collection` with a query builder (`Ndb::Query`); `count`, `bulk`, `update_by`, `delete_by`, `health`, `stats`, `size`, `compact`, index routes on `Ndb::Client`.
- Docs: an **HTTP** tab with one page per route group (system, collections, documents, queries, watch, errors) and the full protocol contract (transport, auth, formats, headers, consistency, retries, streams, limits). `llms.txt` now includes the HTTP and UI pages.

### Migration

| 0.x | 1.0 |
|---|---|
| `GET /health`, `GET /metrics` | `GET /_health`, `GET /_metrics` |
| `GET /v1/c` | `GET /_collections` |
| `GET /v1/stats`, `GET /v1/size`, `POST /v1/backup` | `GET /_stats`, `GET /_size`, `POST /_backup` |
| `PUT /v1/blobs`, `GET /v1/blobs/{ref}` | `POST /_blobs`, `GET /_blobs/{ref}` |
| `PUT`, `GET`, `DELETE /v1/c/{coll}` | `PUT`, `GET`, `DELETE /{coll}` |
| `POST /v1/c/{coll}/truncate`, `/compact`; `GET .../stats`, `.../quarantine` | `POST /{coll}/_truncate`, `_compact`; `GET /{coll}/_stats`, `_quarantine` |
| `DELETE /v1/c/{coll}/indexes/{field}` | `DELETE /{coll}/_indexes/{field}` (and new `PUT`) |
| `POST /v1/c/{coll}/find` `{"where": ...}` | `POST /{coll}/_search` `{"query": ...}` |
| `find` with `count: true` for a total | also `POST /{coll}/_count` |
| `POST /v1/c/{coll}/aggregate` | `POST /{coll}/_aggregate` |
| `POST /v1/c/{coll}/insert` `{"insert": [...]}` | `POST /{coll}/_bulk` `{"docs": [...]}`, or `POST /{coll}/_doc` with one document |
| `GET`, `DELETE /v1/c/{coll}/{id}` | `GET`, `DELETE /{coll}/_doc/{id}` |
| `POST /v1/c/{coll}/{id}/update?upsert` `{"_v":1,"$set":...}` | `POST /{coll}/_update/{id}` `{"query":{"_v":1},"update":{"$set":...},"upsert":true}` |
| `POST /v1/c/{coll}/update?many` `{"status":"x","$set":...}` | `POST /{coll}/_update_by_query` `{"query":{"status":"x"},"update":{"$set":...},"many":true}` |
| `POST /v1/c/{coll}/delete?many` `{"status":"x"}` | `POST /{coll}/_delete_by_query` `{"query":{"status":"x"},"many":true}` |
| `POST /v1/c/{coll}/expire` `{"_id","in"}`, `/persist` `{"_id"}` | `POST /{coll}/_expire/{id}` `{"in"}`, `POST /{coll}/_persist/{id}` |
| `GET /v1/c/{coll}/{id}/ttl` | `GET /{coll}/_ttl/{id}` |
| `POST /v1/c/{coll}/watch` `{"where"}`, `/ack` | `POST /{coll}/_watch` `{"query"}`, `POST /{coll}/_ack` |
| results and events `{"_id","v"}` | `{"_id","_v"}` |
| rows as NDJSON | a JSON array (NDJSON with `Accept: application/x-ndjson`) |
| CLI `ndb update <coll> '{filter + $ops}'` | `ndb update <coll> '<query>' '<update>'` |

| SDK 0.x | Go 1.0 | Ruby 1.0 | TypeScript 1.0 |
|---|---|---|---|
| connect | `ndb.Open` → `ndb.Connect` | `Ndb::Client.new` → `Ndb.connect` (same client) | `connect` |
| collection | `Collection[T](ctx, db, name)` → `Collection[T](db, name)`, declared on first call | new `db.collection(name, definition)` | `db.model` → `db.collection` |
| pattern | `Pattern[T]` → `Collection[T](db, "logs-*")` | `db.collection("logs-*")` | `db.collection('logs-*', schema)` |
| insert | `Insert(ctx, docs...)` → `Insert(ctx, doc)`, `InsertMany(ctx, docs)`; `InsertWith` gone | `client.insert(coll, docs)` → `insert(coll, doc)`, `bulk(coll, docs)`; `collection.insert(doc or docs)` | `insert(doc)` answers one result, `insert([docs])` an array; `create` gone |
| update by id | `UpdateID` → `Update(ctx, id, ops, Guard(...))` | `update(coll, id, ops, guard:)` | `update(id, ops, { guard })` |
| update by query | `Update(ctx, filter, ops)` → `UpdateBy` | `update(coll, filter, ops)` → `update_by` | `update(filter, ops)` → `updateBy` |
| delete | `DeleteID` → `Delete`; `Delete(filter)` → `DeleteBy` | `delete(coll, filter)` → `delete_by` | `delete(filter)` → `deleteBy` |
| query | `Where` → `Query`; `Raw`/`RawJSON` → `RawQuery`/`RawQueryJSON`; `Iter` → `Each`; `Find` → `Search` | `find` → `search`; new `query` / `raw_query` builder; `Page#next_cursor` → `next` | `where` → `query`; `whereRaw` → `rawQuery`; `iterate` → `each`; `find` → `search` |
| watch filter | `WatchWhere` → `WatchQuery` | `where:` → `query:` | `{ where }` → `{ query }` |


## [0.4.0] - 2026-11-07

### Added

- Collection patterns: `POST /v1/c/logs-2026-*/find` and `/aggregate` read every collection whose name starts with `logs-2026-`, for collections split by time. Rows are merged in the `sort` order (then `_coll`, then `_id`); each row starts with `_coll`, the name of its collection. `count`, `skip`, `limit` and `after` work over the whole result; each collection uses its own indexes. `GET /v1/c/logs-*` lists the matched names. Writes and admin calls on a pattern are `400 pattern_read_only`. See [Collection patterns](docs/dev/query.md#collection-patterns).
- Error codes `invalid_pattern`, `pattern_read_only` and `pattern_too_wide` (more than 256 collections).
- Date math on the time field: `{"at": {"$gte": "now-7d/d"}}`, with `+N`/`-N` in `s m h d w M y` and rounding with `/unit` (UTC). It resolves to a time value, so indexes and zone maps work. A `find` cursor keeps the `now` of its first page. See [Date math](docs/dev/query.md#date-math).
- `$elemMatch`: one element of an array matches the whole inner filter (field keys for objects, operators for scalars). See [Array elements](docs/dev/query.md#array-elements).
- SDKs: Go `ndb.ElemMatch`; TypeScript `$elemMatch` in `Where`. Date math strings work in every SDK as is.
- SDKs, queries built elsewhere (a search form, a saved search), sent as is: Go `ndb.Raw(map)`, `ndb.RawJSON([]byte)` and `Coll.Find(ctx, body)` (one page: rows, next, total); TypeScript `whereRaw(obj)` and `model.find(body)`; Rails `where_raw(hash)` (no Array-to-`$in` convention) and `Model.search(body)` (records, next cursor, total).
- SDKs: Go `ndb.Pattern[T](db, "logs-*")` and the `ndb:"_coll"` field; Ruby `ndb.find("logs-*", ...)`; Rails `self.collection_name = "logs-*"` and `record.source_collection`; TypeScript `db.model('logs-*', schema)` and `row._coll`.

### Changed

- On the time field (and `expires_field`), a string operand that starts with `now` is now date math. A malformed one (`"nowish"`, `"now-7x"`) is `invalid_query`; before 0.4.0 it was compared as a plain string and matched nothing.

## [0.3.3] - 2026-11-07

### Added

- Truncate and drop of a collection, for collections split by time. `POST /v1/c/{coll}/truncate?confirm={coll}` removes every document and keeps the definition and indexes; `DELETE /v1/c/{coll}?confirm={coll}` also removes the definition and the watch consumers. Both take a few ms for any size: ndb swaps the collection dir and deletes the old one in the background. Calls in flight finish first and new calls wait; backup and blob GC never see half a swap. A crash during a truncate is finished or rolled back at the next start. See [Truncate and drop](docs/dev/operations.md#truncate-and-drop).
- Watch: a `truncate` event (no `_id`). Each consumer gets it one time after a truncate, then the new changes. Open streams of the collection end at a truncate or drop.
- CLI: `ndb truncate <coll>` and `ndb drop <coll>` ask you to type the name, or take `-yes`.
- SDKs: Go `Truncate`/`Drop` and `OpTruncate`, Ruby `truncate`/`drop` and `Ndb::ConfirmRequired`, Rails `Model.truncate!`/`Model.drop!`, TypeScript `truncate()`/`drop()` and the `'truncate'` op.
- Error code `confirm_required` (400).

### Changed

- A WAL that holds a truncate marker cannot be read by ndb 0.3.2 or older.

## [0.3.2] - 2026-11-07

### Fixed

- A `many` update or delete of more than about 25k documents failed with `line_too_large`, because the full write went into one WAL record. ndb now splits it into records of up to half of `max_line_size`, so a `many` write has no size limit. Each record is atomic, the write as a whole is not. See [Update by filter](docs/dev/query.md#which-documents).

### Changed

- A `many` delete is faster: when the index alone answers the filter (one indexed field, `$eq`, `$in` or a range), ndb deletes the matches without reading them. Candidates are visited in segment and row order, so each Parquet row group is decoded one time. On 1M sealed documents, a delete of 10k takes about 0.14 s (0.6 s before), 100k takes about 0.4 s, and 868k takes about 3 s (both failed before).

- Docs: the console has its own **UI** section (top menu and tab): an overview with a screenshot gallery, Install, Docker (TCP, unix socket, Compose), and a guide with one part per screen. The old `/console` page is gone.
- Docs site on phones: the logo stays on the left and the menu button moves to the right; the sidebar opens from the right.
- Console: a build version such as `dev` shows as is, not as `vdev`.

## [0.3.1] - 2026-11-06

### Added

- Console: a Discover row in the New connection form finds ndb daemons on this machine (unix sockets and `127.0.0.1`) or on another host, and fills the URL. See [UI guide](docs/dev/ui/guide.md#new-connection-and-discover).

### Changed

- Console: New connection opens in a right drawer from a "+ New connection" card. Name is optional (taken from the URL), and Discover sits next to the URL field.
- Console: fields turn off spelling marks and the 1Password, LastPass, Bitwarden, and Dashlane fill prompts.

## [0.3.0] - 2026-11-06

### Added

- Ndb Console (`ndb-ui`): a web console in `console/` (Bun, Hono). Connections and Overview; a Browser with `where` filter, paging, a JSON tree editor (save as `$set`/`$unset` guarded by `_v`), insert and delete; Dev Tools to send raw requests; a read-only Watch tail; and Backups (download over the unix socket, restore command). The Dev Tools editor auto-indents, closes pairs, and indents with Tab; a hover button formats the JSON body. Release binaries for linux and darwin (`ndb-ui-<tag>-<os>-<arch>.tar.gz` with `ndb-ui-checksums.txt`) and the image `ghcr.io/thadeu/ndb-ui`. `install.sh --ui` installs it. See [UI](docs/dev/ui.md).

### Fixed

- Docs: Docker has a Compose section: a dev quick start with no config and no volume (`volumes_from`), the anonymous-volume warning, a production file, and TCP inside the Compose network.

## [0.2.2] - 2026-11-06

### Changed

- Defaults that make the image need no `command:`: without `-config`, the daemon and the CLI read `NDB_CONFIG`, then `./ndb.yml`, then `/etc/ndb/ndb.yml`. The default unix socket is `/run/ndb/ndb.sock` (was `/run/ndb.sock`), with mode `0660` (was the umask). `listen.unix_mode` sets the mode in `ndb.yml`. The image has no `CMD`; mount `ndb.yml` at `/etc/ndb/ndb.yml`.

## [0.2.1] - 2026-11-06

### Added

- CLI `ndb health`: exit 0 and print `ok` when the daemon is ready. Use it as the container health check (the image has no shell or curl).

### Fixed

- Docs: the Go install line uses `@latest`; the Go guide shows `Where().All()` for every document; the backup example shows version 0.2.0.

## [0.2.0] - 2026-11-06

### Changed

- **Breaking: `_id` is the primary key of every collection.** The `id` key of a definition (`ndb.yml`, `PUT /v1/c/{coll}`) is removed and is now `400 catalog_invalid`; declare a natural key with `unique: [field]`. In engine mode an insert without `_id` gets a monotonic UUIDv7 (text order = creation order); an insert with `_id` keeps any non-empty string. `_id` is always in the document.
- **Breaking: direct mode.** A line with `_id` is a document with that id, and later lines with the same `_id` are its next versions. A line without `_id` is a document without `_id`: `find`, `aggregate` and `watch` see it, the `/{_id}` routes do not.
- **Breaking:** `GET /v1/c/{coll}/ttl/{id}` is now `GET /v1/c/{coll}/{_id}/ttl`. The bodies of `expire` and `persist` take `_id`.
- An insert or upsert over an expired document that the sweeper has not deleted yet replaces it, instead of `409 duplicate_id`.
- **Breaking: backups are one `.ndb` file.** `ndb backup <dest>` writes NDJSON (header, catalog, watch cursors, WAL lines with their CRC, a snapshot when `wal_retention` removed early WAL files, blobs, an end line). `-gzip` writes `.ndb.gz`, `-` writes to stdout, `-no-blobs` leaves blobs out. `ndb restore` takes a file or stdin, finds gzip by its first bytes, and checks every line before it touches the data dir. The old backup folder is gone. `ndb backup` takes `-url unix://...`. `POST /v1/backup` streams the file.
- A unix socket path over the OS limit (about 104 bytes on macOS, 108 on Linux) gives a clear error.
- **Breaking:** every answer names the document id `_id`: insert `{"results":[{"_id","v"}]}`, `expire`, `persist`, `ttl`, and `watch` events (`{"pos","op","_id","v","doc"}`).
- SDKs: the id option of the schemas is removed. Go `ndb:"_id"`, `Get`, `UpdateID` (with `Guard`), `DeleteID`; TypeScript `get(_id)` (was `find(id)`), and `update`/`delete` take a string `_id` or a filter; Ruby `get`, and `update`/`delete` take a String `_id`; Rails `primary_key` is always `_id` and `record.id` is its alias.

### Added

- `GET /v1/c/{coll}/{_id}`, `POST /v1/c/{coll}/{_id}/update`, `DELETE /v1/c/{coll}/{_id}`. A fixed route (`find`, `stats`, ...) wins over an `_id` of the same name.
- CLI `ndb get <coll> <_id>`, and `ndb update <coll> <_id> '<ops>'` / `ndb delete <coll> <_id>`.
- `$setOnInsert`: set fields only when an upsert creates the document; a match is never overwritten.
- ndb-rails: dynamic finders `find_by_<attr>` / `find_by_<a>_and_<b>` (and `!`), `find_or_initialize_by`, atomic `find_or_create_by` / `find_or_create_by!` (one document even under races, through upsert and `$setOnInsert`), `destroy!` (`Ndb::RecordNotDestroyed`), `record.delete`, `Model.destroy(ids)`, `Model.delete(ids)`, `destroy_all`, `delete_all` (`.delete` is its alias).
- `ndb.NewID()` (Go), `Ndb.new_id` (Ruby), `newId()` (TypeScript): the same UUIDv7, for apps that write direct-mode files.
- Docs: one page per client (Go, Ruby, Rails, TypeScript, CLI) under Clients.

### Removed

- The `examples` job in CI. Run `cd examples && make check` locally.

## [0.1.2] - 2026-11-06

### Changed

- **Breaking: atomic update by filter.** `POST /v1/c/{coll}/update` takes one object that mixes a filter and write operators (`$set`, `$unset`, `$inc`, `$push`, `$pull`, `$min`, `$max`; `$and`, `$or`, `$nor` and plain keys are the filter). Options are in the query string: `many=true`, `upsert=true`, `expire_in`. The filter is matched and the write is made inside the single writer of the collection, against the newest state, so concurrent updates never interleave. The answer is `{"matched","modified","doc"}` (no `doc` with `many`); nothing matched is `{"matched":0,"modified":0}` with HTTP 200. `modified` is 0, and no version is written, when the operators leave the document as it is.
- **Breaking:** `POST /v1/c/{coll}/delete` takes a filter as the body, with `many=true`, and answers `{"deleted":N}`.
- **Breaking:** the old update shape (`{"update":{"id","set","unset","if_version"}}`), the `if_version` field of `delete` and `expire`, and the `version_conflict` error are removed. Optimistic concurrency is a filter: `{"id":"x","_v":7,"$set":{...}}` matches nothing when the version moved.
- `many` needs the id or a ready index in the filter (`400 unindexed_many`). `upsert` creates the document from the equality fields of the filter, then applies the operators. New contract errors (HTTP 400): `unknown_operator`, `no_update`, `conflicting_update`, `invalid_field`, `empty_filter`.
- SDKs: Go `col.Update(ctx, filter, ops, ndb.Many(), ndb.Upsert(), ndb.ExpireIn(d))` and `col.Delete(ctx, filter, opts...)`; Ruby `client.update(coll, filter, ops, many:, upsert:, expire_in:)` and `client.delete(coll, filter, many:)`; TypeScript `update(filter, ops, { many, upsert, expireIn })` and `delete(filter, { many })`. Every SDK refuses an operator key without `$` before sending. `IfVersion`, `Unset`, `ifVersion` and `if_version:` are removed.
- ndb-rails: `save`, `update` and `destroy` filter by the id and `_v` and raise `Ndb::StaleObjectError` when nothing matched (it is now its own class, code `stale_object`).

### Added

- `$nor` in `where` and in update filters.
- CLI `ndb update <collection> '<json>' [--many] [--upsert] [--expire-in 60s]` and `ndb delete <collection> '<json>' [--many]`.
- Docs: dedicated Query, Aggregate and Watch pages with SDK tabs; the error table moved to the HTTP API page.
- ndb-rails: `record.increment!(attr, by = 1)`, `record.decrement!(attr, by = 1)`, `Model.where(...).update(ops)` and `Model.where(...).delete`.

## [0.1.1] - 2026-11-05

### Added

- Per-document expiry like Redis: `expire_in` (a duration string or seconds) on `insert` and `update`, `POST /v1/c/{coll}/expire` (`in` or `at`, `if_version`), `POST /v1/c/{coll}/persist` and `GET /v1/c/{coll}/ttl/{id}` (`-1` = never expires). They need `expires_field` (`400 ttl_not_enabled`; `400 ttl_derived` with only `ttl` + `time`), are normal writes (new version, WAL, `watch` event), and `expire_in` wins over a value of the field in the same call. See "TTL like Redis" and "Freeing disk" in `docs/dev/configuration.md`: disk from expired documents returns after compaction, and fully only with `wal_retention`.
- CLI `ndb expire <collection> <id> <duration>`, `ndb persist <collection> <id>` and `ndb ttl <collection> <id>`.
- SDKs: Go `ndb.ExpireIn`, `InsertWith`, `Expire`, `ExpireAt`, `Persist`, `TTL`; Ruby `insert/update expire_in:`, `expire`, `persist`, `ttl`; ndb-rails `create(attrs, expire_in:)`, `record.expire`, `record.persist_ttl!`, `record.ttl`; TypeScript `expireIn`, `expire`, `expireAt`, `persist`, `ttl` and the schema `expires` option.

- `install.sh` and `uninstall.sh`, served at `https://ndb.clowk.in`: `curl -fsSL https://ndb.clowk.in/install.sh | bash` installs the release binary after a sha256 check; `--systemd` also sets up the `ndb` user, `/var/lib/ndb`, `/etc/ndb/ndb.yml` and `ndb.service`.
- HTTP API for operators and a future web console: `GET /v1/stats` (every collection with rows, write mode, indexes, quarantine, segment and WAL file counts, bytes per part, totals, blobs and cursors bytes, version, uptime), `GET /v1/size`, `GET /v1/c/{coll}` (the catalog definition), `POST /v1/c/{coll}/compact` (engine mode; direct mode is `400 not_supported`) and `GET /v1/c/{coll}/quarantine?limit=N`. `GET /v1/c/{coll}/stats` keeps its fields and adds `segments`, `wal_files`, `bytes` and `total`.
- CLI commands `ndb collections [name]`, `ndb compact <collection>`, `ndb quarantine <collection>` and `ndb query <collection> ['<where>']` (`-sort`, `-fields`, `-limit`, `-aggregate`), all with `-json`, `-url`, `-config` and `-data-dir`. `ndb stats` and `ndb size` now call the API first and read the data dir only when no daemon answers.
- Tables fit the terminal: box-drawing borders (ASCII when the locale is not UTF-8 or `TERM=dumb`), bold header, right-aligned numbers, human sizes, thousands separators, cells cut with `…` to the terminal width (`COLUMNS` fallback), color only on a terminal without `NO_COLOR`.
- CLI in the same binary: `ndb restore` (refuses a locked or non-empty data dir without `-force`, and checks the backup by recovering it first), `ndb stats` (daemon HTTP API or data dir, `-json`), `ndb size` (disk usage per collection and part, `-json`), `ndb version`, `ndb help`, and `ndb serve` as an explicit alias of the daemon. Running `ndb` with only flags still starts the daemon.

## [0.1.0] - 2026-11-05

First release.

### Added

- `$regex` and `$options` (`i`, `m`, `s`, `x`) in `where`: RE2 syntax, works on array fields, dot paths and inside `$not`, `$and`, `$or`. A literal `^prefix` without `i` or `m` uses the index and the zone maps.
- `$regex` support in the Go and TypeScript SDKs.
- Ruby gems: a `Regexp` is a valid `where` value (`ndb`, and `where`, `where.not`, `or` in `ndb-rails`). It always sends `m` (Ruby line anchors), maps `/i` to `i`, `/m` to `s`, `/x` to `x`, and rejects look-around, backreferences, atomic groups, possessive quantifiers, `\h`, `\G`, `\R` and `\X` with `Ndb::InvalidQuery` before the request.
- `ndb` daemon: one static binary, one data directory, HTTP API over a unix socket or TCP.
- Durable WAL writes, columnar segments, and a column-scan planner with pruning.
- Two write modes: direct (apps append NDJSON files, ndb follows them) and engine (`insert`, `update`, `delete`, optimistic `if_version`).
- NoSQL `find` with comparison and logical operators, sort, offset and cursor pagination.
- `aggregate` with `$match $group $sort $limit $skip $project` and `$dateTrunc`.
- `watch` and `ack` for outbox-style consumers, with `where` filters and WAL truncation (`wal_retention`).
- Secondary and unique indexes built in the background.
- TTL per document, retention, compaction, and a blob store for large values.
- Quarantine for bad lines in direct mode, online backup, Prometheus `/metrics`.
- Optional bearer token on the TCP listener (`-token-file`).
- Go SDK (`github.com/thadeu/ndb/sdk/go`, tag `sdk/go/v0.1.0`), Ruby SDK in two gems (`ndb`, a plain client with no runtime dependency, and `ndb-rails`, the ActiveModel layer that depends on it) and TypeScript SDK (`@thadeu/ndb`), all MIT.
- Release tooling: static binaries for Linux and macOS, multi-arch image at `ghcr.io/thadeu/ndb`.
- Docs in `docs/dev/`, a docs site, and deploy examples (single VM, cross-VM with WireGuard).

[Unreleased]: https://github.com/thadeu/ndb/compare/v1.19.0...HEAD
[1.19.0]: https://github.com/thadeu/ndb/compare/v1.18.2...v1.19.0
[1.18.2]: https://github.com/thadeu/ndb/compare/v1.18.1...v1.18.2
[1.18.1]: https://github.com/thadeu/ndb/compare/v1.18.0...v1.18.1
[1.18.0]: https://github.com/thadeu/ndb/compare/v1.17.2...v1.18.0
[1.17.2]: https://github.com/thadeu/ndb/compare/v1.17.1...v1.17.2
[1.17.1]: https://github.com/thadeu/ndb/compare/v1.17.0...v1.17.1
[1.17.0]: https://github.com/thadeu/ndb/compare/v1.16.0...v1.17.0
[1.16.0]: https://github.com/thadeu/ndb/compare/v1.15.0...v1.16.0
[1.15.0]: https://github.com/thadeu/ndb/compare/v1.14.0...v1.15.0
[1.14.0]: https://github.com/thadeu/ndb/compare/v1.13.2...v1.14.0
[1.13.2]: https://github.com/thadeu/ndb/compare/v1.13.1...v1.13.2
[1.13.1]: https://github.com/thadeu/ndb/compare/v1.13.0...v1.13.1
[1.13.0]: https://github.com/thadeu/ndb/compare/v1.12.1...v1.13.0
[1.12.1]: https://github.com/thadeu/ndb/compare/v1.12.0...v1.12.1
[1.12.0]: https://github.com/thadeu/ndb/compare/v1.11.1...v1.12.0
[1.11.1]: https://github.com/thadeu/ndb/compare/v1.11.0...v1.11.1
[1.11.0]: https://github.com/thadeu/ndb/compare/v1.10.0...v1.11.0
[1.10.0]: https://github.com/thadeu/ndb/compare/v1.9.0...v1.10.0
[1.9.0]: https://github.com/thadeu/ndb/compare/v1.8.0...v1.9.0
[1.8.0]: https://github.com/thadeu/ndb/compare/v1.7.0...v1.8.0
[1.7.0]: https://github.com/thadeu/ndb/compare/v1.6.0...v1.7.0
[1.6.0]: https://github.com/thadeu/ndb/compare/v1.5.0...v1.6.0
[1.5.0]: https://github.com/thadeu/ndb/compare/v1.4.0...v1.5.0
[1.4.0]: https://github.com/thadeu/ndb/compare/v1.3.0...v1.4.0
[1.4.6]: https://github.com/thadeu/ndb/compare/v1.4.5...v1.4.6
[1.4.5]: https://github.com/thadeu/ndb/compare/v1.4.4...v1.4.5
[1.4.4]: https://github.com/thadeu/ndb/compare/v1.4.3...v1.4.4
[1.4.3]: https://github.com/thadeu/ndb/compare/v1.4.2...v1.4.3
[1.4.2]: https://github.com/thadeu/ndb/compare/v1.4.1...v1.4.2
[1.4.1]: https://github.com/thadeu/ndb/compare/v1.4.0...v1.4.1
[1.4.0]: https://github.com/thadeu/ndb/compare/v1.3.0...v1.4.0
[1.3.0]: https://github.com/thadeu/ndb/compare/v1.2.1...v1.3.0
[1.2.9]: https://github.com/thadeu/ndb/compare/v1.2.8...v1.2.9
[1.2.8]: https://github.com/thadeu/ndb/compare/v1.2.7...v1.2.8
[1.2.7]: https://github.com/thadeu/ndb/compare/v1.2.6...v1.2.7
[1.2.6]: https://github.com/thadeu/ndb/compare/v1.2.5...v1.2.6
[1.2.5]: https://github.com/thadeu/ndb/compare/v1.2.4...v1.2.5
[1.2.4]: https://github.com/thadeu/ndb/compare/v1.2.3...v1.2.4
[1.2.3]: https://github.com/thadeu/ndb/compare/v1.2.2...v1.2.3
[1.2.2]: https://github.com/thadeu/ndb/compare/v1.2.1...v1.2.2
[1.2.1]: https://github.com/thadeu/ndb/compare/v1.2.0...v1.2.1
[1.2.0]: https://github.com/thadeu/ndb/compare/v1.1.4...v1.2.0
[1.1.4]: https://github.com/thadeu/ndb/compare/v1.1.3...v1.1.4
[1.1.3]: https://github.com/thadeu/ndb/compare/v1.1.2...v1.1.3
[1.1.2]: https://github.com/thadeu/ndb/compare/v1.1.0...v1.1.2
[1.1.0]: https://github.com/thadeu/ndb/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/thadeu/ndb/compare/v0.4.0...v1.0.0
[0.4.0]: https://github.com/thadeu/ndb/compare/v0.3.3...v0.4.0
[0.3.3]: https://github.com/thadeu/ndb/compare/v0.3.2...v0.3.3
[0.3.2]: https://github.com/thadeu/ndb/compare/v0.3.1...v0.3.2
[0.3.1]: https://github.com/thadeu/ndb/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/thadeu/ndb/compare/v0.2.2...v0.3.0
[0.2.2]: https://github.com/thadeu/ndb/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/thadeu/ndb/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/thadeu/ndb/compare/v0.1.2...v0.2.0
[0.1.2]: https://github.com/thadeu/ndb/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/thadeu/ndb/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/thadeu/ndb/releases/tag/v0.1.0
