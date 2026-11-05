# ndb-rails

<img src="https://raw.githubusercontent.com/thadeu/ndb/main/brand/png/lockup-on-light.png" width="220">

ActiveModel layer for [ndb](https://github.com/thadeu/ndb), an NDJSON query daemon. It gives you `Ndb::Record` (a model class like ActiveRecord), a lazy relation, validations, blobs and a Rails railtie. It sits on the plain client gem [`ndb`](https://github.com/thadeu/ndb/tree/main/sdk/ruby) (`Ndb::Client`), which does the HTTP over a unix socket or TCP. The gem only builds JSON. The daemon decides what every query means.

No Rails, or no models? Use the gem `ndb` alone: it has no runtime dependency.

Docs: [client guide](https://github.com/thadeu/ndb/blob/main/docs/dev/clients.md), [query language](https://github.com/thadeu/ndb/blob/main/docs/dev/query.md), [HTTP API](https://github.com/thadeu/ndb/blob/main/docs/dev/http-api.md).

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
- The address is `unix:///path`, `tcp://host:port`, or a bare path that starts with `/`.
- The token is `token`, or env `NDB_TOKEN`. It goes only in the `Authorization` header. A unix socket does not need it.
- `ready_timeout` is how long a call retries while the daemon is not ready (`not_ready`, or the socket is missing or refuses). It uses backoff with jitter. A write is retried only when the daemon certainly did not process it. A read is also retried after a dropped connection. A timeout is never retried.
- The pool drops its connections after a `fork`.
- `Ndb::Rails.client` is an `Ndb::Client` built from this configuration (`Ndb::Rails.configuration`). You can also build one: `Ndb::Client.new(socket: "/tmp/ndb.sock", token: nil, timeout: 30, pool_size: 16, ready_timeout: 10)`. The [`ndb` README](https://github.com/thadeu/ndb/blob/main/sdk/ruby/README.md) documents every client call.

## Define a model

```ruby
class Call < Ndb::Record
  primary_key :call_id

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
| `primary_key :call_id` | The id field. Default is `_id` (a string, made by the daemon). |
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
call = Call.create!(call_id: "c1", status: "answered", caller: "a", at: Time.now.utc)
Call.create!([{ call_id: "c2", status: "failed", caller: "b" }, { call_id: "c3", status: "answered", caller: "c" }])

call = Call.new(call_id: "c4", status: "answered", caller: "d")
call.save          # false when invalid
call.save!         # raises Ndb::RecordInvalid

call = Call.find("c1")        # raises Ndb::NotFound
Call.find_by(status: "failed") # nil when missing
Call.find_by!(status: "x")     # raises NotFound

call.update!(status: "done")  # sends only the changed fields
call.version                   # the document version

call.destroy
```

- Every load asks for the version (`_v`). `save`, `update` and `destroy` send it back as `if_version`. If another writer changed the document first, you get `Ndb::StaleObjectError` (code `version_conflict`).
- Setting an attribute to `nil` and saving removes the field from the document.
- A duplicate id raises `RecordNotUnique` (code `duplicate_id`).
- `persisted?`, `new_record?`, `destroyed?`, `id` and `version` work as in ActiveRecord.

```ruby
begin
  call.update!(status: "done")
rescue Ndb::StaleObjectError
  call = Call.find(call.id)
  retry
end
```

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
    .order(at: :desc, call_id: :asc)
    .limit(50)
    .offset(100)
    .to_a

Call.where(status: "answered").pluck(:call_id)         # one field: an array of values
Call.where(status: "answered").pluck(:call_id, :status) # several: an array of arrays
Call.where(status: "answered").first(5)
Call.where(status: "failed").exists?
```

- Chained `where` calls are ANDed. `$regex` does not exist in the daemon yet. See the [query language](https://github.com/thadeu/ndb/blob/main/docs/dev/query.md) for the full operator list.
- The order of the `order` keys is the order of the sort. The id is always the last key.
- `page(n).per(size)` sets offset paging. The default page size is 25.
- `pluck` asks the daemon for only those fields.
- There is no `explain` on a relation. Use the client: `Ndb::Rails.client.find(coll, "where" => {...}, "explain" => true)` returns a `Page` with `plan` (the `X-Ndb-Plan` header: `id`, `index:<field>`, `scan`, `snapshot`) and `examined`.

### Regular expressions

A Ruby `Regexp` works in `where`, `where.not` and `or`. The gem `ndb` turns it into `$regex` and `$options` (the daemon uses RE2). Ruby `^` and `$` match at each line, so `m` is always sent. `/i` is `i`, `/m` is `s`, `/x` is `x`. Look-around, backreferences, atomic groups, possessive quantifiers, `\h`, `\G`, `\R` and `\X` raise `Ndb::InvalidQuery` before the request.

```ruby
Call.where(caller: /^55/)
Call.where(caller: /^55/i).where.not(status: /fail/)
Call.where(caller: /^55/).or(Call.where(caller: /^11/))
Call.where(caller: { "$regex" => "^55" })   # a String: no `m`, and a literal ^prefix uses the index
```

A Regexp is a scan filter. For a prefix search that uses the index, pass the String form. See the [`ndb` README](https://github.com/thadeu/ndb/blob/main/sdk/ruby/README.md) for the full mapping.

### Count

```ruby
Call.where(status: "answered").count
```

### Cursor pagination

```ruby
relation = Call.where(status: "answered").order(:call_id).limit(100)
rows = relation.to_a
token = relation.next_cursor   # nil on the last page

Call.where(status: "answered").order(:call_id).limit(100).after(token).to_a
```

A token works only with the same `where` and `order`. Another query shape raises `InvalidCursor`.

To walk every row, use `find_each`. It follows the cursor in batches (default 1000):

```ruby
Call.where(status: "answered").find_each(batch_size: 500) { |call| puts call.call_id }

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
page.next_cursor  # nil on the last page
```

`$dateTrunc` units are `minute`, `hour`, `day`, `week` and `month`. Optional `"timezone"`: a name such as `"Europe/Rome"`.

## Watch

A watch streams the changes of a collection to a named consumer: first the events after its last ack, then new events as they happen. The watch API is on the client (`Ndb::Client`).

```ruby
client = Ndb::Rails.client

begin
  client.watch(Call.collection_name, "relay", where: { status: "answered" }) do |event|
    # event has "pos", "op" ("put" or "del"), "id", "v" and "doc"
    handle(event)

    client.ack(Call.collection_name, "relay", event["pos"])
  end
rescue Ndb::WatchLagged
  retry # the consumer was too slow. watch continues after the last ack.
rescue Ndb::HistoryTruncated
  raise # the saved position is too old. Start over.
end
```

- `where:` takes the same conditions as `where` on a model (Hash and Range). Only matching events come. A delete event has no `"doc"`; it is sent only if this stream saw an earlier matching put of that id.
- `from: "now"` starts a new consumer at the end. `limit: n` ends the stream after n events.
- Without a block, `watch` returns an Enumerator.
- Delivery is at least once. After a restart an event can come again, so make the work safe to repeat.
- `HistoryTruncated` means the WAL files for the position were deleted (`wal_retention`).
- A watch holds one connection until it ends.

## Blobs

Large values live in the blob store. The document keeps only a reference (`$blob`, `size`, `type`).

```ruby
call = Call.new(call_id: "c1", status: "answered", caller: "a",
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

The transport and daemon errors are defined by the gem `ndb`. They all live in the `Ndb` module (`Ndb::Error`, `Ndb::NotFound`, ...). For an ActiveRecord-like `rescue`, this gem adds two aliases: `Ndb::StaleObjectError` is `Ndb::VersionConflict`, and `Ndb::RecordNotUnique` is `Ndb::UniqueViolation`. They are the same classes, so a `rescue` of either name catches the same error. `Ndb::RecordInvalid` and `Ndb::RecordNotSaved` exist only here, and they inherit from `Ndb::Error`. Every error has `code` (the daemon code) and `status` (the HTTP status).

| Class | Code |
|---|---|
| `Unauthorized` | `unauthorized` |
| `NotReady` | `not_ready` |
| `NotFound` | `not_found` |
| `UnknownCollection` | `unknown_collection` |
| `StaleObjectError` | `version_conflict` |
| `RecordNotUnique` | `unique_violation`, `duplicate_id` |
| `CatalogConflict` | `catalog_conflict` |
| `CatalogInvalid` | `catalog_invalid` |
| `ReadOnlyCollection` | `direct_collection` |
| `InvalidQuery` | `invalid_query`, `invalid_request` |
| `InvalidCursor` | `cursor_invalid`, `cursor_mismatch`, `cursor_expired` |
| `WatchLagged` | `watch_lagged` |
| `HistoryTruncated` | `history_truncated` |
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

The tests of this gem build the daemon from the repository (`go build ./cmd/ndb`), start it on a short temp unix socket (macOS allows 104 bytes), wait for `GET /health` to return 200, and stop it at exit. To do the same by hand:

```sh
ndb -config /nonexistent.yml -data-dir "$(mktemp -d)" -unix /tmp/ndb-test.sock
```

```ruby
Ndb::Rails.configure { |c| c.socket = "/tmp/ndb-test.sock" }
```

Use a new collection name per test (`self.collection_name = ...`). Run the suite with `bundle exec rake test` in `sdk/rails`. Its Gemfile uses the gem `ndb` from `../ruby`.

## License

MIT.
