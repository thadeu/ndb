# ndb

<img src="https://raw.githubusercontent.com/thadeu/ndb/main/brand/png/lockup-on-light.png" width="220">

Plain Ruby client for [ndb](https://github.com/thadeu/ndb), an NDJSON query daemon. It talks HTTP over a unix socket or TCP, with a connection pool and keep-alive. It uses the Ruby standard library only: **no runtime dependencies**. The gem only builds JSON. The daemon decides what every query means.

It has two layers:

- `Ndb.connect` and `Ndb::Collection`: a collection with a query builder (`query`, `sort`, `limit`, `each`). Start here.
- `Ndb::Client`: the low-level layer, one method per HTTP route. `Ndb.connect` returns one.

Building a Rails app with models? Use [`ndb-rails`](https://github.com/thadeu/ndb/tree/main/sdk/rails) on top of this gem.

Docs: [client guide](https://github.com/thadeu/ndb/blob/main/docs/dev/clients.md), [query language](https://github.com/thadeu/ndb/blob/main/docs/dev/query.md), [HTTP API](https://github.com/thadeu/ndb/blob/main/docs/dev/http.md).

## Install

Needs Ruby 3.2 or newer and a running ndb daemon.

```ruby
# Gemfile
gem "ndb"
```

```sh
bundle add ndb
# or: gem install ndb
```

```ruby
require "ndb"
```

## Connect

```ruby
ndb = Ndb.connect("unix:///run/ndb/ndb.sock")                 # unix socket
ndb = Ndb.connect("tcp://10.0.0.5:7070", token: "secret")     # TCP with a bearer token
ndb = Ndb.connect                                             # reads NDB_URL and NDB_TOKEN

ndb.health   # => true when the daemon is ready (GET /_health)
```

`Ndb.connect(addr, **opts)` returns an `Ndb::Client`. It is the same as `Ndb::Client.new(url: addr, **opts)`. All options:

```ruby
Ndb::Client.new(
  url: nil,            # "unix:///path", "tcp://host:port", or a bare path that starts with /
  socket: nil,         # a unix socket path
  token: nil,          # bearer token
  timeout: 30,         # seconds, per request
  pool_size: 16,       # idle connections kept
  ready_timeout: 10    # seconds to retry while the daemon starts; 0 turns it off
)
```

- The address is, in order: `url:` (the `addr` of `Ndb.connect`), `socket:`, env `NDB_URL`, then the default socket `/run/ndb/ndb.sock`.
- The token is `token:`, or env `NDB_TOKEN`. It goes only in the `Authorization` header. A unix socket does not need it.
- A bad address raises `ArgumentError` at once.
- `ready_timeout` is how long a call retries while the daemon is not ready (`not_ready`, or the socket is missing or refuses). It uses exponential backoff (50 ms up to 1 s) with jitter. A write is retried only when the daemon certainly did not process it. A read is also retried after a dropped connection. A timeout is never retried. When the deadline passes, the last error is raised.
- `health` does not wait: a daemon that is recovering raises `Ndb::NotReady`.
- A reused keep-alive connection that the server closed is sent again only for a read. A write is never sent twice.
- The pool is thread-safe and drops its connections after a `fork`.

## Collections

```ruby
calls = ndb.collection("calls",
                       indexes: ["status"],
                       unique: ["caller"],
                       time: "at")      # also: expires_field:, ttl:

ndb.collections   # => [{"name" => "calls", ...}] (GET /_collections)
```

- `collection` sends nothing. The first call on the collection declares it (`PUT /calls`) with this definition, once per object. A different definition for an existing collection raises `Ndb::CatalogConflict` on that call.
- A collection declared in `ndb.yml` needs no definition: `ndb.collection("app_logs")`.
- A name that ends with `*` is a collection pattern: `ndb.collection("logs-2026-*")` reads every collection whose name starts with `logs-2026-`. It declares nothing, and a write raises `Ndb::Error` with code `pattern_read_only`. Each row has `"_coll"`, the name of its collection.

Every collection has the primary key `_id`. You can send your own (any non-empty string), or leave it out: the daemon makes a UUIDv7 (`Ndb.new_id` makes the same kind of id on the client). For a natural key such as an email, keep `_id` and declare that field in `unique:`.

## Insert, update, delete

Every document has a version, `_v`. A new document has version 1, and each change adds 1.

```ruby
calls.insert(_id: "c1", status: "answered", caller: "a", duration: 42)   # one document (POST /calls/_doc)
# => {"_id" => "c1", "_v" => 1}

calls.insert([                                                             # many in one transaction (POST /calls/_bulk)
  { _id: "c2", status: "failed", caller: "b", duration: 5 },
  { _id: "c3", status: "failed", caller: "c", duration: 7 }
])
# => [{"_id" => "c2", "_v" => 1}, {"_id" => "c3", "_v" => 1}]

calls.upsert(_id: "c2", status: "answered")              # create by _id, or merge into the stored document
calls.upsert_all([{ _id: "c2", duration: 9 }, { _id: "c4", status: "new" }])   # one transaction

calls.get("c1")   # => {"_id" => "c1", ..., "_v" => 1}, or nil (GET /calls/_doc/c1)

# Atomic update by _id (POST /calls/_update/c1): the id, then the write operators.
calls.update("c1", { "$set" => { status: "done" }, "$unset" => { caller: true } })
# => {"matched" => 1, "modified" => 1, "doc" => {"_id" => "c1", ..., "_v" => 2}}

calls.update("c1", { "$set" => { duration: 50 } }, guard: { _v: 2 })   # only if the version is still 2
stock.update("a", { "$inc" => { qty: -1 } }, guard: { qty: 1.. })      # guarded decrement
counters.update("hits", { "$inc" => { n: 1 } }, upsert: true)          # created with this _id when missing

# Atomic update by query (POST /calls/_update_by_query): the first match, or every match with many.
calls.update_by({ status: "failed" }, { "$set" => { status: "retry" } }, many: true)   # => {"matched" => 2, "modified" => 2}
calls.update_by({ caller: "z" }, { "$inc" => { duration: 1 } }, upsert: true)

calls.delete("c2")                                  # by _id => {"deleted" => 1} (DELETE /calls/_doc/c2)
calls.delete_by({ status: "retry" }, many: true)    # by query (POST /calls/_delete_by_query)
```

- Operators: `$set`, `$setOnInsert` (like `$set`, but only when an upsert creates the document), `$unset`, `$inc`, `$push` (with `{ "$each" => [...] }`), `$pull`, `$min`, `$max`. Field names can be dot paths. A key of the operators without `$` raises `ArgumentError` before anything is sent.
- Nothing matched is not an error: `matched` is 0. A stale `_v` guard matches nothing, so read again and retry. `modified` is 0 when the operators leave the document as it is.
- `update` takes `guard:`, `upsert:` (creates the document with that `_id`) and `expire_in:`. `update_by` takes `many:`, `upsert:` (creates the document from the equality fields of the query) and `expire_in:`. `delete_by` takes `many:`. The answer has `doc` only for one document.
- The query and the guard take the Ruby forms of `query` (below): an Array is `$in`, a Range is `$gte`/`$lte`, `nil` is `$exists: false`.
- `many: true` needs `_id` or an indexed field in the query (`Ndb::UnindexedMany`). An empty query raises `Ndb::EmptyFilter`.
- The contract errors are `Ndb::UnknownOperator`, `NoUpdate`, `ConflictingUpdate`, `InvalidField`, `UnindexedMany` and `EmptyFilter`, all under `Ndb::InvalidUpdate`.
- A duplicate `_id` raises `Ndb::UniqueViolation` (code `duplicate_id`), and a many-document insert writes nothing. A broken unique field raises it too (code `unique_violation`).

### Truncate and drop

```ruby
calls.truncate   # every document goes; the definition and indexes stay => {"collection" => "calls", "truncated" => true}
calls.drop       # the collection, its definition and its watch consumers go
```

- Both take the same time for 10 or 10 million documents. Watch consumers get one event with `"op" => "truncate"` (no `_id`), then the new changes.
- After `drop`, the next call on the collection declares it again. A collection declared in `ndb.yml` comes back, empty, when the daemon restarts.
- A direct-mode collection raises `Ndb::ReadOnlyCollection`.

## Expiry (TTL like Redis)

The collection needs `expires_field`.

```ruby
sessions = ndb.collection("sessions", expires_field: "exp")

sessions.insert({ _id: "s1" }, expire_in: 3600)                 # seconds, or "1h" / "30m" / "1d"
sessions.update("s1", { "$set" => { user: "u" } }, expire_in: "30m")

sessions.expire("s1", in: 1800)                                  # => {"_id", "_v", "expires_at"}
sessions.expire("s1", at: Time.now + 7200)
sessions.ttl("s1")                                               # => 7199, -1 (never expires), or nil (missing or expired)
sessions.persist("s1")                                           # => {"_id", "_v"}
```

- `expire_in` wins over a value of `expires_field` in the same call. `ttl` rounds up to whole seconds.
- `expire` and `persist` raise `Ndb::NotFound` for a missing or expired document. `Ndb::TtlNotEnabled` means no `expires_field`; `Ndb::TtlDerived` means the expiry comes from `ttl` + `time`.
- Disk space of expired documents returns after compaction, and fully only with `wal_retention`; see [Freeing disk](https://github.com/thadeu/ndb/blob/main/docs/dev/configuration.md#freeing-disk).

## Query

A query is immutable: every step returns a new one. The request (`POST /{coll}/_search`) goes out when you call `all`, `first`, `page`, `count` or `each`.

```ruby
calls.query(status: "answered").all
calls.query(status: %w[answered failed])            # $in
calls.query(duration: 30..60)                       # $gte and $lte
calls.query(duration: 30...60)                      # $gte and $lt
calls.query(duration: nil)                          # $exists: false
calls.query(at: Time.now - 86_400..)
calls.query("agent.id" => 7)                        # dot path
calls.query(duration: { "$gt" => 10 })              # any daemon operator, as a Hash
calls.query("$or" => [{ status: "failed" }, { duration: { "$gt" => 50 } }])

calls.query(status: "answered")
     .query(caller: %w[a b])                        # ANDed
     .sort(at: :desc, _id: :asc)
     .fields(:_id, :status)
     .limit(50)
     .skip(100)
     .all                                           # => [{"_id" => "c1", "status" => "answered"}, ...]

calls.query(status: "answered").first               # the first row, or nil
```

- `query` turns Ruby values into operators: an Array is `$in`, a Range is `$gte` and `$lte` (or `$lt`), `nil` is `$exists: false`, a `Time` is an RFC 3339 string, a `Regexp` is `$regex`. A key that starts with `$` is passed as is.
- `sort(at: -1, _id: 1)`: `1`, `:asc` ascending, `-1`, `:desc` descending. The key order is the sort order. The id is always the last key.
- `fields` returns only these fields.
- See the [query language](https://github.com/thadeu/ndb/blob/main/docs/dev/query.md) for every operator.

### A query built elsewhere

A query from a search form or a saved search is a `raw_query`. Its Hash is sent as it is (only Times and Regexps are converted), ANDed with the other conditions:

```ruby
calls.raw_query("status" => "answered", "at" => { "$gte" => "now-7d" }).limit(50).all
```

For a whole search body (`query`, `sort`, `fields`, `limit`, `skip`, `after`, `count`, `explain`), use `search`. It returns one `Ndb::Page`:

```ruby
page = calls.search(query: { status: "answered" }, sort: { at: -1 }, limit: 50, count: true)

page.rows       # => [{"_id" => "c1", ...}, ...]
page.next       # the cursor of the next page, nil on the last one
page.total      # with count: true
page.plan       # "id", "index:status", "scan" or "snapshot"
page.examined   # rows the daemon looked at
```

`query(...).page` returns the same `Ndb::Page` for a built query (`page(count: true)` fills `total`).

### Regular expressions

A Ruby `Regexp` is accepted anywhere a value is: a field value, `"$regex" => /x/`, inside `$not`, `$or`, `$and`, in `query`, `search`, `aggregate` (`$match`) and in the `query:` of `watch`.

```ruby
calls.query(name: /^alpha/i)                                     # i: ignore case
calls.query(name: { "$regex" => /bet$/ })
calls.query("$or" => [{ name: /^a/ }, { name: /^b/ }])
```

The daemon uses RE2. The mapping to `$options` is:

| Ruby | `$options` |
|---|---|
| always | `m` (Ruby `^` and `$` match at each line, so the daemon must do the same) |
| `/i` (IGNORECASE) | `i` |
| `/m` (MULTILINE: `.` matches a newline) | `s` |
| `/x` (EXTENDED) | `x` |

Ruby features that RE2 does not have raise `Ndb::InvalidQuery` (code `invalid_query`) **before** any request: look-around (`(?=`, `(?!`, `(?<=`, `(?<!`), backreferences (`\1` to `\9`, `\k<name>`), atomic groups (`(?>`), possessive quantifiers (`a++`), `\h`, `\G`, `\R` and `\X`.

A String pattern is sent unchanged and has no `m`: `query(name: { "$regex" => "^abc" })`. A pattern anchored with `^` and a literal start, without `i` or `m`, uses the index on the field (`page.plan` is `index:name`). A Regexp always sends `m`, so it is a scan filter, not an index range. For a prefix search that uses the index, send the String form.

### Count

```ruby
calls.count(status: "answered")            # POST /calls/_count
calls.query(status: "answered").count      # the same
calls.count                                # every document
```

`limit`, `skip` and `sort` do not change the count.

### Cursor pagination

Offset pages get slower as `skip` grows. Use a cursor for deep pages.

```ruby
query = calls.query(status: "answered").sort(_id: 1).limit(100)

loop do
  page = query.page

  page.rows.each { |row| puts row["_id"] }

  break if page.next.nil?   # nil on the last page

  query = query.after(page.next)
end
```

A cursor works only with the same query and `sort`. Another query shape raises `Ndb::InvalidCursor`.

To walk every row, use `each`. It follows the cursor, with `limit` as the batch size. A query is `Enumerable`:

```ruby
calls.query(status: "answered").sort(_id: 1).limit(500).each { |row| puts row["_id"] }
```

## Aggregate

The pipeline needs exactly one `$group`. Accumulators: `$count`, `$sum`, `$avg`, `$min`, `$max`, `$first`, `$last`.

```ruby
page = calls.aggregate([
                         { "$match" => { "duration" => { "$gte" => 1 } } },
                         { "$group" => {
                           "_id" => "$status",
                           "n" => { "$count" => {} },
                           "avg" => { "$avg" => "$duration" }
                         } },
                         { "$sort" => { "n" => -1 } }
                       ],
                       limit: 100,
                       count: true)

page.rows    # => [{"_id" => "answered", "n" => 2, "avg" => 35.5}, ...]
page.total   # with count: true
page.next    # the cursor of the next page, nil on the last one
```

Options: `limit:`, `skip:`, `after:`, `count:`. `$dateTrunc` units are `minute`, `hour`, `day`, `week` and `month`.

## Watch

A watch streams the changes of a collection to a named consumer: first the events after its last ack, then new events as they happen. Use it for outbox-style work.

```ruby
begin
  calls.watch("relay", query: { status: "failed" }) do |event|
    # event has "pos", "op" ("put", "del" or "truncate"), "_id", "_v" and "doc"
    handle(event)

    calls.ack(event, "relay")
  end
rescue Ndb::WatchLagged
  retry # the consumer was too slow. watch continues after the last ack.
rescue Ndb::HistoryTruncated
  raise # the saved position is too old. Start over.
end
```

- `query:` takes the same conditions as `query`: equality, an Array (`$in`), a Range, a Regexp, `nil` (`$exists: false`) and `$` operators as keys. Only matching events come. A delete event has no `"doc"`; it is sent only if this stream saw an earlier matching put of that id.
- `from: "now"` starts a new consumer at the end. `limit: n` ends the stream after `n` events.
- Without a block, `watch` returns an Enumerator: `calls.watch("relay", limit: 10).to_a`.
- `ack(event, consumer)` stores the position of the event (`POST /calls/_ack`): the next `watch` with that consumer name starts after it. The event Hash has no consumer name, so pass it.
- Delivery is at least once. After a restart an event can come again, so make the work safe to repeat.
- `HistoryTruncated` means the WAL files for the position were deleted (`wal_retention`).
- A watch holds one connection until it ends. It is retried only for the initial open, never in the middle of a stream.

## Streams

A stream is an append-only log with consumer groups, with the behavior of
Redis Streams (see [Streams](../../docs/dev/streams.md)). An entry is a Hash
with any fields.

```ruby
orders = ndb.stream("orders")

id = orders.append({ order: 42, total: 99.9 })            # "1731164400000-0"
ids = orders.append_many([{ order: 43 }, { order: 44 }])  # one fsync
orders.append({ order: 45 }, trim: { max_len: 10_000, approx: true })

entries = orders.range("-", "+", count: 100) # XRANGE; entries[0].id, .time, .doc
orders.info                                  # length, first and last ID, groups
```

A worker in a group. It reads its own pending entries first, then new ones
(waiting up to `block` seconds). A block that returns acks the entry; a
block that raises any exception (also `NotImplementedError`) leaves it
pending, but a signal or `exit` goes on. The worker also claims the entries
other workers left idle. An entry delivered `max_deliveries` times goes to
the dead-letter stream as `{stream, group, id, deliveries, doc}`, is acked,
and `on_error` gets an `Ndb::PoisonMessage`. An own pending entry that
crashed the process `max_deliveries` times goes there at the next start,
without the block:

```ruby
group = orders.group("billing")
group.ensure(Ndb::Stream::START_NEW) # or Ndb::Stream::START_ALL

worker = group.worker(
  "worker-1",
  count: 100,
  claim_idle: 60,
  max_deliveries: 5,
  dead_letter: "orders_dead",
  on_error: ->(error) { warn error.message }
) { |entry| charge(entry.doc) }

trap("TERM") { worker.stop }

worker.run # blocks until worker.stop; worker.start runs it in a Thread
```

`worker.stop` ends the loop after the current read (at most `block`
seconds). `group.work("worker-1", stop: -> { done }, **options) { |entry| ... }`
is the same loop in one call: it ends when the `stop` callable returns true.
Delivery is at least once: make the work safe to repeat. Run many workers
with different consumer names to share the work. A failed read goes to
`on_error` and is tried again after a backoff (0.1 s, doubled up to 5 s),
also when the group or the stream does not exist yet (`Ndb::UnknownGroup`,
`Ndb::UnknownCollection`): a worker that starts before its group waits for
it. Only `stop` ends the worker.

The calls one by one, when you need your own loop:

| Redis | Ruby |
|---|---|
| `XGROUP CREATE` | `group.create(Ndb::Stream::START_NEW)` / `group.ensure` |
| `XGROUP SETID` / `DESTROY` | `group.set_id(id)` / `group.destroy` |
| `XREADGROUP ... >` | `group.read("w1", count: 100, block: 5)` |
| `XREADGROUP ... 0` | `group.read("w1", pending: true)` |
| `XACK` | `group.ack(*ids)` |
| `XPENDING` | `group.pending` / `group.pending_list(idle: 60)` |
| `XCLAIM` / `XAUTOCLAIM` | `group.claim("w2", *ids, min_idle: 60)` / `group.auto_claim("w2", min_idle: 60, cursor: "0-0", count: 100)` |
| `XCLAIM ... IDLE` / `RETRYCOUNT` / `FORCE` / `JUSTID` | `group.claim("w2", *ids, idle: 90, retry_count: 5, force: true, just_ids: true)` |
| `XTRIM` | `orders.trim(max_len: 1000)` / `orders.trim(min_id: id)` |
| `XDEL` | `orders.delete(id)` |
| `XREVRANGE` | `orders.rev_range("+", "-", count: 10)` |
| `XREAD` | `orders.read(after: "0", count: 100, block: 5)` (`after: "$"`: only new entries) |
| `XINFO GROUPS` / `CONSUMERS` | `orders.groups` / `group.consumers` |

A read with `block` waits longer than the client `timeout`: the read timeout
of that one request is `block` + 5 seconds.

### Delayed retry (ndb only)

With `retry:`, a worker gives a failed entry back to the group after a wait
that grows with its delivery count (`nack`). The entry has no owner while it
waits: a restart does not take it early, and any worker of the group takes
it when it is due. `max_age:` sends an entry older than that to the dead
letter (with `"reason" => "max_age"`), with no retry. The fsw-esl schedule
(try 1: 15 s, tries 2-5: 1 min, 6-11: 10 min, 12-23: 1 h, then 6 h):

```ruby
group.work(
  "w1",
  retry: Ndb::Stream.tiers(
    { wait: 15, up_to: 1 },      # try 1: 15 s
    { wait: 60, up_to: 5 },      # tries 2 to 5: 1 min
    { wait: 600, up_to: 11 },    # tries 6 to 11: 10 min
    { wait: 3600, up_to: 23 },   # tries 12 to 23: 1 h
    { wait: 21_600 }             # then 6 h
  ),
  max_age: 15 * 86_400,                 # 15 days
  dead_letter: "postcall_jobs_dead"
) { |entry| post_call(entry.doc) }
```

`retry:` also takes a callable `->(deliveries) { seconds }`, or an Array
with one wait for each try (`[15, 60, 600]`, the last one repeats). `due_every:` (1 s with `retry:`) is how often
the worker claims the due entries. The calls one by one:

| ndb only | Ruby |
|---|---|
| `nack` | `group.nack("w1", *ids, delay: 60)` (seconds; returns the count) |
| due claim | `group.claim_due("w2", cursor: "0-0", count: 100)` (an `Ndb::Claimed`) |
| pending | `group.pending["waiting"]`, `group.pending_list(due: true)`, each row `"retry_at"` (a `Time` or `nil`) |

A dead letter entry has `"reason"`: `"max_deliveries"` or `"max_age"`.

## Redis Streams commands

`Ndb::Client` has the stream commands of the [redis-rb](https://github.com/redis/redis-rb)
gem (`Ndb::RedisCommands`), with the same arguments and the same answers as
redis-rb 5. An app that uses Redis Streams picks its backend in one place
and keeps its calls:

```ruby
# config/initializers/streams.rb: the factory of the app
STREAMS = ENV["STREAMS_BACKEND"] == "ndb" ? Ndb.connect : Redis.new

STREAMS.xadd("orders", { order_id: 42 }, maxlen: 1000)                      # "1731164400000-0"
STREAMS.xgroup(:create, "orders", "billing", "$", mkstream: true)           # "OK"
STREAMS.xreadgroup("billing", "w1", "orders", ">", count: 10, block: 5000)  # { "orders" => [[id, { "order_id" => "42" }]] }
STREAMS.xack("orders", "billing", id)                                       # 1
```

Times are in milliseconds, as in redis-rb (`block:`, `min_idle_time`,
`idle:`).

| Method | Answer |
|---|---|
| `xadd(key, entry, approximate: nil, maxlen: nil, minid: nil, nomkstream: nil, id: "*")` | the ID (a String); `nil` with `nomkstream:` and no stream |
| `xlen(key)` | Integer (0 for no stream) |
| `xrange(key, start = "-", range_end = "+", count: nil)` | `[[id, { "field" => "value" }], ...]`; `"(id"` leaves the ID out |
| `xrevrange(key, range_end = "+", start = "-", count: nil)` | the same, from the highest ID |
| `xread(keys, ids, count: nil, block: nil)` | `{ "stream" => [[id, fields], ...] }`, only the streams with entries; `{}` for none |
| `xdel(key, *ids)` | how many it deleted |
| `xtrim(key, len_or_id, strategy: "MAXLEN", approximate: false, limit: nil)` | how many it deleted (`strategy: "MINID"` with an ID) |
| `xgroup(subcommand, key, group, id_or_consumer = nil, mkstream: false)` | `create`, `setid`: `"OK"`; `destroy`, `createconsumer`: 1 or 0; `delconsumer`: the pending count |
| `xreadgroup(group, consumer, keys, ids, count: nil, block: nil, noack: nil)` | as `xread`. `">"`: new entries. An ID (`"0"`): the pending entries of the consumer after it; the key is in the answer also with `[]`, and a deleted entry is `[id, nil]` |
| `xack(key, group, *ids)` | how many were pending |
| `xpending(key, group)` | `{ "size" => 2, "min_entry_id" => id, "max_entry_id" => id, "consumers" => { "w1" => "2" } }` |
| `xpending(key, group, start, end, count, consumer = nil, idle: nil)` | `[{ "entry_id" => id, "consumer" => "w1", "elapsed" => 1200, "count" => 1 }, ...]` |
| `xclaim(key, group, consumer, min_idle_time, *ids, idle:, retrycount:, force:, justid:)` | `[[id, fields], ...]`; the IDs with `justid: true` |
| `xautoclaim(key, group, consumer, min_idle_time, start, count: nil, justid: false)` | `{ "next" => "0-0", "entries" => [[id, fields], ...] }`; `"entries"` are IDs with `justid: true` |
| `xinfo(:stream, key)` | `{ "length", "last-generated-id", "max-deleted-entry-id", "entries-added", "recorded-first-entry-id", "groups", "first-entry", "last-entry" }`; an entry is `[id, [field, value, ...]]` |
| `xinfo(:groups, key)` | `[{ "name", "consumers", "pending", "last-delivered-id", "entries-read", "lag" }, ...]` |
| `xinfo(:consumers, key, group)` | `[{ "name", "pending", "idle", "inactive" }, ...]` |

The differences from Redis:

- **Values.** Redis keeps strings. ndb keeps the JSON value that `xadd` got:
  a number stays a number and a nested Hash stays an object, so a query or
  an index can use it. A read gives every value as a String, as Redis does:
  a string as it is, a number or a boolean with `to_s` (`42` is `"42"`),
  `nil` as `""`, and a nested value as its JSON text
  (`{ city: "Recife" }` is `'{"city":"Recife"}'`). redis-rb refuses a nested
  value in `xadd`; ndb takes it. Field names become Strings.
- **Field names.** A name that starts with `_` is reserved (`_id`, `_ts`).
- **Many streams.** ndb reads one stream for each request. `xread` and
  `xreadgroup` with many keys and no `block:` send one request for each key,
  in order. With `block:` and more than one key they raise
  `Ndb::NotSupported`.
- **`block: 0`** waits with no limit, as in Redis: it is a loop of reads of
  30 seconds each. A blocking `xread` on a stream that does not exist looks
  for the stream every 0.5 seconds until the time ends.
- **Errors** are `Ndb::Error` subclasses, not `Redis::CommandError`: rescue
  both in code that runs on the two backends. The message starts as in
  Redis, and the daemon message follows in parentheses:

  | Case | Class | Message starts with |
  |---|---|---|
  | `xgroup(:create)` on a group that exists | `Ndb::GroupExists` | `BUSYGROUP` |
  | a group command on a group or a stream that does not exist | `Ndb::UnknownGroup`, `Ndb::UnknownCollection` | `NOGROUP` |
  | `xgroup(:create)` without `mkstream:` and no stream | `Ndb::UnknownCollection` | `ERR The XGROUP subcommand requires the key to exist` |
  | `xinfo(:stream)` or `xinfo(:groups)` with no stream | `Ndb::UnknownCollection` | `ERR no such key` |

  A different number of keys and IDs is an `ArgumentError`, sent before any
  request.
- **`xclaim`** has `idle:`, `retrycount:`, `force:` and `justid:`. `time:`
  raises `Ndb::NotSupported` (use `idle:`); any other option is an
  `ArgumentError`.
- **`xinfo(:stream)`** has no `radix-tree-keys` and `radix-tree-nodes`, and
  there is no `FULL` form.
- A trim or a delete keeps the pending references, as up to Redis 8.0.
  `xautoclaim` and `xclaim` drop the pending IDs whose entry is gone.
- These are methods of `Ndb::Client`. There is no `multi`, `pipelined` or
  other Redis command.

## Blobs

Large values live in the blob store. A document keeps only a reference.

```ruby
stored = ndb.put_blob(File.open("call.wav"), "audio/wav")   # an IO, streamed in chunks (POST /_blobs)
stored   # => {"$blob" => "sha256:...", "size" => 123456, "type" => "audio/wav"}

calls.insert(_id: "c9", recording: stored)

ndb.get_blob(stored["$blob"])                    # all the bytes (GET /_blobs/{ref})
ndb.get_blob(stored["$blob"], from: 0, to: 1023) # bytes 0..1023 (inclusive)
```

The same content always gives the same reference. An IO upload is never retried.

## Admin

```ruby
calls.stats                 # rows, indexes, files, bytes (GET /calls/_stats)
calls.compact               # merge every segment now (POST /calls/_compact)
calls.ensure_index("caller") # built in the background (PUT /calls/_indexes/caller) => the indexed fields
calls.drop_index("caller")

ndb.collections             # one definition per collection (GET /_collections)
ndb.stats                   # every collection, totals, version, uptime (GET /_stats)
ndb.size                    # disk use per collection and part (GET /_size)
```

## The low-level client

`Ndb::Client` has one method per route. The collection methods above call it. Use it when the collection name is only known at run time, or to send a body built elsewhere. The first argument is the collection name.

```ruby
ndb.ensure_collection("calls", indexes: ["status"], time: "at")    # PUT /calls

ndb.insert("calls", { _id: "c1", status: "answered" })             # POST /calls/_doc => {"_id", "_v"}
ndb.bulk("calls", [{ _id: "c2" }, { _id: "c3" }], expire_in: "1h") # POST /calls/_bulk => the results
ndb.get("calls", "c1")                                              # GET /calls/_doc/c1 => the document, or nil

ndb.update("calls", "c1", { "$set" => { status: "done" } }, guard: { _v: 1 })                 # POST /calls/_update/c1
ndb.update_by("calls", { status: "failed" }, { "$set" => { status: "retry" } }, many: true)    # POST /calls/_update_by_query
ndb.delete("calls", "c2")                                                                      # DELETE /calls/_doc/c2
ndb.delete_by("calls", { status: "retry" }, many: true)                                        # POST /calls/_delete_by_query

page = ndb.search("calls", query: { status: "answered" }, sort: { at: -1 }, limit: 50)   # POST /calls/_search => an Ndb::Page
ndb.count("calls", { status: "answered" })                                                # POST /calls/_count => an Integer
ndb.aggregate("calls", pipeline: [{ "$group" => { "_id" => "$status", "n" => { "$count" => {} } } }])   # POST /calls/_aggregate

ndb.expire("calls", "c1", in: "30m")    # POST /calls/_expire/c1
ndb.persist("calls", "c1")              # POST /calls/_persist/c1
ndb.ttl("calls", "c1")                  # GET /calls/_ttl/c1

ndb.watch("calls", "relay", query: { status: "failed" }) { |event| ndb.ack("calls", "relay", event["pos"]) }   # POST /calls/_watch, /calls/_ack

ndb.truncate("calls")                   # POST /calls/_truncate?confirm=calls
ndb.drop("calls")                       # DELETE /calls?confirm=calls
ndb.ensure_index("calls", "caller")     # PUT /calls/_indexes/caller
ndb.drop_index("calls", "caller")       # DELETE /calls/_indexes/caller
ndb.collection_stats("calls")           # GET /calls/_stats
ndb.compact("calls")                    # POST /calls/_compact
```

- `search`, `count` and `aggregate` send the body as it is: write the daemon operators yourself. Only a Regexp is turned into `$regex`.
- `update`, `update_by`, `delete_by` and `watch` take the Ruby forms of `query` (Array, Range, `nil`, Time, Regexp) in the query and the guard.
- `ndb.collection(name, definition)` returns the `Ndb::Collection` of the sections above.

## Errors

All errors inherit from `Ndb::Error`, which has `code` (the daemon code) and `status` (the HTTP status).

| Class | Code |
|---|---|
| `Unauthorized` | `unauthorized` |
| `NotReady` | `not_ready` |
| `NotFound` | `not_found` |
| `UnknownCollection` | `unknown_collection` |
| `InvalidUpdate` and its subclasses | `unknown_operator`, `no_update`, `conflicting_update`, `invalid_field`, `unindexed_many`, `empty_filter` |
| `UniqueViolation` | `unique_violation`, `duplicate_id` |
| `CatalogConflict` | `catalog_conflict` |
| `CatalogInvalid` | `catalog_invalid` |
| `ReadOnlyCollection` | `direct_collection` |
| `InvalidQuery` | `invalid_query`, `invalid_request` |
| `InvalidCursor` | `cursor_invalid`, `cursor_mismatch`, `cursor_expired` |
| `WatchLagged` | `watch_lagged` |
| `HistoryTruncated` | `history_truncated` |
| `TtlNotEnabled`, `TtlDerived` | `ttl_not_enabled`, `ttl_derived` |
| `NotAStream`, `InvalidStreamID`, `StreamIDTooSmall` (all `StreamError`) | `not_a_stream`, `invalid_stream_id`, `stream_id_too_small` |
| `UnknownGroup`, `GroupExists` | `unknown_group`, `group_exists` |
| `ConnectionError` | cannot reach the daemon. Subclasses: `ConnectError` (nothing was sent), `TimeoutError` |

Any other code (for example `unknown_route`, `pattern_read_only`) is a plain `Ndb::Error`. A response that is not a daemon error has the code `http_status`.

```ruby
begin
  calls.insert(_id: "c1")
rescue Ndb::UniqueViolation => e
  e.code    # "duplicate_id"
  e.status  # 409
end
```

## Testing

The tests of this gem build the daemon from the repository (`go build ./cmd/ndb`), start it on a short temp unix socket (macOS allows 104 bytes), wait for `GET /_health` to return 200, and stop it at exit. To do the same by hand:

```sh
ndb -config /nonexistent.yml -data-dir "$(mktemp -d)" -unix /tmp/ndb-test.sock
```

```ruby
ndb = Ndb.connect("unix:///tmp/ndb-test.sock")
```

Use a new collection name per test. Run the suite with `bundle exec rake test` in `sdk/ruby`.

The cases of the Redis Streams commands (`test/redis_commands_test.rb`) also run on a real Redis, to prove that the answers have the same shape, when `NDB_REDIS_ADDR` is set and the `redis` gem loads (it is not a dependency of this gem; without them the cases are skipped). They use their own keys and never flush the server:

```sh
docker run -d --rm --name ndb-redis-rb -p 6391:6379 redis:7.4
NDB_REDIS_ADDR=redis://127.0.0.1:6391 bundle exec rake test
```

## License

MIT.
