# Ruby

The gem `ndb`: the plain Ruby client. Stdlib only, no runtime dependency. Use it in a script, a worker or any Ruby project. For Rails models, see [Rails](rails.md).

The source and the same guide are in [`sdk/ruby`](https://github.com/thadeu/ndb/tree/main/sdk/ruby).

It has two layers:

- **`Ndb.connect` and `db.collection`**: a collection handle with a query builder, the same vocabulary as the Go and TypeScript SDKs (`insert`, `update`, `update_by`, `delete`, `delete_by`, `query`, `search`, `count`, `watch`). Use this one.
- **`Ndb::Client`**: the low layer, one method per [HTTP route](../http.md), with the collection name as the first argument. The collection layer is built on it.

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

### The ndb executable

The gem installs an `ndb` command. It has one command of its own, `streams`:

```sh
bundle exec ndb streams [-r file] [-e env] [--only A,B] [-c N]
```

- `ndb streams` runs the stream consumers of an app: the classes with `include Ndb::Streams`. These classes are in the gem `ndb-rails`, so the command needs that gem (without it: a message and exit status 2). The flags and the behavior are on [Rails: Run the consumers](rails.md#run-the-consumers).
- **Pass-through.** Every other command (`ndb serve`, `ndb bench`, `ndb -h`, ...) goes to the ndb daemon binary, with the same arguments. So the gem does not hide the daemon when the two have the name `ndb` in `PATH`.
- The binary is `NDB_BIN` when it is set. If not, it is the first `ndb` of `PATH` that is a native executable (ELF or Mach-O). So a binstub, the stub that RubyGems makes and the shell shim of a version manager (rbenv) are skipped, and so is any file in a `shims` directory (mise, asdf).
- Before the exec, the command sets `NDB_GEM_EXEC=1`. When it starts again with that variable (the binary came back to it), it prints a message and exits with status 127: it never runs itself in a loop. Set `NDB_BIN` to the daemon binary then.
- When there is no binary, the command prints how to install it and exits with status 127.

## Connect

```ruby
db = Ndb.connect("unix:///run/ndb/ndb.sock")                 # unix socket
db = Ndb.connect("tcp://10.0.0.5:7070", token: "secret")     # TCP with a bearer token
db = Ndb.connect                                             # reads NDB_URL and NDB_TOKEN
```

`Ndb.connect(addr, **options)` returns an `Ndb::Client`. All options:

```ruby
Ndb.connect(
  "unix:///run/ndb/ndb.sock", # or "tcp://host:port", "https://host", or a bare path that starts with /
  token: nil,                 # bearer token
  timeout: 30,                # seconds, per request
  pool_size: 16,              # idle connections kept
  ready_timeout: 10           # seconds to retry while the daemon starts; 0 turns it off
)
```

- The address is, in order: the argument, env `NDB_URL`, then the default socket `/run/ndb/ndb.sock`.
- `/run/ndb/ndb.sock` is the socket of a server install (root, or `/var/lib/ndb` exists). A local daemon that you start as a user with no flags (`ndb serve`) listens on `~/.ndb/ndb.sock` and on TCP `127.0.0.1:7070`. Connect to it with `Ndb.connect("unix://#{Dir.home}/.ndb/ndb.sock")` or `Ndb.connect("tcp://127.0.0.1:7070")`, or set `NDB_URL`.
- The token is `token:`, or env `NDB_TOKEN`. It goes only in the `Authorization` header. A unix socket does not need it.
- A bad address raises `ArgumentError` at once.
- `ready_timeout` is how long a call retries while the daemon is not ready (`not_ready`, or the socket is missing or refuses). It uses exponential backoff (50 ms up to 1 s) with jitter. A write is retried only when the daemon certainly did not process it. A read is also retried after a dropped connection. A timeout is never retried. When the deadline passes, the last error is raised.
- A read that gets no answer in `timeout` raises `Ndb::TimeoutError`, also on a reused connection. It is not sent again.
- A reused keep-alive connection that the server closed is sent again only for a read. A write is never sent twice.
- The pool is thread-safe and drops its connections after a `fork`. A connection goes back to the pool only after a request that ended with no raise. After any other end (an error, `Timeout.timeout`, `Thread#kill`, `rack-timeout`), it is closed, so the next call never reads the answer of another call.

## Collections

```ruby
calls = db.collection("calls",
                      indexes: ["status"],
                      unique: ["caller"],
                      time: "at")      # also: expires_field:, ttl:, durability:...
```

- `collection` sends nothing. The first call on it declares the collection (`PUT /calls`) with this definition, once per handle. The keys are on [PUT /{coll}](../http/collections.md#put-coll).
- Every collection has the primary key `_id`. You can send your own (any non-empty string), or leave it out: the daemon makes a UUIDv7 (`Ndb.new_id` makes the same kind of id on the client). For a natural key such as an email, keep `_id` and declare that field in `unique:`.
- A different definition for an existing collection raises `Ndb::CatalogConflict`.
- A name with `*` at the end is a [collection pattern](#collection-patterns): it never declares.

## Insert, get, update, delete

```ruby
calls.insert(_id: "c1", status: "answered", at: Time.now)
# => {"_id" => "c1", "_v" => 1}                  one document: POST /calls/_doc

calls.insert([{ _id: "c2", status: "failed" }, { status: "answered" }])
# => [{"_id" => "c2", "_v" => 1}, {"_id" => "0192...", "_v" => 1}]   many, all or nothing: _bulk

calls.get("c1")   # => {"_id" => "c1", "_v" => 1, ...}, or nil

calls.update("c1", { "$set" => { status: "done" }, "$unset" => { caller: true } })
# => {"matched" => 1, "modified" => 1, "doc" => {...with "_v"}}

calls.update("c1", { "$set" => { duration: 50 } }, guard: { _v: 2 })   # only if the version is still 2
calls.update("c9", { "$inc" => { hits: 1 } }, upsert: true)            # create c9 when missing

stock.update_by({ sku: "a", qty: { "$gte" => 1 } }, { "$inc" => { qty: -1 } })   # guarded decrement
calls.update_by({ status: "failed" }, { "$set" => { status: "retry" } }, many: true)   # => {"matched" => 3, "modified" => 3}

calls.delete("c2")                                   # by _id => {"deleted" => 1}
calls.delete_by({ status: "failed" }, many: true)    # by query
```

- Operators: `$set`, `$setOnInsert` (like `$set`, but only when an upsert creates the document), `$unset`, `$inc`, `$push` (with `{ "$each" => [...] }`), `$pull`, `$min`, `$max`. Field names can be dot paths. A key of the operators without `$` raises `ArgumentError` before anything is sent.
- Nothing matched is not an error: `matched` is 0. A stale `_v` guard matches nothing, so read again and retry. `modified` is 0 when the operators leave the document as it is.
- `update(id, ...)` takes `guard:`, `upsert:` and `expire_in:`. `update_by(query, ...)` takes `many:`, `upsert:` and `expire_in:`; `many: true` needs `_id` or an indexed field in the query (`Ndb::UnindexedMany`), and `upsert: true` creates the document from the equality fields of the query. The answer has `doc` only for one document.
- The query of `update_by` and `delete_by` takes the Ruby forms of [Search](#search) (Range, Array as `$in`, Regexp).
- The contract errors are `Ndb::UnknownOperator`, `NoUpdate`, `ConflictingUpdate`, `InvalidField`, `UnindexedMany` and `EmptyFilter`, all under `Ndb::InvalidUpdate`.
- A duplicate `_id` raises `Ndb::UniqueViolation` (code `duplicate_id`). A broken unique field raises it too (code `unique_violation`).
- `upsert(doc)` and `upsert_all(docs)` create by `_id`, or merge into the stored document when the `_id` exists, in one transaction. See [Upsert](../http/documents.md#upsert).

### Truncate and drop

```ruby
calls.truncate   # every document goes; the definition and indexes stay => {"collection" => "calls", "truncated" => true}
calls.drop       # the collection, its definition and its watch consumers go
```

- Both take the same time for 10 or 10 million documents. Watch consumers get one event with `"op" => "truncate"` (no `_id`), then the new changes.
- Both send the collection name as the `confirm` value for you.
- A direct-mode collection raises `Ndb::ReadOnlyCollection`. A collection declared in `ndb.yml` comes back, empty, when the daemon restarts after a drop. After `drop`, the next call on the handle declares it again.

### Rename and restore

```ruby
calls.rename("calls_old")                    # => {"collection", "renamed_from", "replaced"}
calls.rename("calls_live", replace: true)    # an existing calls_live goes aside first

calls.pitr                                   # => {"collection", "window", "oldest", "newest", "snapshots", "bytes"}
calls.restore(at: Time.now - 600, to: "calls_10min_ago")   # => {"collection", "restored", "at", "duration_ms"}
```

- `rename` without `replace:` raises `Ndb::CollectionExists` when the new name exists. The handle keeps the old name.
- `restore` makes the collection `to:` hold this collection as it was at `at:` (a `Time` or an RFC 3339 string), from the PITR window (1 day by default). It raises `Ndb::OutOfWindow`, `Ndb::PitrOff` or `Ndb::CollectionExists`.

## Expiry (TTL like Redis)

The collection needs `expires_field`.

```ruby
sessions = db.collection("sessions", expires_field: "exp")

sessions.insert({ _id: "s1" }, expire_in: 3600)            # seconds, or "1h" / "30m" / "1d"
sessions.update("s1", { "$set" => { user: "u" } }, expire_in: "30m")

sessions.expire("s1", in: 1800)                            # => {"_id", "_v", "expires_at"}
sessions.expire("s1", at: Time.now + 7200)
sessions.ttl("s1")                                         # => 7199, -1 (never expires), or nil (missing or expired)
sessions.persist("s1")                                     # => {"_id", "_v"}
```

- `expire_in` wins over a value of `expires_field` in the same call. `ttl` rounds up to whole seconds.
- `expire` and `persist` raise `Ndb::NotFound` for a missing or expired document. `Ndb::TtlNotEnabled` means no `expires_field`; `Ndb::TtlDerived` means the expiry comes from `ttl` + `time`.
- Disk space of expired documents returns after compaction, and fully only with `wal_retention`; see [Freeing disk](../configuration.md#freeing-disk).

## Search

`query` starts an immutable search; every step returns a new one. It runs on `all`, `first`, `count`, `each` or `page`.

```ruby
rows = calls.query(status: "answered", duration: 30..60)
            .sort(at: -1)
            .fields(:_id, :status)
            .limit(50)
            .all
# => [{"_id" => "c1", "status" => "answered"}, ...]

calls.query(status: "answered").first        # one row, or nil
calls.query(status: "answered").count        # POST /calls/_count
calls.query.sort(_id: 1).limit(500).each { |row| puts row["_id"] }   # every row, page by page

page = calls.query(status: "answered").limit(50).page(count: true)
page.rows; page.next; page.total; page.plan; page.examined
```

`query` takes the Ruby forms of the conditions, and turns them into the [query language](../query.md):

| Ruby | Query |
|---|---|
| `status: "answered"` | `{"status": "answered"}` |
| `status: %w[a b]` | `{"status": {"$in": ["a", "b"]}}` |
| `duration: 30..60`, `30..`, `..60` | `{"$gte": 30, "$lte": 60}` |
| `at: Time` | an RFC 3339 string |
| `name: /^a/i` | `$regex` (see below) |
| `caller: nil` | `{"caller": {"$exists": false}}` |
| `"$or" => [...]`, `{ "$gte" => 1 }` | as is |

Several `query` calls are ANDed. `sort(at: -1, _id: 1)`: `1` ascending, `-1` descending, the key order is the sort order, and `_id` is always the last key.

### Regular expressions

A Ruby `Regexp` is accepted anywhere a value is: a field value, `"$regex" => /x/`, inside `$not`, `$or`, `$and`, in a search, an `aggregate` (`$match`) and the `query:` of `watch`.

```ruby
calls.query(name: /^alpha/i).all                     # i: ignore case
calls.query(name: { "$regex" => /bet$/ }).all
calls.query("$or" => [{ name: /^a/ }, { name: /^b/ }]).all
```

The daemon uses RE2. The mapping to `$options` is:

| Ruby | `$options` |
|---|---|
| always | `m` (Ruby `^` and `$` match at each line, so the daemon must do the same) |
| `/i` (IGNORECASE) | `i` |
| `/m` (MULTILINE: `.` matches a newline) | `s` |
| `/x` (EXTENDED) | `x` |

Ruby features that RE2 does not have raise `Ndb::InvalidQuery` (code `invalid_query`) **before** any request: look-around (`(?=`, `(?!`, `(?<=`, `(?<!`), backreferences (`\1` to `\9`, `\k<name>`), atomic groups (`(?>`), possessive quantifiers (`a++`), `\h`, `\G`, `\R` and `\X`.

A String pattern is sent unchanged and has no `m`: `name: { "$regex" => "^abc" }`. A pattern anchored with `^` and a literal start, without `i` or `m`, uses the index on the field (`page.plan` is `index:name`). A Regexp always sends `m`, so it is a scan filter, not an index range. For a prefix search that uses the index, send the String form.

### Queries built elsewhere

A search form or a saved search goes as is. `raw_query` takes a query Hash with no Ruby conversion (an Array stays an Array) and chains like `query`; `search` takes a whole search body and returns an `Ndb::Page`:

```ruby
calls.raw_query(JSON.parse(params[:q])).query(dnis: "1000").sort(at: -1).all

page = calls.search(JSON.parse(params[:body]))   # { "query", "sort", "limit", "after", "count" }
page.rows; page.next; page.total
```

The daemon validates the JSON (`invalid_query`), but it takes any field: for a public form, keep an allowlist of fields and operators, cap `limit`, and ask for an indexed field or a time window, as you would before sending a form to Elasticsearch `_search`.

### Collection patterns

A collection name with `*` at the end reads every collection whose name starts with the prefix (see [Collection patterns](../http/queries.md#collection-patterns)). Each row has `"_coll"`, the name of its collection. Writes raise `Ndb::InvalidPattern` with code `pattern_read_only`.

```ruby
logs = db.collection("logs-2026-*")
logs.query(lvl: "error").sort(at: -1).all.first["_coll"]   # => "logs-2026-11"
logs.count(lvl: "error")
```

### Cursor pagination

`each` follows the cursor for you. By hand:

```ruby
search = calls.query(status: "answered").sort(_id: 1).limit(100)
page = search.page

loop do
  page.rows.each { |row| puts row["_id"] }

  break if page.next.nil?   # nil on the last page

  page = search.after(page.next).page
end
```

A cursor works only with the same query and sort. Another query shape raises `Ndb::InvalidCursor`.

## Lookup

`lookup` is a key of the HTTP search (`POST /{coll}/_search`): in the same call, the daemon reads the documents of other collections that each row points to, and puts them in the row. The HTTP API is the primitive and has every rule ([Lookup](../query.md#lookup)); this SDK only builds that JSON.

| Relation | The foreign key lives | Match | The row gets |
|---|---|---|---|
| belongs to | in **this** collection | `row.fk == other.pk` | an object, or null |
| has many | in the **other** collection | `other.fk == row.pk` | an array (100 unless a limit, at most 1000) |
| has one | in the **other** collection | `other.fk == row.pk` | an object, or null |

`pk` is `_id` unless you say otherwise. The field that is searched on the other side (its `pk` for a belongs to, its `fk` for a has many) must be `_id` or indexed, else the daemon answers `lookup_unindexed`.

### Basic use

The relations are the JSON of the HTTP API as a Hash, with Symbol or String keys:

```ruby
orders = Ndb::Collection.new(client, "orders")

# belongs to: orders.customer_id -> customers._id
rows = orders.query(status: "paid")
             .lookup(customer: { belongs_to: "customers", fk: :customer_id })
             .all

rows.first["customer"]["name"] # "Ana" (nil when there is no customer)

# has many: order_items.order_id -> orders._id
orders.query({}).lookup(items: { has_many: "order_items", fk: :order_id }).all

# has one: invoices.order_id -> orders._id
orders.query({}).lookup(invoice: { has_one: "invoices", fk: :order_id }).all
```

### Options

```ruby
orders.query({}).lookup(
  items: {
    has_many: "order_items", fk: :order_id,   # or foreign_key:
    query: { qty: { "$gt" => 0 } },          # filter the related documents
    sort: { pos: 1 },                         # their order
    limit: 50,                                # per row, 1 to 1000
    fields: %w[sku qty],                      # keep these fields
    lookup: {                                 # nested, two levels at most
      product: { belongs_to: "products", fk: :sku, pk: :code }   # or primary_key:
    }
  }
).all
```

`lookup` can be called more than once; the relations add up. `query`, `sort` and `limit` are for `has_many` and `has_one`.

### Index

`has_many: "order_items", fk: :order_id` needs an index on `order_items.order_id`: `client.ensure_index("order_items", "order_id")`, or `indexes: ["order_id"]` in its definition.

### Errors

`Ndb::LookupError` (codes `lookup_invalid`, `lookup_unindexed`, `lookup_too_large`) and `Ndb::UnknownCollection`. In Rails, use the [associations](rails.md#lookup).

## Aggregate

The pipeline needs exactly one `$group`. Accumulators: `$count`, `$sum`, `$avg`, `$min`, `$max`, `$first`, `$last`. See [Aggregate](../aggregate.md).

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
```

`$dateTrunc` units are `minute`, `hour`, `day`, `week` and `month`.

## Watch

A watch streams the changes of a collection to a named consumer: first the events after its last ack, then new events as they happen. Use it for outbox-style work. See [Watch](../watch.md).

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

- `query:` takes the Ruby forms of [Search](#search). Only matching events come. A delete event has no `"doc"`; it is sent only if this stream saw an earlier matching put of that id.
- `from: "now"` starts a new consumer at the end. `limit: n` ends the stream after `n` events.
- Without a block, `watch` returns an Enumerator: `calls.watch("relay", limit: 10).to_a`.
- Delivery is at least once. After a restart an event can come again, so make the work safe to repeat. `ack(event, consumer)` stores the position of an event: the next `watch` with that consumer name starts after it.
- `HistoryTruncated` means the WAL files for the position were deleted (`wal_retention`).
- A watch holds one connection until it ends. It is retried only for the initial open, never in the middle of a stream.

## Streams

A stream is an append-only log with consumer groups, with the behavior of Redis Streams. Apps append entries. The workers of a group each take a part of the entries, do the work and ack them. See [Streams](../streams.md) for the model and [Stream routes](../http/streams.md) for the wire contract.

A stream is also a collection: `db.collection("orders").query(...)` searches it with every filter.

### `stream`

```ruby
orders = db.stream("orders")   # sends nothing
orders.name                    # => "orders"
```

- `db.stream(name)` returns an `Ndb::Stream`. It sends nothing until the first call.
- The first `append` or `group.create` makes the stream.

### `create`

```ruby
orders.create                                  # makes the stream, or checks that it is one
orders.create(indexes: ["customer"], retention: "7d")
```

- Use it for a stream with options. It sends `PUT /{s}` with `"stream": true`.
- The definition takes the keys of a collection, except `time` (it is `_ts`) and `unique`.
- A collection that is not a stream raises `Ndb::NotAStream`.

### `append`

```ruby
id = orders.append({ order: 42, total: 99.9 })            # => "1731164400000-0"
orders.append({ order: 43 }, id: "1731164400500-*")       # this ms, the daemon makes seq
orders.append({ order: 44 }, trim: { max_len: 10_000, approx: true })
orders.append({ order: 45 }, no_create: true)             # NOMKSTREAM
```

- Adds one entry (`XADD`) and returns its ID, `"<ms>-<seq>"`.
- `id:` is `"<ms>-<seq>"` or `"<ms>-*"`. The default is `"*"`: the daemon makes the ID.
- `trim:` is a Hash with the keywords of [`trim`](#trim). The trim runs after the append.
- `no_create: true` raises `Ndb::UnknownCollection` when the stream does not exist.
- An `id:` that is not greater than the last ID raises `Ndb::StreamIDTooSmall`. A bad ID raises `Ndb::InvalidStreamID`.

### `append_many`

```ruby
ids = orders.append_many([{ order: 46 }, { order: 47 }])   # => ["...-0", "...-1"]
```

- Adds the entries in order, with one fsync for all, and returns their IDs.
- It takes the same `id:`, `trim:` and `no_create:` as `append`. `id:` applies to the first entry.

### `trim`

```ruby
orders.trim(max_len: 1000)                    # keep the newest 1000 entries
orders.trim(min_id: "1731160000000-0")        # delete the entries with a lower ID
orders.trim(max_len: 1000, approx: true, limit: 5000)
```

- Deletes old entries (`XTRIM`) and returns how many.
- `approx: true` deletes only whole blocks of 1 024 entries. `limit:` caps the count with `approx`.

### `info`

```ruby
orders.info
# => {"length" => 3, "first_id" => "...", "last_id" => "...", "entries_added" => 3,
#     "max_deleted_id" => "0-0", "groups" => 1}
```

- The state of the stream (`XINFO STREAM`).

### `range` and `rev_range`

```ruby
entries = orders.range("-", "+", count: 100)     # XRANGE
entries.first.id; entries.first.time; entries.first.doc

orders.rev_range("+", "-", count: 10)            # XREVRANGE: newest first
```

- `range(start = "-", stop = "+", count: nil)` gives the entries with an ID from `start` to `stop`, both included, in ID order. `rev_range(stop = "+", start = "-", count: nil)` gives them in the reverse order.
- `"-"` and `"+"` are the lowest and the highest ID. An ID without `-seq` is `<ms>-0` as a start and the last ID of that ms as an end.
- Each item is an `Ndb::Entry`: `id`, `time` (a UTC `Time`, from the ms of the ID), `doc` (the Hash without `_id` and `_ts`), `deleted?` and `raw` (the row as the daemon sent it).

### `read` without a group

```ruby
entries = orders.read(after: "0", count: 100)        # from the start of the stream
entries = orders.read(after: entries.last.id, block: 5)   # the next ones; wait up to 5 s

orders.read(after: "$", block: 5)                    # only the entries appended after this call starts
```

- `read(after: "0", count: nil, block: nil, fields: nil)` gives up to `count` entries with an ID greater than `after:`, in ID order (`XREAD`). It returns an Array of `Ndb::Entry`; an empty Array means no entry.
- It uses no group: it changes no group and no pending list, and nothing is acked.
- `after:` is an ID (`"0"`: the start of the stream) or `"$"`: the last ID when the request starts, so only later entries come.
- `count:` is the most entries (the daemon default is 10, at most 10 000).
- `block:` is in seconds (a Float is fine). With no entry, the read waits up to this long. The read timeout of that request is `block` + 5 seconds, or the client `timeout` when it is longer.
- `fields:` gives only these fields (and `_id`).
- To read on, give the ID of the last entry as `after:`. Do not give `"$"` again: an entry appended between two calls would be lost.
- A stream that does not exist raises `Ndb::UnknownCollection`.

### `delete`

```ruby
orders.delete("1731164400000-0")   # => 1, or 0 when it is not there
```

- Removes one entry by ID (`XDEL`). It stays in the pending list of a group until it is acked: a read of the pending entries gives it with `deleted?` true and `doc` nil.

### `groups`

```ruby
orders.groups
# => [{"name" => "billing", "consumers" => 2, "pending" => 5,
#      "last_delivered_id" => "...", "entries_read" => 40, "lag" => 0}]
```

- The groups of the stream (`XINFO GROUPS`). `lag` is nil when it cannot be known.

### `group`

```ruby
billing = orders.group("billing")   # sends nothing
billing.name; billing.stream
```

- Returns an `Ndb::Group`. Each entry goes to one consumer of the group, and stays pending until it is acked.

### `group.create` and `group.ensure`

```ruby
billing.create(Ndb::Stream::START_NEW)   # "$": only the entries appended after now
billing.create(Ndb::Stream::START_ALL)   # "0": every entry of the stream
billing.create("1731160000000-0")        # the entries after this ID

billing.ensure                           # makes it, or does nothing when it exists

billing.create(Ndb::Stream::START_NEW, no_create: true)   # XGROUP CREATE without MKSTREAM
```

- `create(start = Ndb::Stream::START_NEW, no_create: false)` makes the group (`XGROUP CREATE ... MKSTREAM`). It makes the stream too.
- `no_create: true` does not make the stream: it raises `Ndb::UnknownCollection` when the stream does not exist.
- `create` raises `Ndb::GroupExists` when the group exists. `ensure(start = START_NEW)` does not: use it at boot.

### `group.set_id`

```ruby
billing.set_id("$")                  # skip every entry that is there now
billing.set_id("1731160000000-0")
```

- Moves the last delivered ID of the group (`XGROUP SETID`).

### `group.destroy`

```ruby
billing.destroy
```

- Deletes the group and its pending entries (`XGROUP DESTROY`). The entries stay in the stream.

### `group.create_consumer`, `delete_consumer` and `consumers`

```ruby
billing.create_consumer("w1")   # => true when it is new (XGROUP CREATECONSUMER)
billing.consumers               # => [{"name" => "w1", "pending" => 0, "idle_ms" => 12, "inactive_ms" => 12}]
billing.delete_consumer("w1")   # => how many pending entries it held (XGROUP DELCONSUMER)
```

- A read makes the consumer too, so `create_consumer` is optional.
- `delete_consumer` drops the pending entries of that consumer. Claim them first if they must be done.

### `group.read`

```ruby
entries = billing.read("w1", count: 100, block: 5)    # new entries, wait up to 5 s
mine = billing.read("w1", pending: true)              # its own pending entries
more = billing.read("w1", pending: true, after: mine.last.id)
billing.read("w1", no_ack: true, fields: %w[order])
```

- Gives entries to the consumer (`XREADGROUP`) and returns an Array of `Ndb::Entry`. An empty Array means no entry.
- By default it gives new entries (`>` in Redis). Each one becomes pending for this consumer.
- `count:` is the most entries (the daemon default is 10, at most 10 000).
- `block:` is in seconds (a Float is fine). With no new entry, the read waits up to this long. The read timeout of that request is `block` + 5 seconds, or the client `timeout` when it is longer.
- `pending: true` gives its own pending entries with an ID greater than `after:` (default `"0"`). Each one counts as a new delivery (delivery count + 1), as in Redis. A deleted entry comes with `deleted?` true. `block:` does not apply.
- `no_ack: true` does not add the entries to the pending list (`NOACK`): at most once.
- `fields:` gives only these fields (and `_id`).
- A group that does not exist raises `Ndb::UnknownGroup`.

### `group.ack`

```ruby
billing.ack(entries.first.id)        # => 1
billing.ack(*entries.map(&:id))
billing.ack(entries.map(&:id))       # an Array is fine too
```

- Ends the work on the entries (`XACK`). It returns how many were pending. An ID that is not pending counts 0. No ID sends nothing and returns 0.

### `group.pending`

```ruby
billing.pending
# => {"count" => 5, "lowest" => "...", "highest" => "...",
#     "consumers" => [{"name" => "w1", "count" => 4}], "waiting" => 1}
```

- The pending list in short (`XPENDING`).
- `"waiting"` (ndb only): the nacked entries whose retry time has not come. A nacked entry counts in `"count"`, and for no consumer in `"consumers"`.

### `group.pending_list`

```ruby
billing.pending_list(idle: 60, count: 100)
# => [{"_id" => "...", "consumer" => "w1", "idle_ms" => 61000, "deliveries" => 2, "retry_at" => nil},
#     {"_id" => "...", "consumer" => "", "idle_ms" => 3000, "deliveries" => 1, "retry_at" => 2026-11-10 14:01:00 UTC}]

billing.pending_list(start: "-", stop: "+", consumer: "w1")
billing.pending_list(due: false)   # the nacked entries that still wait
```

- The pending entries in ID order (`XPENDING` with a range).
- Keywords: `start:` (`"-"`), `stop:` (`"+"`), `count:` (100), `consumer:` (all), `idle:` (seconds: only the entries idle for at least this long), `due:` (ndb only: `true` gives only the nacked entries whose retry time passed, `false` only the ones that still wait; `nil` gives all).
- `"retry_at"` (ndb only) is the retry time of a nacked entry, as a `Time` (UTC), or `nil` when the entry is not nacked. A nacked entry has `"consumer" => ""`. The other fields are as the daemon sends them (`"idle_ms"` in milliseconds).

### `group.nack`

```ruby
billing.nack("w1", entry.id, delay: 60)        # => 1
billing.nack("w1", ids, delay: 0.5)            # an Array, a Float is fine
billing.nack("w1", entry.id)                   # delay 0: due at once
```

- ndb only: Redis has no delayed retry. `nack(consumer, *ids, delay: 0)` gives the entries back to the group after `delay` seconds, for a retry. It returns how many it gave back.
- Only the IDs pending **for this consumer** count; other IDs count 0, not an error. No ID sends nothing and returns 0.
- The entry stays pending with no owner (`"consumer" => ""`) and a `"retry_at"`. Its delivery count does not change; the claim that takes it again adds 1.
- No claim takes it before its retry time (also not `min_idle: 0` or `force: true`). After it, any consumer of the group takes it at once with [`group.claim_due`](#group-claim_due) or `group.claim`. The own pending read (`read(pending: true)`) never gives it, so a restarted worker does not take it early.
- `delay` is at most 30 days; a negative or larger one raises `Ndb::InvalidQuery` (`invalid_request`).
- `group.ack(id)` of a nacked entry removes it: so an app can cancel a retry.

### `group.claim_due`

```ruby
cursor = "0-0"

loop do
  out = billing.claim_due("w2", cursor: cursor, count: 100)
  out.entries.each { |e| handle(e) }

  cursor = out.cursor

  break if cursor == "0-0"
end
```

- ndb only. `claim_due(consumer, cursor: "0-0", count: nil, just_ids: false)` scans only the nacked entries and moves the ones whose retry time passed to the consumer, in ID order. Each one gets one more delivery.
- It does not read the entries in flight at live workers, so a worker can call it on a short tick (the worker does, every `due_every`).
- It returns an `Ndb::Claimed`, as [`group.auto_claim`](#group-auto_claim): `entries`, `deleted` (nacked IDs whose entry was trimmed) and `cursor`.

### `group.claim`

```ruby
out = billing.claim("w2", "1731164400000-0", "1731164400000-1", min_idle: 60)
out.entries   # Ndb::Entry list, now pending for w2
out.deleted   # pending IDs whose entry is no longer in the stream

billing.claim("w2", ids, idle: 90, retry_count: 5, force: true)
billing.claim("w2", ids, min_idle: 60, just_ids: true).entries   # => ["1731164400000-0", ...]
```

- `claim(consumer, *ids, min_idle: 0, idle: nil, retry_count: nil, force: false, just_ids: false)` moves the entries idle for at least `min_idle` seconds to the consumer (`XCLAIM`). Each one gets one more delivery.
- `idle:` (seconds) sets the idle time of the entries (`IDLE`). `retry_count:` sets their delivery count (`RETRYCOUNT`).
- `force: true` also takes an ID that is not pending (`FORCE`).
- `just_ids: true` gives the IDs (Strings) in `entries`, not `Ndb::Entry` values, and the delivery count does not change (`JUSTID`).
- It returns an `Ndb::Claimed` with `entries`, `deleted` and `cursor` (nil for `claim`).

### `group.auto_claim`

```ruby
cursor = "0-0"

loop do
  out = billing.auto_claim("w2", min_idle: 60, cursor: cursor, count: 100)
  out.entries.each { |e| handle(e) }

  cursor = out.cursor

  break if cursor == "0-0"
end
```

- Scans the pending list from `cursor:` (default `"0-0"`) and moves up to `count:` entries idle for at least `min_idle` seconds to the consumer (`XAUTOCLAIM`).
- `out.cursor` is where the next call starts. It is `"0-0"` when the scan is complete.
- `just_ids: true` gives the IDs (Strings) in `out.entries`, and the delivery count does not change, as in [`group.claim`](#group-claim).

### `group.worker`

```ruby
worker = billing.worker(
  "w1",
  count: 100,
  block: 5,
  claim_idle: 60,
  max_deliveries: 5,
  dead_letter: "orders_dead",
  on_error: ->(error) { warn error.message }
) { |entry| charge(entry.doc) }

trap("TERM") { worker.stop }

worker.run
```

```ruby
billing.worker("w1") do |entry, deliveries|
  logger.warn("retry #{deliveries} of #{entry.id}") if deliveries > 1

  charge(entry.doc)
end
```

- `group.worker(consumer, **options) { |entry| ... }` returns an `Ndb::Worker`. The block is required (`ArgumentError` without it).
- A block with two parameters also gets the delivery count of the entry: 1 on the first delivery. For the worker's own pending entries (after a restart), the count costs one more request for each read; a block with one parameter does not send it.
- A block that returns acks the entry. A block that raises leaves it pending; `on_error` gets the error, and a worker of the group claims the entry again after `claim_idle`. Any exception is a failure, also one that is not a `StandardError` (`NotImplementedError`, `SystemStackError`).
- A `SignalException` (with `Interrupt`) or `SystemExit` in the block goes on to the caller of `run`. The entries of that read that the block did before it are acked first.
- A deleted entry is acked without the block.
- The acks of one read go in one call.
- Delivery is at least once: make the work safe to repeat. Run many workers with different consumer names to share the work.

The options and their defaults:

| Option | Default | Meaning |
|---|---|---|
| `count:` | `10` | the most entries per read |
| `block:` | `5` | seconds a read waits for new entries |
| `claim_idle:` | `60` | seconds; entries of any consumer pending this long are claimed and tried again. `nil` or a value that is not positive turns the claim off |
| `claim_every:` | `claim_idle / 2`, at least `1` | seconds between two claim scans |
| `max_deliveries:` | `nil` (no limit) | an entry delivered this many times is poison |
| `dead_letter:` | `nil` | the stream name that gets the poison entries |
| `on_error:` | `nil` | a callable that gets the errors that do not stop the worker |
| `retry:` | `nil` | ndb only. The wait before the next try of a failed entry: an Array of seconds (`[15, 60, 600]`, the last one repeats) or a callable `->(deliveries) { seconds }`. See [Delayed retry](#delayed-retry). `nil`: a failed entry stays pending until `claim_idle` |
| `max_age:` | `nil` | ndb only. Seconds. An entry older than this (by the time of its ID) goes to `dead_letter` with the reason `"max_age"` and is acked, with no retry |
| `due_every:` | `1` with `retry:`, else off | seconds between two claims of the due retries (`group.claim_due`) |

### `Worker#run`

```ruby
worker.run   # blocks until worker.stop
```

The loop:

1. It reads its own pending entries first (the ones it took before a restart and did not ack), and does the work on them. With `max_deliveries`, an entry whose delivery count (with this read) is above `max_deliveries` goes to `dead_letter` without the block: the process stopped in the block that many times (a crash). Its dead letter entry has `deliveries` = the count - 1.
2. It reads new entries, and waits up to `block` for them.
3. It acks the entries that the block did without a raise, and nacks the failed ones with the wait of `retry:`.
4. Every `due_every`, it claims the nacked entries whose wait passed (`claim_due`), and does the work on them.
5. Every `claim_every`, it claims the entries idle for `claim_idle` (a scan: the nacked entries that still wait do not fill it), and sends the poison ones to `dead_letter`.

- A failed read goes to `on_error`. The loop waits (backoff from 0.1 s up to 5 s) and reads again. This is also true for the first read of the own pending entries (the daemon is down at the start).
- A group or a stream that does not exist is a failed read too: `on_error` gets `Ndb::UnknownGroup` (or `Ndb::UnknownCollection`), and the worker reads again after the backoff. So a worker that starts before its group (deploy order) waits for it, and starts the work when the group is made. Only `stop` ends the worker.
- `run` returns nil after `stop`.

### `Worker#start` and `Worker#stop`

```ruby
thread = worker.start   # runs the loop in a new Thread
worker.stop             # ends the loop
thread.join
worker.stopped?         # => true
```

- `stop` ends the loop after the current read (at most `block` seconds) and the work on the entries of that read. The entries of that read that were not done stay pending.

### `group.work`

```ruby
done = false

billing.work("w1", stop: -> { done }, count: 100, max_deliveries: 5) { |entry| charge(entry.doc) }
```

- The same loop as `worker(...).run` in one call, in this thread. It takes the options of `worker`.
- `stop:` is a callable. The loop ends when it returns true (it is checked about every 50 ms, and the loop ends after the current read). Without `stop:`, the loop never ends: use `worker` and `Worker#stop` instead.

### Delayed retry

ndb only. With `retry:`, a failed entry is not left pending for `claim_idle`: the worker nacks it with the wait for its delivery count, and the entry comes back after that wait, not before. While it waits it has no owner, so a restart does not take it early, and any worker of the group takes it when it is due.

The schedule of the fsw-esl outbox: try 1 waits 15 s, tries 2 to 5 wait 1 min, 6 to 11 wait 10 min, 12 to 23 wait 1 h, then 6 h, until the entry is 15 days old:

```ruby
billing.work(
  "w1",
  retry: Ndb::Stream.tiers(
    { wait: 15, up_to: 1 },      # try 1: 15 s
    { wait: 60, up_to: 5 },      # tries 2 to 5: 1 min
    { wait: 600, up_to: 11 },    # tries 6 to 11: 10 min
    { wait: 3600, up_to: 23 },   # tries 12 to 23: 1 h
    { wait: 21_600 }             # then 6 h
  ),
  max_age: 15 * 86_400,
  dead_letter: "postcall_jobs_dead",
  on_error: ->(error) { warn error.message }
) { |entry| post_call(entry.doc) }
```

- The wait after the failure on delivery n is the wait of the first step with `n <= up_to`; after the last `up_to`, the last wait repeats. With this schedule, an entry that always fails runs 81 times, and the 82nd delivery is past 15 days: it goes to the dead letter with reason `"max_age"`.
- A plain Array is one wait for each try: `retry: [15, 60, 600, 3600, 21_600]` waits 15 s after try 1, 1 min after try 2, and 6 h after each try from the fifth (the last wait repeats).
- An entry that fails on its `max_deliveries`-th delivery goes to the dead letter, it is not nacked.
- With `max_age:`, an entry older than `max_age` goes to the dead letter (reason `"max_age"`) without the block. An entry that fails when it is older than `max_age` goes there at once too, it is not nacked.
- A failed nack (ndb is down) leaves the entry pending: `claim_idle` takes it.
- A block that raises after `worker.stop` is not a failure: the entry stays pending for the next run, with no nack.
- The precision is about `due_every` (1 s).

### `Ndb::Stream.tiers`

```ruby
Ndb::Stream.tiers(15, 60, 600, 3600, 21_600)   # what retry: [15, 60, ...] makes

Ndb::Stream.tiers(                             # steps: a wait for a range of tries
  { wait: 15, up_to: 1 },                      # try 1
  { wait: 60, up_to: 5 },                      # tries 2 to 5
  { wait: 600, up_to: 11 },                    # tries 6 to 11
  { wait: 3600, up_to: 23 },                   # tries 12 to 23
  { wait: 21_600 }                             # then 6 h
)
```

- It returns an `Ndb::Stream::Tiers`: a callable (`tiers.call(deliveries)` gives the wait in seconds) for `retry:`.
- Waits are seconds (a Float is fine), from 0 to 30 days. Each `up_to:` is greater than the one before; only the last step can have no `up_to:`. After the last bound, the last wait repeats.
- `tiers.to_s` is the schedule in words: `"15s, 1m, 10m, 1h, then 6h"` or `"15s x1, 1m x4, 10m x6, 1h x12, then 6h"`.
- A bad wait or step raises `ArgumentError`.

### Dead letter and poison

```ruby
dead = db.stream("orders_dead")
dead.range.each do |e|
  e.doc   # => {"stream" => "orders", "group" => "billing", "id" => "...", "deliveries" => 5,
          #     "reason" => "max_deliveries", "doc" => {...}}
end
```

- An entry that fails on its `max_deliveries`-th delivery is poison. At that failure, the worker appends `{stream, group, id, deliveries, reason, doc}` to the `dead_letter` stream (when set), acks the entry, and gives an `Ndb::PoisonMessage` to `on_error`.
- `reason` is `"max_deliveries"`, or `"max_age"` for an entry older than `max_age:`.
- `Ndb::PoisonMessage` has `entry`, `deliveries`, `reason` and the code `poison_message`. The worker does not raise it.
- Without `dead_letter:`, a poison entry is only acked and reported.

### Stream errors

All are under `Ndb::StreamError`, a subclass of `Ndb::Error`:

| Class | Code | When |
|---|---|---|
| `NotAStream` | `not_a_stream` | a stream call, or `create`, on a collection that is not a stream |
| `InvalidStreamID` | `invalid_stream_id` | an ID that is not `<ms>-<seq>`, a special value where it is not allowed, or `0-0` in an append |
| `StreamIDTooSmall` | `stream_id_too_small` | an append ID that is not greater than the last one |
| `UnknownGroup` | `unknown_group` | the group does not exist |
| `GroupExists` | `group_exists` | `group.create` with a name that exists |

```ruby
begin
  orders.append({ order: 1 }, id: "1-0")
rescue Ndb::StreamIDTooSmall => e
  e.code   # "stream_id_too_small"
end
```

## Redis Streams commands

`Ndb::Client` has the stream commands of the [redis-rb](https://github.com/redis/redis-rb) gem (the module `Ndb::RedisCommands`), with the arguments and the answers of redis-rb 5. An app that uses Redis Streams picks its backend in one place, its factory, and keeps its calls:

```ruby
# config/initializers/streams.rb: the factory of the app
STREAMS = ENV["STREAMS_BACKEND"] == "ndb" ? Ndb.connect : Redis.new

STREAMS.xadd("orders", { order_id: 42 }, maxlen: 1000)                      # "1731164400000-0"
STREAMS.xgroup(:create, "orders", "billing", "$", mkstream: true)           # "OK"
STREAMS.xreadgroup("billing", "w1", "orders", ">", count: 10, block: 5000)  # { "orders" => [[id, { "order_id" => "42" }]] }
STREAMS.xack("orders", "billing", id)                                       # 1
```

- The methods are on the client that `Ndb.connect` returns. There is no new class and no new dependency: the gem does not load `redis`.
- Times are in milliseconds, as in redis-rb (`block:`, `min_idle_time`, `idle:`). The other stream methods of this gem use seconds.
- An entry in an answer is `[id, { "field" => "value" }]`. Every value is a String: see [Differences from Redis](#differences-from-redis).
- See [Move from Redis Streams](../streams.md#move-from-redis-streams) for the same change in Go and TypeScript.

### `xadd`

```ruby
xadd(key, entry, approximate: nil, maxlen: nil, minid: nil, nomkstream: nil, id: "*")

STREAMS.xadd("orders", { order_id: 42 })                               # => "1731164400000-0"
STREAMS.xadd("orders", { order_id: 43 }, maxlen: 1000, approximate: true)
STREAMS.xadd("orders", { order_id: 44 }, id: "1731164400001-0")
```

- Returns the ID of the entry (a String).
- `maxlen:` or `minid:` trims in the same call (`approximate: true` is `~`). The two together raise `ArgumentError`.
- `nomkstream: true` does not make the stream: the answer is `nil` when the stream does not exist.

### `xlen`

```ruby
STREAMS.xlen("orders")   # => 3; 0 for a stream that does not exist
```

### `xrange`

```ruby
xrange(key, start = "-", range_end = "+", count: nil)

STREAMS.xrange("orders", "-", "+", count: 100)
# => [["1731164400000-0", { "order_id" => "42" }], ...]

STREAMS.xrange("orders", "(1731164400000-0", "+")   # "(": without this ID
```

- The entries in ID order. A stream that does not exist gives `[]`.

### `xrevrange`

```ruby
xrevrange(key, range_end = "+", start = "-", count: nil)

STREAMS.xrevrange("orders", "+", "-", count: 1)   # the newest entry
```

- As `xrange`, from the highest ID down. The end comes first, as in Redis.

### `xread`

```ruby
xread(keys, ids, count: nil, block: nil)

STREAMS.xread("orders", "0", count: 100)
# => { "orders" => [[id, { "order_id" => "42" }], ...] }

STREAMS.xread("orders", "$", block: 5000)          # wait up to 5 s for a new entry
STREAMS.xread(%w[orders refunds], %w[0 0])         # one request for each stream
```

- `keys` and `ids` are one value or an Array of each. A different number of keys and IDs raises `ArgumentError`.
- The answer has only the streams that have entries. With none, it is `{}`.
- `block:` is in milliseconds, for one key only. `block: 0` waits with no limit.
- Without `block:`, the ID `"$"` always gives no entry, as in Redis.

### `xdel`

```ruby
STREAMS.xdel("orders", "1731164400000-0", "1731164400000-1")   # => 2
```

- `xdel(key, *ids)` returns how many entries it deleted. It sends one request for each ID. A stream that does not exist gives 0.

### `xtrim`

```ruby
xtrim(key, len_or_id, strategy: "MAXLEN", approximate: false, limit: nil)

STREAMS.xtrim("orders", 1000)                                   # keep the newest 1 000
STREAMS.xtrim("orders", "1731164400000-0", strategy: "MINID")   # delete the entries before this ID
```

- Returns how many entries it deleted; 0 for a stream that does not exist.
- `approximate: true` deletes only whole blocks of 1 024 entries; `limit:` is the most entries to delete with it.

### `xgroup`

```ruby
xgroup(subcommand, key, group, id_or_consumer = nil, mkstream: false)

STREAMS.xgroup(:create, "orders", "billing", "$", mkstream: true)   # => "OK"
STREAMS.xgroup(:setid, "orders", "billing", "0")                    # => "OK"
STREAMS.xgroup(:createconsumer, "orders", "billing", "w1")          # => 1, or 0 when it exists
STREAMS.xgroup(:delconsumer, "orders", "billing", "w1")             # => its pending count
STREAMS.xgroup(:destroy, "orders", "billing")                       # => 1, or 0 when it is not there
```

- `create` without `mkstream: true` needs a stream that exists. A group that exists raises `Ndb::GroupExists` (`BUSYGROUP`).

### `xreadgroup`

```ruby
xreadgroup(group, consumer, keys, ids, count: nil, block: nil, noack: nil)

STREAMS.xreadgroup("billing", "w1", "orders", ">", count: 10, block: 5000)
# => { "orders" => [[id, { "order_id" => "42" }]] }, or {} when there is no new entry

STREAMS.xreadgroup("billing", "w1", "orders", "0")
# => { "orders" => [...] }: the pending entries of w1, also when there are none ("orders" => [])
```

- The ID `">"` gives new entries. Any other ID gives the pending entries of the consumer after that ID. A pending entry that was deleted from the stream comes as `[id, nil]`.
- `block:` is in milliseconds (0: no limit), for one key only. A pending read does not use it.
- `noack: true` does not add the entries to the pending list.

### `xack`

```ruby
STREAMS.xack("orders", "billing", id1, id2)   # => 2
```

- `xack(key, group, *ids)` returns how many entries were pending. A group or a stream that does not exist gives 0.

### `xpending`

```ruby
STREAMS.xpending("orders", "billing")
# => { "size" => 2, "min_entry_id" => "...", "max_entry_id" => "...", "consumers" => { "w1" => "2" } }

STREAMS.xpending("orders", "billing", "-", "+", 10)
STREAMS.xpending("orders", "billing", "-", "+", 10, "w1", idle: 60_000)
# => [{ "entry_id" => "...", "consumer" => "w1", "elapsed" => 61000, "count" => 2 }]
```

- `xpending(key, group)` is the summary. With no pending entry, the two IDs are `nil` and `"consumers"` is `{}`.
- `xpending(key, group, start, end, count, consumer = nil, idle: nil)` is one Hash for each entry. `"elapsed"` is the idle time in milliseconds and `"count"` is the delivery count. `idle:` keeps only the entries idle for at least this long.
- Any other number of arguments raises `ArgumentError`.

### `xclaim`

```ruby
xclaim(key, group, consumer, min_idle_time, *ids, **opts)

STREAMS.xclaim("orders", "billing", "w2", 60_000, id1, id2)                 # => [[id, fields], ...]
STREAMS.xclaim("orders", "billing", "w2", 60_000, id1, justid: true)        # => [id]
STREAMS.xclaim("orders", "billing", "w2", 0, id1, idle: 0, retrycount: 1, force: true)
```

- Moves the entries idle for at least `min_idle_time` to the consumer.
- Options: `idle:` (ms), `retrycount:`, `force:`, `justid:`. `time:` raises `Ndb::NotSupported`. Any other option raises `ArgumentError`.

### `xautoclaim`

```ruby
xautoclaim(key, group, consumer, min_idle_time, start, count: nil, justid: false)

STREAMS.xautoclaim("orders", "billing", "w2", 60_000, "0-0", count: 100)
# => { "next" => "0-0", "entries" => [[id, fields], ...] }
```

- `"next"` is the start of the next call. It is `"0-0"` when the scan is complete.
- With `justid: true`, `"entries"` are IDs.
- The pending IDs whose entry is gone leave the pending list. They are not in the answer.

### `xinfo`

```ruby
STREAMS.xinfo(:stream, "orders")
# => { "length", "last-generated-id", "max-deleted-entry-id", "entries-added",
#      "recorded-first-entry-id", "groups", "first-entry", "last-entry" }

STREAMS.xinfo(:groups, "orders")
# => [{ "name", "consumers", "pending", "last-delivered-id", "entries-read", "lag" }, ...]

STREAMS.xinfo(:consumers, "orders", "billing")
# => [{ "name", "pending", "idle", "inactive" }, ...]
```

- `xinfo(subcommand, key, group = nil)`. The keys have the names of Redis.
- In `xinfo(:stream)`, `"first-entry"` and `"last-entry"` are `[id, [field, value, ...]]`, or `nil`. An ID that the stream does not have yet is `"0-0"`.

### Differences from Redis

- **Values.** Redis keeps strings. ndb keeps the JSON value that `xadd` got: a number stays a number and a nested Hash stays an object, so a query or an index can use it. A read gives every value as a String, as Redis does: a string as it is, a number or a boolean with `to_s` (`42` is `"42"`), `nil` as `""`, and a nested value as its JSON text (`{ city: "Recife" }` is `'{"city":"Recife"}'`). redis-rb refuses a nested value in `xadd`; ndb takes it.
- **Field names.** They become Strings. A name that starts with `_` is reserved (`_id`, `_ts`). The fields of an entry come in name order, not in the order of the `xadd`.
- **Stream names.** A stream is an ndb collection, so its name follows the rules of a collection name. `orders:eu` is not a valid name.
- **Many streams.** ndb reads one stream for each request. `xread` and `xreadgroup` with many keys and no `block:` send one request for each key, in order. With `block:` and more than one key they raise `Ndb::NotSupported`.
- **No `count:`.** Redis gives every entry. `xread` and `xreadgroup` without `count:` give at most 10 000 entries for each stream (the most of one ndb read). `xrange` and `xrevrange` without `count:` give every entry.
- **`block: 0`** waits with no limit, as in Redis: it is a loop of reads of 30 seconds each. For `xread`, the ID `"$"` becomes the last ID of the stream before the first read, so an entry appended between two reads of the loop is not lost. A blocking `xread` on a stream that does not exist looks for the stream every 0.5 seconds until the time ends.
- **Errors** are `Ndb::Error` subclasses, not `Redis::CommandError`: rescue both in code that runs on the two backends. The message starts as in Redis, and the daemon message follows in parentheses.

  | Case | Class | The message starts with |
  |---|---|---|
  | `xgroup(:create)` on a group that exists | `Ndb::GroupExists` | `BUSYGROUP` |
  | A group command on a group or a stream that does not exist | `Ndb::UnknownGroup`, `Ndb::UnknownCollection` | `NOGROUP` |
  | `xgroup(:create)` without `mkstream:` and no stream | `Ndb::UnknownCollection` | `ERR The XGROUP subcommand requires the key to exist` |
  | `xinfo(:stream)` or `xinfo(:groups)` with no stream | `Ndb::UnknownCollection` | `ERR no such key` |

- **`xclaim`** has no `time:` option (use `idle:`).
- **`xinfo(:stream)`** has no `radix-tree-keys` and `radix-tree-nodes`, and there is no `FULL` form.
- **A trim or a delete keeps the pending references**, as up to Redis 8.0. `xautoclaim` and `xclaim` drop the pending IDs whose entry is gone.
- **Only these commands.** They are methods of `Ndb::Client`. There is no `multi`, no `pipelined` and no other Redis command.

## Admin

```ruby
db.health           # => true (GET /_health); raises Ndb::NotReady while recovering
db.collections      # the catalog (GET /_collections)
db.stats            # every collection and totals (GET /_stats)
db.size             # disk use (GET /_size)

calls.stats              # GET /calls/_stats
calls.compact            # POST /calls/_compact
calls.ensure_index("dnis")   # => ["dnis", "status"]
calls.drop_index("dnis")
```

## Blobs

Large values live in the blob store. A document keeps only a reference.

```ruby
stored = db.put_blob(File.open("call.wav"), "audio/wav")   # an IO, streamed in chunks
stored   # => {"$blob" => "sha256:...", "size" => 123456, "type" => "audio/wav"}

calls.insert(_id: "c9", recording: stored)

db.get_blob(stored["$blob"])                    # all the bytes
db.get_blob(stored["$blob"], from: 0, to: 1023) # bytes 0..1023 (inclusive)
```

The same content always gives the same reference. An IO upload is never retried.

## The low layer: Ndb::Client

`Ndb.connect` returns an `Ndb::Client`. Its methods map one to one to the [HTTP routes](../http.md), with the collection name first. Bodies are sent as they are (only a Regexp is converted), except the `query` of `update_by`, `delete_by` and `watch`, which takes the Ruby forms.

| Method | Route |
|---|---|
| `search(coll, body)` → `Ndb::Page` | `POST /{coll}/_search` |
| `count(coll, query = nil)` | `POST /{coll}/_count` |
| `aggregate(coll, body)` → `Ndb::Page` | `POST /{coll}/_aggregate` |
| `get(coll, id)` | `GET /{coll}/_doc/{id}` |
| `insert(coll, doc)` | `POST /{coll}/_doc` |
| `bulk(coll, docs, expire_in:, upsert:)` | `POST /{coll}/_bulk` |
| `update(coll, id, ops, guard:, upsert:, expire_in:)` | `POST /{coll}/_update/{id}` |
| `update_by(coll, query, ops, many:, upsert:, expire_in:)` | `POST /{coll}/_update_by_query` |
| `delete(coll, id)` | `DELETE /{coll}/_doc/{id}` |
| `delete_by(coll, query, many:)` | `POST /{coll}/_delete_by_query` |
| `expire(coll, id, in:/at:)`, `persist(coll, id)`, `ttl(coll, id)` | `_expire`, `_persist`, `_ttl` |
| `watch(coll, consumer, query:, from:, limit:)`, `ack(coll, consumer, pos)` | `_watch`, `_ack` |
| `ensure_collection(coll, definition)`, `truncate(coll)`, `drop(coll)` | `PUT /{coll}`, `_truncate`, `DELETE /{coll}` |
| `rename(coll, to, replace:)`, `restore(coll, at:, to:)`, `pitr(coll)` | `_rename`, `_restore`, `_pitr` |
| `stream(name)` → `Ndb::Stream`, `stream_request(method, coll, action, body)` | the [stream routes](../http/streams.md); see [Streams](#streams) |
| `ensure_index(coll, f)`, `drop_index(coll, f)`, `collection_stats(coll)`, `compact(coll)` | `_indexes/{f}`, `_stats`, `_compact` |
| `health`, `collections`, `stats`, `size`, `put_blob`, `get_blob` | system routes |

```ruby
page = db.search("calls", query: { status: "answered" }, sort: { at: -1 }, limit: 10, count: true)
page.rows; page.next; page.total
```

## Errors

All errors inherit from `Ndb::Error`, which has `code` (the daemon code) and `status` (the HTTP status). The codes are on [Errors](../http/errors.md).

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
| `InvalidQuery` | `invalid_query`, `invalid_request`, `invalid_pipeline` |
| `InvalidCursor` | `cursor_invalid`, `cursor_mismatch`, `cursor_expired` |
| `WatchLagged` | `watch_lagged` |
| `HistoryTruncated` | `history_truncated` |
| `TtlNotEnabled`, `TtlDerived` | `ttl_not_enabled`, `ttl_derived` |
| `ConfirmRequired` | `confirm_required` |
| `CollectionExists` | `collection_exists` |
| `OutOfWindow`, `PitrOff` | `out_of_window`, `pitr_off` |
| `LookupError` | `lookup_invalid`, `lookup_unindexed`, `lookup_too_large` |
| `Forbidden` | `forbidden` |
| `InvalidDocument` | `invalid_doc`, `missing_id`, `invalid_id`, `immutable_id`, `invalid_batch`, `invalid_path`, `invalid_index` |
| `InvalidPattern` | `invalid_pattern`, `pattern_read_only`, `pattern_too_wide` |
| `NotSupported` | `not_supported` |
| `InvalidBlobRef` | `invalid_blob_ref` |
| `UnknownRoute` | `unknown_route`, `method_not_allowed` |
| `GroupLimitExceeded` | `group_limit_exceeded` |
| `RequestTooLarge` | `request_too_large`, `line_too_large` |
| `InvalidPosition` | `invalid_position` |
| `Closed` | `closed`, `stopping` (not retried: the daemon may have taken the request) |
| `Internal` | `internal` |
| `StreamError` and its subclasses | `not_a_stream`, `invalid_stream_id`, `stream_id_too_small`, `unknown_group`, `group_exists`; see [Stream errors](#stream-errors) |
| `ConnectionError` | cannot reach the daemon. Subclasses: `ConnectError` (nothing was sent), `TimeoutError` |

Any other code is a plain `Ndb::Error`. A response that is not a daemon error has the code `http_status`.

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

`-tcp ''` turns off the default TCP listener (`127.0.0.1:7070`), so the test daemon does not take the port of your local daemon.

```sh
ndb serve -config /nonexistent.yml -data-dir "$(mktemp -d)" -unix /tmp/ndb-test.sock -tcp ''
```

```ruby
db = Ndb.connect("unix:///tmp/ndb-test.sock")
```

Use a new collection name per test. Run the suite with `bundle exec rake test` in `sdk/ruby`.
