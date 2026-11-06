# Ruby

The gem `ndb`: the plain Ruby client. Stdlib only, no runtime dependency. Use it in a script, a worker or any Ruby project.

The source and the same guide are in [`sdk/ruby`](https://github.com/thadeu/ndb/tree/main/sdk/ruby).

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
ndb = Ndb::Client.new(socket: "/run/ndb/ndb.sock")                       # unix socket
ndb = Ndb::Client.new(url: "unix:///run/ndb/ndb.sock")                   # same, as a URL
ndb = Ndb::Client.new(url: "tcp://10.0.0.5:7070", token: "secret")       # TCP with a bearer token
ndb = Ndb::Client.new                                                    # reads NDB_URL and NDB_TOKEN
```

All options:

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

- The address is, in order: `url:`, `socket:`, env `NDB_URL`, then the default socket `/var/run/ndb/ndb.sock`.
- The token is `token:`, or env `NDB_TOKEN`. It goes only in the `Authorization` header. A unix socket does not need it.
- A bad address raises `ArgumentError` at once.
- `ready_timeout` is how long a call retries while the daemon is not ready (`not_ready`, or the socket is missing or refuses). It uses exponential backoff (50 ms up to 1 s) with jitter. A write is retried only when the daemon certainly did not process it. A read is also retried after a dropped connection. A timeout is never retried. When the deadline passes, the last error is raised.
- A reused keep-alive connection that the server closed is sent again only for a read. A write is never sent twice.
- The pool is thread-safe and drops its connections after a `fork`.

## Collections

```ruby
ndb.ensure_collection("calls",
                      indexes: ["status"],
                      unique: ["caller"],
                      time: "at")      # also: expires_field:, ttl:

ndb.collections   # => [{"name" => "calls", ...}]
```

Every collection has the primary key `_id`. You can send your own (any non-empty string), or leave it out: the daemon makes a UUIDv7 (`Ndb.new_id` makes the same kind of id on the client). For a natural key such as an email, keep `_id` and declare that field in `unique:`.

`ensure_collection` is idempotent. A different definition for an existing collection raises `Ndb::CatalogConflict`.

## Insert, update, delete

Every document has a version. A new document has version 1, and each change adds 1.

```ruby
results = ndb.insert("calls", [
  { _id: "c1", status: "answered", caller: "a", duration: 42 },
  { _id: "c2", status: "failed", caller: "b", duration: 5 }
])
results   # => [{"_id" => "c1", "v" => 1}, {"_id" => "c2", "v" => 1}]

ndb.get("calls", "c1")   # => {"_id" => "c1", ..., "_v" => 1}, or nil

# Atomic update by _id (a String) or by filter (a Hash): the target, then the write operators.
ndb.update("calls", "c1", { "$set" => { status: "done" }, "$unset" => { caller: true } })
# => {"matched" => 1, "modified" => 1, "doc" => {"_id" => "c1", ..., "_v" => 2}}

ndb.update("calls", { _id: "c1", _v: 2 }, { "$set" => { duration: 50 } })   # only if the version is still 2
ndb.update("stock", { _id: "a", qty: { "$gte" => 1 } }, { "$inc" => { qty: -1 } })   # guarded decrement

ndb.update("calls", { status: "failed" }, { "$set" => { status: "retry" } }, many: true)   # => {"matched" => 3, "modified" => 3}
ndb.update("counters", { name: "hits" }, { "$inc" => { n: 1 } }, upsert: true)

ndb.delete("calls", "c2")                                # by _id => {"deleted" => 1}
ndb.delete("calls", { status: "failed" }, many: true)
```

- Operators: `$set`, `$setOnInsert` (like `$set`, but only when an upsert creates the document), `$unset`, `$inc`, `$push` (with `{ "$each" => [...] }`), `$pull`, `$min`, `$max`. Field names can be dot paths. A key of the operators without `$` raises `ArgumentError` before anything is sent.
- Nothing matched is not an error: `matched` is 0. A stale `_v` is a filter that matches nothing, so read again and retry. `modified` is 0 when the operators leave the document as it is.
- `many: true` needs `_id` or an indexed field in the filter (`Ndb::UnindexedMany`), and does not apply to an `_id` String. `upsert: true` creates the document from the equality fields of the filter. The answer has `doc` only for one document.
- The contract errors are `Ndb::UnknownOperator`, `NoUpdate`, `ConflictingUpdate`, `InvalidField`, `UnindexedMany` and `EmptyFilter`, all under `Ndb::InvalidUpdate`.
- A duplicate `_id` raises `Ndb::UniqueViolation` (code `duplicate_id`). A broken unique field raises it too (code `unique_violation`).

## Expiry (TTL like Redis)

The collection needs `expires_field`.

```ruby
ndb.ensure_collection("sessions", { expires_field: "exp" })

ndb.insert("sessions", [{ _id: "s1" }], expire_in: 3600)  # seconds, or "1h" / "30m" / "1d"
ndb.update("sessions", "s1", { "$set" => { user: "u" } }, expire_in: "30m")

ndb.expire("sessions", "s1", in: 1800)                     # => {"_id", "v", "expires_at"}
ndb.expire("sessions", "s1", at: Time.now + 7200)
ndb.ttl("sessions", "s1")                                  # => 7199, -1 (never expires), or nil (missing or expired)
ndb.persist("sessions", "s1")                              # => {"_id", "v"}
```

- `expire_in` wins over a value of `expires_field` in the same call. `ttl` rounds up to whole seconds.
- `expire` and `persist` raise `Ndb::NotFound` for a missing or expired document. `Ndb::TtlNotEnabled` means no `expires_field`; `Ndb::TtlDerived` means the expiry comes from `ttl` + `time`.
- Disk space of expired documents returns after compaction, and fully only with `wal_retention`; see [Freeing disk](../configuration.md#freeing-disk).

## Find

`find` returns a `Ndb::Page` with `rows` (an array of Hash), `next_cursor`, `plan`, `examined` and `total`.

```ruby
page = ndb.find("calls",
                where: { status: "answered", duration: { "$gte" => 30, "$lte" => 60 } },
                sort: { at: -1 },
                fields: %w[_id status],
                limit: 50,
                skip: 0)

page.rows          # => [{"_id" => "c1", "status" => "answered"}, ...]
page.plan          # "id", "index:status", "scan" or "snapshot"
page.examined      # rows the daemon looked at
```

The body of `find` is the JSON of the [query language](../query.md): `where`, `sort`, `fields`, `limit`, `skip`, `after`, `count`, `explain`, `versions`. Symbol keys are fine.

In `find`, `where` is sent as it is: write the daemon operators yourself. (Only `watch` turns Ruby values such as Range and Array into operators, see below.)

```ruby
ndb.find("calls", where: { status: { "$in" => %w[answered failed] } })
ndb.find("calls", where: { duration: { "$gte" => 30, "$lte" => 60 } })
ndb.find("calls", where: { "$or" => [{ status: "failed" }, { duration: { "$gt" => 50 } }] })
ndb.find("calls", where: { "agent.id" => 7 })   # dot path
```

- `sort: { at: -1, _id: 1 }`: `1` ascending, `-1` descending. The key order is the sort order. The id is always the last key.
- `versions: true` adds the field `_v` (the document version) to each row.

### Regular expressions

A Ruby `Regexp` is accepted anywhere a value is: a field value, `"$regex" => /x/`, inside `$not`, `$or`, `$and`, in `find`, `aggregate` (`$match`) and in the `where:` of `watch`.

```ruby
ndb.find("calls", where: { name: /^alpha/i })                    # i: ignore case
ndb.find("calls", where: { name: { "$regex" => /bet$/ } })
ndb.find("calls", where: { "$or" => [{ name: /^a/ }, { name: /^b/ }] })
```

The daemon uses RE2. The mapping to `$options` is:

| Ruby | `$options` |
|---|---|
| always | `m` (Ruby `^` and `$` match at each line, so the daemon must do the same) |
| `/i` (IGNORECASE) | `i` |
| `/m` (MULTILINE: `.` matches a newline) | `s` |
| `/x` (EXTENDED) | `x` |

Ruby features that RE2 does not have raise `Ndb::InvalidQuery` (code `invalid_query`) **before** any request: look-around (`(?=`, `(?!`, `(?<=`, `(?<!`), backreferences (`\1` to `\9`, `\k<name>`), atomic groups (`(?>`), possessive quantifiers (`a++`), `\h`, `\G`, `\R` and `\X`.

A String pattern is sent unchanged and has no `m`: `where: { name: { "$regex" => "^abc" } }`. A pattern anchored with `^` and a literal start, without `i` or `m`, uses the index on the field (`page.plan` is `index:name`). A Regexp always sends `m`, so it is a scan filter, not an index range. For a prefix search that uses the index, send the String form.

### Count

```ruby
page = ndb.find("calls", where: { status: "answered" }, count: true, limit: 1)
page.total   # how many documents match, whatever the limit
```

### Cursor pagination

```ruby
cursor = nil

loop do
  page = ndb.find("calls", where: { status: "answered" }, sort: { _id: 1 }, limit: 100, after: cursor)

  page.rows.each { |row| puts row["_id"] }

  cursor = page.next_cursor

  break if cursor.nil?   # nil on the last page
end
```

A cursor works only with the same `where` and `sort`. Another query shape raises `Ndb::InvalidCursor`.

## Aggregate

The pipeline needs exactly one `$group`. Accumulators: `$count`, `$sum`, `$avg`, `$min`, `$max`, `$first`, `$last`.

```ruby
page = ndb.aggregate("calls",
                     pipeline: [
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
```

`$dateTrunc` units are `minute`, `hour`, `day`, `week` and `month`.

## Watch

A watch streams the changes of a collection to a named consumer: first the events after its last ack, then new events as they happen. Use it for outbox-style work.

```ruby
begin
  ndb.watch("calls", "relay", where: { status: "failed" }) do |event|
    # event has "pos", "op" ("put" or "del"), "_id", "v" and "doc"
    handle(event)

    ndb.ack("calls", "relay", event["pos"])
  end
rescue Ndb::WatchLagged
  retry # the consumer was too slow. watch continues after the last ack.
rescue Ndb::HistoryTruncated
  raise # the saved position is too old. Start over.
end
```

- `where:` takes the same conditions as the models of `ndb-rails`: equality, an Array (`$in`), a Range, a Regexp (see Regular expressions), `nil` (`$exists: false`) and `$` operators as keys. Only matching events come. A delete event has no `"doc"`; it is sent only if this stream saw an earlier matching put of that id.
- `from: "now"` starts a new consumer at the end. `limit: n` ends the stream after `n` events.
- Without a block, `watch` returns an Enumerator: `ndb.watch("calls", "relay", limit: 10).to_a`.
- Delivery is at least once. After a restart an event can come again, so make the work safe to repeat. `ack` stores the position of an event: the next `watch` with that consumer name starts after it.
- `HistoryTruncated` means the WAL files for the position were deleted (`wal_retention`).
- A watch holds one connection until it ends. It is retried only for the initial open, never in the middle of a stream.

## Blobs

Large values live in the blob store. A document keeps only a reference.

```ruby
stored = ndb.put_blob(File.open("call.wav"), "audio/wav")   # an IO, streamed in chunks
stored   # => {"$blob" => "sha256:...", "size" => 123456, "type" => "audio/wav"}

ndb.insert("calls", [{ _id: "c9", recording: stored }])

ndb.get_blob(stored["$blob"])                    # all the bytes
ndb.get_blob(stored["$blob"], from: 0, to: 1023) # bytes 0..1023 (inclusive)
```

The same content always gives the same reference. An IO upload is never retried.

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
| `ConnectionError` | cannot reach the daemon. Subclasses: `ConnectError` (nothing was sent), `TimeoutError` |

Any other code is a plain `Ndb::Error`. A response that is not a daemon error has the code `http_status`.

```ruby
begin
  ndb.insert("calls", [{ _id: "c1" }])
rescue Ndb::UniqueViolation => e
  e.code    # "duplicate_id"
  e.status  # 409
end
```

## Testing

The tests of this gem build the daemon from the repository (`go build ./cmd/ndb`), start it on a short temp unix socket (macOS allows 104 bytes), wait for `GET /health` to return 200, and stop it at exit. To do the same by hand:

```sh
ndb -config /nonexistent.yml -data-dir "$(mktemp -d)" -unix /tmp/ndb-test.sock
```

```ruby
ndb = Ndb::Client.new(socket: "/tmp/ndb-test.sock")
```

Use a new collection name per test. Run the suite with `bundle exec rake test` in `sdk/ruby`.
