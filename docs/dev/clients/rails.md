# Rails

The gem `ndb-rails`: an ActiveModel layer with `Ndb::Record`. It depends on the gem `ndb` and uses `Ndb::Client` inside.

The source and the same guide are in [`sdk/rails`](https://github.com/thadeu/ndb/tree/main/sdk/rails).

## Install

Needs Ruby 3.2 or newer, `activemodel` and `activesupport` 7.1 or newer, and a running ndb daemon. The gem `ndb` (`~> 0.1`) is installed with it, as a dependency.

```ruby
# Gemfile
gem "ndb-rails"
```

```sh
bundle add ndb-rails
# or: gem install ndb-rails
```

In Rails, the gem loads a railtie. It reads `config/ndb.yml` and, when the app eager loads (production), it declares the collection of every model at boot. In development a model declares its collection on first use. Without Bundler autorequire, use `require "ndb/rails"`. Without Rails, call `Ndb::Rails.configure` yourself (below).

## Connect

Rails: create `config/ndb.yml`. It has one block per environment. ERB and YAML anchors work.

```yaml
default: &default
  timeout: 30          # seconds, per request
  pool_size: 16        # idle connections kept
  ready_timeout: 10    # seconds to retry while the daemon starts; 0 turns it off

development:
  <<: *default
  socket: /tmp/ndb.sock

production:
  <<: *default
  url: tcp://ndb.internal:7070
  token: <%= ENV["NDB_TOKEN"] %>
```

Keys: `url`, `socket`, `token`, `timeout`, `pool_size`, `ready_timeout`. Any other key is ignored.

Any app: configure in code, for example in `config/initializers/ndb.rb`.

```ruby
Ndb::Rails.configure do |c|
  c.socket = "/run/ndb/ndb.sock"            # or: c.url = "tcp://10.0.0.5:7070"
  c.token = ENV["NDB_TOKEN"]
  c.timeout = 30
  c.pool_size = 16
  c.ready_timeout = 10
end
```

- The address is, in order: `url`, `socket`, env `NDB_URL`, then the default socket `/var/run/ndb/ndb.sock`.
- The address is `unix:///path`, `tcp://host:port`, `https://host[:port]` (ndb behind a TLS proxy), or a bare path that starts with `/`.
- The token is `token`, or env `NDB_TOKEN`. It goes only in the `Authorization` header. A unix socket does not need it.
- `ready_timeout` is how long a call retries while the daemon is not ready (`not_ready`, or the socket is missing or refuses). It uses backoff with jitter. A write is retried only when the daemon certainly did not process it. A read is also retried after a dropped connection. A timeout is never retried.
- The pool drops its connections after a `fork`.
- `Ndb::Rails.client` is an `Ndb::Client` built from this configuration (`Ndb::Rails.configuration`). You can also build one: `Ndb::Client.new(socket: "/tmp/ndb.sock", token: nil, timeout: 30, pool_size: 16, ready_timeout: 10)`. The [`ndb` README](https://github.com/thadeu/ndb/blob/main/sdk/ruby/README.md) documents every client call.

## Define a model

```ruby
class Call < Ndb::Record
  attribute :status, :string, index: true
  attribute :caller, :string, unique: true
  attribute :duration, :integer
  attribute :at, :datetime, time: true
  attribute :expires_at, :datetime, expires: true
  attribute :recording, :blob

  validates :status, presence: true
end
```

| Option or method | Meaning |
|---|---|
| `_id` / `id` | The primary key is always `_id` (a string; the daemon makes a UUIDv7 when it is empty). `id` is an alias of `_id`. For a natural key (email, SKU) keep `_id` and use `unique: true` on that attribute. |
| `index: true` | Index this field. |
| `unique: true` | Unique value. A second one raises `RecordNotUnique`. |
| `time: true` | The collection time field. Used by zone maps, `$dateTrunc` and TTL. |
| `expires: true` | The field holds the time the document expires. |
| `ttl 24.hours` | Documents expire this long after the time field. Use `ttl` or `expires:`, not both: the daemon returns `catalog_invalid`. |
| `self.collection_name = "x"` | Collection name. Default follows Rails naming (`Call` becomes `calls`). |
| `source "glob"` | Declares a read-only direct-mode collection over files. Writes raise `ReadOnlyCollection`. |

- Attributes are ActiveModel attributes, so every ActiveModel type works (`:string`, `:integer`, `:float`, `:boolean`, `:datetime`, `:date`, and so on). Times are sent as RFC 3339 UTC strings with milliseconds. `:blob` is added by this gem.
- Validations, callbacks (`save`, `create`, `update`, `destroy`), dirty tracking and `as_json` work as in ActiveModel.
- The model declares its collection on first use, once per process.
- Mark a base class with `self.abstract_class = true` to share settings.

## Insert, find, update, delete

```ruby
call = Call.create!(_id: "c1", status: "answered", caller: "a", at: Time.now.utc)
Call.create!([{ _id: "c2", status: "failed", caller: "b" }, { _id: "c3", status: "answered", caller: "c" }])

call = Call.new(_id: "c4", status: "answered", caller: "d")
call.save          # false when invalid
call.save!         # raises Ndb::RecordInvalid

call = Call.find("c1")        # raises Ndb::NotFound
Call.find_by(status: "failed") # nil when missing
Call.find_by!(status: "x")     # raises NotFound

call.update!(status: "done")  # $set of the changed fields only, filtered by id and version
call.version                   # the document version

call.increment!(:duration)     # atomic $inc on the daemon, refreshes the attribute and the version
call.decrement!(:duration, 5)

call.destroy                   # callbacks, filtered by id and version
call.destroy!                  # raises Ndb::RecordNotDestroyed when a before_destroy callback aborts
call.delete                    # by id only: no callbacks, no version guard

Call.destroy("c1")             # find, destroy, return the record (an Array of ids returns an Array)
Call.delete(["c2", "c3"])      # one delete by id, no callbacks, returns the count

# Dynamic finders, for declared attributes and id. The ! variant raises NotFound.
Call.find_by_status("failed")
Call.find_by_status_and_caller("failed", "a")
Call.where(caller: "a").find_by_status!("failed")

Call.find_or_initialize_by(_id: "c9") { |c| c.status = "new" }   # unsaved when missing
Call.find_or_create_by(_id: "c9") { |c| c.status = "new" }       # atomic: concurrent callers get one document
Call.find_or_create_by!(_id: "c9", status: "new")                # raises RecordInvalid

# Atomic writes on every match of a where (it needs the id or an indexed field).
# update takes the operators of the client and returns the number of modified documents.
Call.where(status: "failed").update({ "$set" => { "status" => "retry" } })
Call.where(status: "retry").delete_all   # one delete, no callbacks, returns how many (delete is an alias)
Call.where(status: "retry").destroy_all  # loads and destroys each record with its callbacks, returns them
```

- Every load asks for the version (`_v`). `save`, `update` and `destroy` put the id and `_v` in the filter of the write. If another writer changed the document first, the filter matches nothing and you get `Ndb::StaleObjectError` (code `stale_object`).
- `find_or_create_by` is atomic. It checks validations and runs the `save` and `create` callbacks, then sends one upsert: the attributes you pass are the filter, and the other attributes go in `$setOnInsert`, so a writer that created the document first is never overwritten and you get that document. An invalid record is returned unsaved (the `!` form raises `Ndb::RecordInvalid`). Only the attributes you pass must be equality values.
- `delete_all` and `destroy_all` on a model without a `where` are refused by the daemon (`Ndb::EmptyFilter`); `delete_all` needs the id or an indexed field in the `where`. To empty the whole collection, use `Call.truncate!`.
- `Call.truncate!` removes every document, with no callbacks, and keeps the definition and indexes. `Call.drop!` removes the collection, its definition and its watch consumers; the next use of the model declares it again. Both take the same time for 10 or 10 million documents, and watch consumers get one `"truncate"` event.
- Setting an attribute to `nil` and saving removes the field from the document.
- A duplicate id raises `RecordNotUnique` (code `duplicate_id`).
- `Call.upsert(id: "c1", status: "done")` and `Call.upsert_all([...])` write by `_id` like ActiveRecord: they create the document, or merge the given attributes into the stored one (the others stay), in one transaction. No validations or callbacks run. See [Upsert](../http/documents.md#upsert).
- `persisted?`, `new_record?`, `destroyed?`, `id` and `version` work as in ActiveRecord.

```ruby
begin
  call.update!(status: "done")
rescue Ndb::StaleObjectError
  call = Call.find(call.id)
  retry
end
```

## Expiry (TTL like Redis)

Give the model an `expires: true` attribute. The daemon sets it for you.

```ruby
class Session < Ndb::Record
  attribute :user, :string
  attribute :expires_at, :datetime, expires: true
end

Session.create!({ _id: "s1", user: "u" }, expire_in: 1.hour)   # also create, save, update (seconds, a Duration or "30m")
session = Session.find("s1")

session.expire(30.minutes)   # EXPIRE: sets expires_at in the object, other unsaved changes stay
session.ttl                  # => 1799, -1 (never expires), or nil (missing or expired)
session.persist_ttl!         # PERSIST: removes the expiry
```

- `persist_ttl!` is not called `persist!`: ActiveModel already uses `persisted?` for something else.
- `expire_in:` on `create`, `save` and `update` goes in the same write. `expire_in` wins over the `expires_at` value. After a write with `expire_in`, load the record again (`Session.find`) to see `expires_at`.
- `expire` and `persist_ttl!` raise `Ndb::NotFound` for a missing or expired document, and `Ndb::TtlNotEnabled` when the model has no `expires: true` attribute.
- The class-level `ttl 24.hours` stays: every document expires that long after its time field. `expire` and `persist_ttl!` raise `Ndb::TtlDerived` for it.
- Disk space of expired documents returns after compaction, and fully only with `wal_retention`; see [Freeing disk](../configuration.md#freeing-disk).

## Query

Relations are lazy and immutable. The request goes out when you call `to_a`, `first`, `each`, `count` and so on.

```ruby
Call.where(status: "answered")
Call.where(status: %w[answered failed])      # $in
Call.where(duration: 30..60)                 # $gte and $lte
Call.where(duration: 30..)                   # $gte
Call.where(duration: nil)                    # $exists: false
Call.where(at: 1.day.ago..)
Call.where("agent.id" => 7)                  # dot path
Call.where(duration: { "$gt" => 10 })        # any daemon operator, as a Hash
Call.where.not(status: "failed")             # $ne, $nin, $exists
Call.where(status: "failed").or(Call.where(duration: 60..))
Call.where("$or" => [{ status: "failed" }, { duration: { "$gt" => 50 } }])

Call.where(status: "answered")
    .order(at: :desc, _id: :asc)
    .limit(50)
    .offset(100)
    .to_a

Call.where(status: "answered").pluck(:_id)         # one field: an array of values
Call.where(status: "answered").pluck(:_id, :status) # several: an array of arrays
Call.where(status: "answered").first(5)
Call.where(status: "failed").exists?
```

- Chained `where` calls are ANDed. See the [query language](../query.md) for the full operator list.
- The order of the `order` keys is the order of the sort. The id is always the last key.
- `page(n).per(size)` sets offset paging. The default page size is 25.
- `pluck` asks the daemon for only those fields.
- There is no `explain` on a relation. Use the client: `Ndb::Rails.client.search(coll, "query" => {...}, "explain" => true)` returns a `Page` with `plan` (the `X-Ndb-Plan` header: `id`, `index:<field>`, `scan`, `snapshot`) and `examined`.

### Regular expressions

A Ruby `Regexp` works in `where`, `where.not` and `or`. The gem `ndb` turns it into `$regex` and `$options` (the daemon uses RE2). Ruby `^` and `$` match at each line, so `m` is always sent. `/i` is `i`, `/m` is `s`, `/x` is `x`. Look-around, backreferences, atomic groups, possessive quantifiers, `\h`, `\G`, `\R` and `\X` raise `Ndb::InvalidQuery` before the request.

```ruby
Call.where(caller: /^55/)
Call.where(caller: /^55/i).where.not(status: /fail/)
Call.where(caller: /^55/).or(Call.where(caller: /^11/))
Call.where(caller: { "$regex" => "^55" })   # a String: no `m`, and a literal ^prefix uses the index
```

A Regexp is a scan filter. For a prefix search that uses the index, pass the String form. See the [`ndb` README](https://github.com/thadeu/ndb/blob/main/sdk/ruby/README.md) for the full mapping.

### Queries built elsewhere

For a search form or a saved search, take the JSON as is. `where_raw` takes a where Hash and chains like `where`, without the Rails conventions: an Array stays an Array (equal to the whole array), where `where(tags: [...])` means `$in`. `search` takes a whole search body (the `query`, `sort`, `limit`, `after` and `count` of [POST /{coll}/_search](../http/queries.md#post-coll-search)).

```ruby
form = JSON.parse(params[:q])
Call.where_raw(form).where(dnis: "1000").order(ts: :desc).limit(50)

page = Call.search("query" => form, "sort" => { "ts" => -1 }, "limit" => 50, "count" => true)
page.records      # Call records, with their version
page.next_cursor  # for "after"; nil on the last page
page.total        # with "count" => true
```

Rows decode like any other query. The daemon validates the JSON (`invalid_query`), but it takes any field: for a public form, keep an allowlist of fields and operators, cap `limit`, and ask for an indexed field or a time window, as you would before sending a form to Elasticsearch `_search`.

### Collection patterns

A model whose `collection_name` is a [collection pattern](../query.md#collection-patterns) reads every matched collection. It declares nothing at boot, and its writes raise `Ndb::Error` with code `pattern_read_only`. `record.source_collection` is the collection a record came from.

```ruby
class LogArchive < Ndb::Record
  self.collection_name = "logs-2026-*"

  attribute :at, :datetime, time: true
  attribute :lvl, :string
end

LogArchive.where(lvl: "error").order(at: :desc).limit(50).map(&:source_collection)
LogArchive.where(lvl: "error").count
```

### Count

```ruby
Call.where(status: "answered").count
```

### Cursor pagination

```ruby
relation = Call.where(status: "answered").order(:_id).limit(100)
rows = relation.to_a
token = relation.next_cursor   # nil on the last page

Call.where(status: "answered").order(:_id).limit(100).after(token).to_a
```

A token works only with the same `where` and `order`. Another query shape raises `InvalidCursor`.

To walk every row, use `find_each`. It follows the cursor in batches (default 1000):

```ruby
Call.where(status: "answered").find_each(batch_size: 500) { |call| puts call._id }

Call.in_batches(of: 500) { |batch| batch.each { |call| ... } }
```

## Aggregate

Relations have simple group calculations. Each one is one aggregate request.

```ruby
Call.group(:status).count            # => {"answered" => 2, "failed" => 1}
Call.group(:status).sum(:duration)
Call.group(:status, :caller).count   # keys are arrays
Call.where(status: "answered").average(:duration)
Call.minimum(:duration)
Call.maximum(:duration)
Call.sum(:duration)
```

For a full pipeline, use the client. The pipeline needs exactly one `$group`. Accumulators: `$count`, `$sum`, `$avg`, `$min`, `$max`, `$first`, `$last`.

```ruby
page = Ndb::Rails.client.aggregate(Call.collection_name,
  pipeline: [
    { "$match" => { "duration" => { "$gte" => 1 } } },
    { "$group" => {
      "_id" => {
        "status" => "$status",
        "hour" => { "$dateTrunc" => { "date" => "$at", "unit" => "hour" } }
      },
      "n" => { "$count" => {} },
      "avg" => { "$avg" => "$duration" }
    } },
    { "$sort" => { "n" => -1 } }
  ],
  limit: 100,
  count: true)

page.rows         # array of Hash
page.total        # with count: true
page.next         # the cursor of the next page; nil on the last page
```

`$dateTrunc` units are `minute`, `hour`, `day`, `week` and `month`. Optional `"timezone"`: a name such as `"Europe/Rome"`.

## Watch

A watch streams the changes of a collection to a named consumer: first the events after its last ack, then new events as they happen. The watch API is on the client (`Ndb::Client`).

```ruby
client = Ndb::Rails.client

begin
  client.watch(Call.collection_name, "relay", query: { status: "answered" }) do |event|
    # event has "pos", "op" ("put", "del" or "truncate"), "_id", "_v" and "doc"
    handle(event)

    client.ack(Call.collection_name, "relay", event["pos"])
  end
rescue Ndb::WatchLagged
  retry # the consumer was too slow. watch continues after the last ack.
rescue Ndb::HistoryTruncated
  raise # the saved position is too old. Start over.
end
```

- `query:` takes the same conditions as `where` on a model (Hash and Range). Only matching events come. A delete event has no `"doc"`; it is sent only if this stream saw an earlier matching put of that id.
- `from: "now"` starts a new consumer at the end. `limit: n` ends the stream after n events.
- Without a block, `watch` returns an Enumerator.
- Delivery is at least once. After a restart an event can come again, so make the work safe to repeat.
- `HistoryTruncated` means the WAL files for the position were deleted (`wal_retention`).
- A watch holds one connection until it ends.

## Blobs

Large values live in the blob store. The document keeps only a reference (`$blob`, `size`, `type`).

```ruby
call = Call.new(_id: "c1", status: "answered", caller: "a",
                recording: File.open("call.wav"))   # an IO or a String
call.save!                                          # uploads the blob, then saves

call.recording.ref           # "sha256:..."
call.recording.size

loaded = Call.find("c1")
loaded.recording.read        # all the bytes
loaded.recording.each_chunk { |chunk| out.write(chunk) } # 1 MiB ranges
```

A blob behaves like a read-only IO (`read`, `each_chunk`, `eof?`, `rewind`). A loaded blob reads the daemon in 1 MiB ranges, so memory stays small. The same content always gives the same reference. You can also use `Ndb::Blob.new(io_or_string, content_type: "audio/wav")`.

## Errors

The transport and daemon errors are defined by the gem `ndb`. They all live in the `Ndb` module (`Ndb::Error`, `Ndb::NotFound`, ...). For an ActiveRecord-like `rescue`, this gem adds one alias: `Ndb::RecordNotUnique` is `Ndb::UniqueViolation` (the same class). `Ndb::StaleObjectError`, `Ndb::RecordInvalid` and `Ndb::RecordNotSaved` exist only here, and they inherit from `Ndb::Error`. Every error has `code` (the daemon code) and `status` (the HTTP status).

| Class | Code |
|---|---|
| `Unauthorized` | `unauthorized` |
| `NotReady` | `not_ready` |
| `NotFound` | `not_found` |
| `UnknownCollection` | `unknown_collection` |
| `StaleObjectError` | `stale_object` (raised by the gem, not sent by the daemon) |
| `InvalidUpdate` and its subclasses | `unknown_operator`, `no_update`, `conflicting_update`, `invalid_field`, `unindexed_many`, `empty_filter` |
| `RecordNotUnique` | `unique_violation`, `duplicate_id` |
| `CatalogConflict` | `catalog_conflict` |
| `CatalogInvalid` | `catalog_invalid` |
| `ReadOnlyCollection` | `direct_collection` |
| `InvalidQuery` | `invalid_query`, `invalid_request` |
| `InvalidCursor` | `cursor_invalid`, `cursor_mismatch`, `cursor_expired` |
| `WatchLagged` | `watch_lagged` |
| `HistoryTruncated` | `history_truncated` |
| `TtlNotEnabled`, `TtlDerived` | `ttl_not_enabled`, `ttl_derived` |
| `RecordInvalid` | ActiveModel validation failed (has `record`) |
| `RecordNotSaved` | `save!` returned false without errors |
| `ConnectionError` | cannot reach the daemon. Subclasses: `ConnectError` (nothing was sent), `TimeoutError` |

Any other code is a plain `Ndb::Error`. A response that is not a daemon error has the code `http_status`.

```ruby
rescue Ndb::RecordNotUnique => e
  e.code    # "unique_violation"
  e.status  # 409
```

## Testing

The tests of this gem build the daemon from the repository (`go build ./cmd/ndb`), start it on a short temp unix socket (macOS allows 104 bytes), wait for `GET /_health` to return 200, and stop it at exit. To do the same by hand:

```sh
ndb -config /nonexistent.yml -data-dir "$(mktemp -d)" -unix /tmp/ndb-test.sock
```

```ruby
Ndb::Rails.configure { |c| c.socket = "/tmp/ndb-test.sock" }
```

Use a new collection name per test (`self.collection_name = ...`). Run the suite with `bundle exec rake test` in `sdk/rails`. Its Gemfile uses the gem `ndb` from `../ruby`.
