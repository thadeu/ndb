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

- The address is, in order: the argument, env `NDB_URL`, then the default socket `/var/run/ndb/ndb.sock`.
- The token is `token:`, or env `NDB_TOKEN`. It goes only in the `Authorization` header. A unix socket does not need it.
- A bad address raises `ArgumentError` at once.
- `ready_timeout` is how long a call retries while the daemon is not ready (`not_ready`, or the socket is missing or refuses). It uses exponential backoff (50 ms up to 1 s) with jitter. A write is retried only when the daemon certainly did not process it. A read is also retried after a dropped connection. A timeout is never retried. When the deadline passes, the last error is raised.
- A reused keep-alive connection that the server closed is sent again only for a read. A write is never sent twice.
- The pool is thread-safe and drops its connections after a `fork`.

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
- A direct-mode collection raises `Ndb::ReadOnlyCollection`. A collection declared in `ndb.yml` comes back, empty, when the daemon restarts after a drop. After `drop`, the next call on the handle declares it again.

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

A collection name with `*` at the end reads every collection whose name starts with the prefix (see [Collection patterns](../http/queries.md#collection-patterns)). Each row has `"_coll"`, the name of its collection. Writes raise `Ndb::Error` with code `pattern_read_only`.

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
| `bulk(coll, docs, expire_in:)` | `POST /{coll}/_bulk` |
| `update(coll, id, ops, guard:, upsert:, expire_in:)` | `POST /{coll}/_update/{id}` |
| `update_by(coll, query, ops, many:, upsert:, expire_in:)` | `POST /{coll}/_update_by_query` |
| `delete(coll, id)` | `DELETE /{coll}/_doc/{id}` |
| `delete_by(coll, query, many:)` | `POST /{coll}/_delete_by_query` |
| `expire(coll, id, in:/at:)`, `persist(coll, id)`, `ttl(coll, id)` | `_expire`, `_persist`, `_ttl` |
| `watch(coll, consumer, query:, from:, limit:)`, `ack(coll, consumer, pos)` | `_watch`, `_ack` |
| `ensure_collection(coll, definition)`, `truncate(coll)`, `drop(coll)` | `PUT /{coll}`, `_truncate`, `DELETE /{coll}` |
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
| `InvalidQuery` | `invalid_query`, `invalid_request` |
| `InvalidCursor` | `cursor_invalid`, `cursor_mismatch`, `cursor_expired` |
| `WatchLagged` | `watch_lagged` |
| `HistoryTruncated` | `history_truncated` |
| `TtlNotEnabled`, `TtlDerived` | `ttl_not_enabled`, `ttl_derived` |
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

```sh
ndb -config /nonexistent.yml -data-dir "$(mktemp -d)" -unix /tmp/ndb-test.sock
```

```ruby
db = Ndb.connect("unix:///tmp/ndb-test.sock")
```

Use a new collection name per test. Run the suite with `bundle exec rake test` in `sdk/ruby`.
