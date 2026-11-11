# Rails

The gem `ndb-rails`: an ActiveModel layer with `Ndb::Record`, and [streams](#streams) with `Ndb::Streams`. It depends on the gem `ndb` and uses `Ndb::Client` inside.

The source and the same guide are in [`sdk/rails`](https://github.com/thadeu/ndb/tree/main/sdk/rails).

> **Optimistic locking by default, pessimistic locking when you ask.** Every model uses optimistic locking with no setup: each save sends the version (`_v`) the record was read with, as `lock_version` does in ActiveRecord, without the column. Use `with_retry` to try again after a conflict. For pessimistic locking, `Model.lock.find(id)`, `lock!` and `with_lock` work as in ActiveRecord. See [Concurrency](../concurrency.md).

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

Keys: `url`, `socket`, `token`, `timeout`, `pool_size`, `ready_timeout`, `user_agent`. Any other key is ignored.

Any app: configure in code, for example in `config/initializers/ndb.rb`.

```ruby
Ndb::Rails.configure do |c|
  c.socket = "/run/ndb/ndb.sock"            # or: c.url = "tcp://10.0.0.5:7070"
  c.token = ENV["NDB_TOKEN"]
  c.timeout = 30
  c.pool_size = 16
  c.ready_timeout = 10
  c.user_agent = "myapp/1.2"                # optional: the token of your app
end
```

- The address is, in order: `url`, `socket`, env `NDB_URL`, then the default socket `/run/ndb/ndb.sock`.
- `/run/ndb/ndb.sock` is the socket of a server install (root, or `/var/lib/ndb` exists). A local daemon that you start as a user with no flags (`ndb serve`) listens on `~/.ndb/ndb.sock` and on TCP `127.0.0.1:7070`. In development, set `socket: <%= Dir.home %>/.ndb/ndb.sock` or `url: tcp://127.0.0.1:7070`.
- The address is `unix:///path`, `tcp://host:port`, `https://host[:port]` (ndb behind a TLS proxy), or a bare path that starts with `/`.
- The token is `token`, or env `NDB_TOKEN`. It goes only in the `Authorization` header. A unix socket does not need it.
- `ready_timeout` is how long a call retries while the daemon is not ready (`not_ready`, or the socket is missing or refuses). It uses backoff with jitter. A write is retried only when the daemon certainly did not process it. A read is also retried after a dropped connection. A timeout is never retried.
- The pool drops its connections after a `fork`.
- Every request sends `User-Agent: ndb-ruby/<version> (ruby <version>; <platform>) ndb-rails/<version> rails/<Rails.version>`, then the `user_agent` of the configuration (the token of your app, such as `myapp/1.2`), so the daemon can tell the apps apart.
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
| `index: true` | Index this field. For a [compound index](../query.md#indexes), call `index :account_id, :created_at` in the class; with `unique: true` it is a compound unique key (`index :account_id, :email, unique: true`). |
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

Call.where(status: "failed")   # a list (lazy relation)
Call.find_by(status: "failed") # one record or nil: the daemon gives 1 row (limit 1)
Call.find_by!(status: "x")     # the same, raises Ndb::NotFound when missing
call = Call.find("c1")         # by id, raises Ndb::NotFound

Call.first                     # the lowest _id; Call.last: the highest (limit 1, not the whole list)
Call.order(:created_at).last(3) # the last 3 in that order, oldest first

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
- `record.with_retry { |r| ... }` reads the document again, yields the record and saves it. On `Ndb::StaleObjectError` it reads again and yields again (5 tries, `with_retry(retries: 10)`), then raises it. An exception in the block saves nothing. Nothing waits on the daemon, and the block can run more than once. See [with_retry](../query.md#with-retry-read-change-write-retry).

  ```ruby
  Wallet.find("w1").with_retry do |w|
    raise InsufficientFunds if w.balance_cents < 500

    w.balance_cents -= 500
  end
  ```

- `lock` is pessimistic locking, with the names of ActiveRecord. One request holds the record; another `lock` of it waits (`wait:`, default 10 s, then `Ndb::Locked`), and a write from any other place waits for the release. So the code runs once: use it when the code has an effect outside ndb. See [Lock](../query.md#pessimistic-locking-one-holder-at-a-time).

  ```ruby
  # With a block: the lock ends with the block, also on an exception.
  Payment.lock.find("p1") do |p|
    next if p.status == "refunded"

    Gateway.refund(p.charge_id)
    p.update!(status: "refunded")
  end

  payment.with_lock { |p| ... }            # the same, on a loaded record (it is read again)

  # Without a block: the lock ends at the first save or destroy (also when it
  # raises), or at unlock!. A save that returns false keeps it.
  payment = Payment.lock.find("p1")
  payment.update!(status: "refunded")      # written with the lock, then released

  payment.lock!                            # lock a loaded record and read it again
  payment.unlock!                          # release without a write
  Payment.lock(ttl: 2.minutes.to_i, wait: 0).find_by(charge_id: "ch_1")
  ```

  Outside a transaction, prefer the block. Every lock also ends after `ttl:` (default 30 s): a process that dies holds nothing, and a save after it raises `Ndb::LockExpired` (the record then holds no lock, and its next save is a plain save). `lock.find` takes one id; `lock.find_by` and `lock.first` find the record, then lock it and read it again.
- `Model.transaction { }` writes everything of the block in one step, all or none: `save`, `update`, `create`, `destroy`, `increment!` and `decrement!` run their validations and callbacks, but are sent together at the end of the block. An exception writes nothing; `raise Ndb::Rollback` stops the block with no error (it returns `nil`). `lock: [records or ids]` with `ttl:` and `wait:` takes every lock in one call (no deadlock), and `lock!` inside the block joins it. One collection per block (`Ndb::TransactionError` otherwise). See [transaction](../concurrency.md#_4-transaction-many-writes-all-or-none).

  ```ruby
  Account.transaction(lock: [from, to], ttl: 5) do
    raise Ndb::Rollback if from.balance < amount

    from.update!(balance: from.balance - amount)
    to.update!(balance: to.balance + amount)
  end
  ```
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

`to_ndb` gives the JSON body that a relation sends to the daemon (`POST /{collection}/_search`), with no request, like `to_sql` in ActiveRecord:

```ruby
Call.where(status: "failed").order(created_at: :desc).limit(10).to_ndb
# => {"query":{"status":"failed"},"sort":{"created_at":-1},"limit":10,"versions":true}

puts Call.where(status: "failed").to_ndb(pretty: true)
```

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

## Lookup

`lookup` is a key of the HTTP search (`POST /{coll}/_search`): in the same call, the daemon reads the documents of other collections that each row points to, and puts them in the row. The HTTP API is the primitive and has every rule ([Lookup](../query.md#lookup)); this SDK only builds that JSON.

| Relation | The foreign key lives | Match | The row gets |
|---|---|---|---|
| belongs to | in **this** collection | `row.fk == other.pk` | an object, or null |
| has many | in the **other** collection | `other.fk == row.pk` | an array (100 unless a limit, at most 1000) |
| has one | in the **other** collection | `other.fk == row.pk` | an object, or null |

`pk` is `_id` unless you say otherwise. The field that is searched on the other side (its `pk` for a belongs to, its `fk` for a has many) must be `_id` or indexed, else the daemon answers `lookup_unindexed`.

In Rails the relations are associations on the model, with the Rails names and conventions, and `includes` sends them.

### Basic use

```ruby
class User < Ndb::Record
  attribute :name, :string

  has_many :cdrs                # cdrs.user_id -> users._id
  has_one :profile              # profiles.user_id -> users._id
end

class Cdr < Ndb::Record
  attribute :user_id, :string
  attribute :duration, :integer

  belongs_to :user              # cdrs.user_id -> users._id
end

User.where(plan: "pro").includes(:cdrs, :profile).each do |user|
  user.cdrs.sum(&:duration)     # loaded with the users, in the same call
end

Cdr.order(at: :desc).limit(50).includes(:user).map { |cdr| cdr.user.name }
```

Each association is a method, made by the declaration. Without `includes`, it loads on first use, with one more call for each record (the N+1 of ActiveRecord):

```ruby
cdr = Cdr.find("x1")
cdr.user                        # finds the user now: User.find_by(_id: cdr.user_id)
cdr.user                        # the same object, no call
```

### has_many is a relation

`user.cdrs` is an `Ndb::Relation`, as in ActiveRecord: lazy and chainable. A builder on it is a new query, scoped to the user:

```ruby
user = User.find("u1")

user.cdrs.where(status: "answered").order(at: :desc).limit(10).to_a
user.cdrs.count                 # a count in the daemon
user.cdrs.each { |cdr| ... }    # loads once, then reads from memory

User.includes(:cdrs).each do |u|
  u.cdrs.size                   # loaded with the users: no call
  u.cdrs.where(status: "missed").count   # a builder queries again
end
```

### Stale associations

An association is kept on the record for the key it was read with:

```ruby
cdr.user                        # Ana
cdr.user_id = "u2"
cdr.user                        # the foreign key changed: reads again, Bruno
cdr.reload                      # reads the document; every association loads again on next use
```

### What is inferred

Nothing needs to be said when the names follow Rails:

| Declaration | Class | Foreign key | Primary key | Match |
|---|---|---|---|---|
| `User has_many :cdrs` | `Cdr` (`"cdrs".singularize.camelize`) | `user_id`, on each CDR | `_id`, on the user | `cdrs.user_id == users._id` |
| `User has_one :profile` | `Profile` | `user_id`, on the profile | `_id` | `profiles.user_id == users._id` |
| `Cdr belongs_to :user` | `User` | `user_id`, on the CDR | `_id`, on the user | `cdrs.user_id == users._id` |

The foreign key of a `has_many` or `has_one` is named after the model that declares it (`User` → `user_id`), and lives on the other side; the one of a `belongs_to` is named after the association (`:user` → `user_id`) and lives on this side. The collection is the `collection_name` of the class.

### When the names differ

A value you pass always wins over the inference:

```ruby
class User < Ndb::Record
  has_many :cdrs, fk: :account_id                     # cdrs.account_id -> users._id
  has_many :calls, class_name: "CDR", fk: :account_id # an acronym class
end

class Cdr < Ndb::Record
  belongs_to :caller, class_name: "User", fk: :account_id
  belongs_to :plan, fk: :plan_code, pk: :code         # plans.code instead of _id
end
```

`fk:` and `foreign_key:` are the same option, as are `pk:` and `primary_key:`; passing both names of one option is an `ArgumentError`.

### Options

| Option | On | |
|---|---|---|
| `class_name:` | all | the model of the other side |
| `fk:` / `foreign_key:` | all | the foreign key field |
| `pk:` / `primary_key:` | all | the field it points to (default `_id`) |
| `sort:` | `has_many`, `has_one` | `sort: { at: -1 }` |
| `limit:` | `has_many` | per record, 1 to 1000 (default 100) |
| `where:` | `has_many`, `has_one` | filter the other side: `where: { status: "answered" }` |

```ruby
has_many :recent_cdrs, class_name: "Cdr", sort: { at: -1 }, limit: 20, where: { status: "answered" }
```

### Nested

Two levels, as in ActiveRecord:

```ruby
Order.includes(:customer, items: :product).to_a
```

### Index

`has_many` and `has_one` create the index on the foreign key of the other model (`cdrs.user_id`) the first time `includes` uses them; the lookup needs it. Declaring it on the other model also works: `attribute :user_id, :string, index: true`. A `belongs_to` with `pk:` other than `_id` needs that field indexed on the other model (`attribute :code, :string, index: true`).

### Limits

- Associations read; they do not write: no `user.cdrs << cdr`, `build`, `dependent:` or `counter_cache`.
- No `joins(...).where` (filtering users by their CDRs); query the CDRs and read `user_id`.
- `Ndb::LookupError` for a bad relation, a missing index or more than 10 000 documents in one answer.

## Aggregate

Relations have simple group calculations. Each one is one aggregate request.

```ruby
Call.group(:status).count            # => {"answered" => 2, "failed" => 1}
Call.group(:status).sum(:duration)
Call.group(:status, :caller).count   # keys are arrays
Order.where(status: "paid").unwind(:items).group("items.sku").sum("items.qty")   # one row per element of items ($unwind)
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

## Streams

A stream is an append-only log with consumer groups, with the behavior of Redis Streams. A class in `app/streams` appends to a stream, consumes it, or does both. There is no base class: the class has `include Ndb::Streams`. See [Streams](../streams.md) for the model. `Ndb::Stream` (singular) is the low-level client of the gem `ndb`; see the [Ruby SDK](ruby.md#streams).

`Ndb::Streams` needs no Rails: `require "ndb/streams"` loads it in any Ruby program. Then the client comes from `NDB_URL` and `NDB_TOKEN`, or from `Ndb::Streams.client = Ndb::Client.new(...)`.

### A writer

```ruby
# app/streams/order_event.rb
class OrderEvent
  include Ndb::Streams
end

OrderEvent.append(order_id: 42)   # => "1731164400000-0"
```

- A class without a `perform` method is a writer.
- The stream name comes from the class name: `OrderEvent` is `order_events`, `Billing::OrderEvent` is `billing_order_events`, and `Category` is `categories`. [`stream`](#stream-and-its-options) sets another name.
- The name uses the ActiveSupport inflections (`underscore` and `pluralize`). `ndb/streams` loads them, so the name is the same with and without Rails. An inflection of the app (`config/initializers/inflections.rb`) changes the name: set the name with `stream` when it must not change. The class keeps its name after the first call.
- A class name needs no suffix.

### A consumer

```ruby
# app/streams/billing_paid.rb
class BillingPaid
  include Ndb::Streams

  stream :order_events, group: :billing

  def perform(event)
    Invoice.create!(order_id: event[:order_id])
  end
end
```

- A class with a `perform` method is a consumer. It needs `group:`.
- Each group gets every entry one time. Give each service (billing, mail, search) its own group. The threads and the processes of one group share the entries.
- One class has one stream and one group. Put shared code in a module.
- A class can do both: it appends with `append` and it consumes with `perform`.
- The consumers run in a process of their own: see [Run the consumers](#run-the-consumers).

### stream and its options

```ruby
stream :order_events, group: :billing, concurrency: 2, max_deliveries: 5
stream OrderEvent, group: :billing    # the stream of another class
stream group: :billing                # the name from the class name
```

`stream(name = nil, **options)`. The name is a Symbol, a String, or a class that has `include Ndb::Streams`. Every option has a default:

| Option | Default | Meaning |
|---|---|---|
| `group:` | none | The consumer group. Required in a class with `perform`. |
| `concurrency:` | `1` | The threads of this class in each `ndb streams` process. |
| `max_deliveries:` | none (no limit) | After this many deliveries, the entry goes to the dead letter and is acked. |
| `dead_letter:` | `"<stream>_dead"` when `max_deliveries` or `max_age` is set | The stream that gets the poison entries. `false`: no dead letter stream. |
| `claim_idle:` | `1.minute` | An entry pending for this long is tried again. Seconds or a duration. |
| `retry:` | none | The wait before the next try of a failed entry: an Array of seconds or durations (the last one repeats), or a callable `->(deliveries) { seconds }`. See [Delayed retry](#delayed-retry). |
| `max_age:` | none | Seconds or a duration. An entry older than this (by the time of its ID) goes to the dead letter (reason `max_age`) and is acked, with no retry. |
| `start:` | `:new` | Where a new group starts. `:new`: only the entries appended after the group is made. `:all`: from the start of the stream. |
| `discard_on:` | `[]` | Exception classes. An error of these classes acks the entry without a retry. |
| `max_len:` | none (keep everything) | Keep only the newest N entries. It is an exact trim on each append. |
| `retain:` | none (keep everything) | Delete the entries older than this: `"30d"`, `30.days`, or seconds. It is the `retention` of the collection. |

- An option that is not in the table, or a bad value, raises `ArgumentError` when the class loads.
- A second `stream` call in the same class (or in a subclass) merges its options with the first ones.
- `start:` is read only when the group is made. A change of `start:` does not move a group that exists.
- `OrderEvent.stream_name` and `OrderEvent.stream_options` give the name and every option with its value.

### append

```ruby
OrderEvent.append(order_id: 42)                           # one entry => its ID
OrderEvent.append([{ order_id: 1 }, { order_id: 2 }])     # many entries => their IDs
OrderEvent.append('{"order_id":42}')                      # a JSON string (an object or an array)

OrderEvent.enqueue(order_id: 42)                          # the same method
OrderEvent.perform_async(order_id: 42)                    # the same method
```

- The payload is a Hash (one entry), an Array of Hash (many entries, in one request) or a JSON string of one of the two. Any other value raises `ArgumentError`. An empty Array returns `[]` and sends nothing.
- `enqueue` and `perform_async` are aliases of `append`.
- The payload is JSON. A Time or a Date is written as a String, and it comes back as a String. See [The contract between languages](../streams.md#the-contract-between-languages).
- A field name that starts with `_` is reserved.
- With `max_len:`, each append trims the stream.

**In a transaction.** In an ActiveRecord transaction, the append waits for the commit. A rollback drops it.

```ruby
Order.transaction do
  order.update!(status: "paid")

  OrderEvent.append(order_id: order.id)   # => nil; sent after the commit
end
```

- In a transaction, `append` returns `nil`: the entry has no ID yet.
- This needs Rails 7.2 or newer (`ActiveRecord.after_all_transactions_commit`). With an older Rails, or without ActiveRecord, the append is sent at once.
- It is not an outbox. The append is a request after the commit: if the process dies between the commit and the request, the entry is lost. If the daemon is down, the error comes from the `commit`, after the data of the transaction is saved.

### last

```ruby
OrderEvent.last        # the newest entry, or nil
OrderEvent.last(10)    # the 10 newest entries, oldest first
```

- Each entry is an [event](#perform-and-the-event). `last`, `find` and `range` read without a group: they change no group, and `event.deliveries` is `nil`.
- A stream that does not exist gives `nil` or `[]`.

### find

```ruby
OrderEvent.find("1731164400000-0")   # the entry with this ID, or nil
```

### range

```ruby
OrderEvent.range                                        # every entry, oldest first
OrderEvent.range(1.hour.ago, Time.now, count: 100)
OrderEvent.range("1731164400000-0", "+")
```

- `range(from = "-", to = "+", count: nil)`: the entries from `from` to `to`, both included, oldest first.
- Each end is an entry ID or a Time. A Time as `from` is the first ID of that millisecond; as `to` it is the last ID of that millisecond.
- `count:` is the most entries. Without it, every entry of the range comes.
- A stream is also a collection: for a filter on the fields, use `Ndb::Rails.client.search` on the stream name.

### Stream count

```ruby
OrderEvent.count   # => 1280
```

- How many entries the stream has now. A stream that does not exist gives 0.

### perform and the event

```ruby
def perform(event)
  event[:order_id]     # one field, by Symbol or String
  event.key?(:total)   # true when the entry has the field
  event.to_h           # the payload, without "_id" and "_ts"
  event.id             # "1731164400000-0"
  event.time           # the Time of the ID (UTC)
  event.deliveries     # 1 on the first delivery, 2 on the first retry
end
```

- The runner makes a new instance of the class for each entry and calls `perform(event)`. `perform` can be private.
- `perform` that returns acks the entry. `perform` that raises leaves it pending: see [Failure and retry](#failure-and-retry).
- In a Rails app, each `perform` runs in the Rails reloader (`Rails.application.reloader.wrap`), so a changed class loads again in development and the ActiveRecord connections go back to the pool. The options (`discard_on` and the others) come from the class after the reload.
- `event.to_h` is a copy. With ActiveSupport, it reads a Symbol and a String as the same key.
- There are no typed fields. The payload comes as JSON gives it: a number is a number, a date is a String.
- Delivery is at least once: after a crash or a retry, an entry can come again. Make `perform` safe to repeat.
- A deleted entry (a trim or a delete while it was pending) is acked without a `perform`.

### perform_now

Runs `perform` on one payload now, in the caller's thread, with no request: nothing is written to ndb and nothing is acked. It takes the path of a delivery (the Rails reloader, `around_perform`, `discard_on`) and returns the value of `perform` (`nil` when `discard_on` took the error). Use it in a unit test, or to run one entry by hand in a console.

```ruby
PostCallJob.perform_now(call_id: 42)                       # the fields as keywords
PostCallJob.perform_now({ "call_id" => 42 })               # a Hash
PostCallJob.perform_now('{"call_id":42}')                  # a JSON string
PostCallJob.perform_now(call_id: 42, _id: "1731163600000-0", _deliveries: 3)
```

- `_id` and `_deliveries` set `event.id` and `event.deliveries` (default: an ID of now, and 1). They are not payload fields: a name that starts with `_` is reserved in a stream.
- An error of `perform` (not one of `discard_on`) goes to the caller. There is no retry, no dead letter and no group.
- A class without `perform` (a writer) raises `ArgumentError`, and so does an Array (one entry only).
- To test the whole path, with the append and the consumers of the stream, use `Ndb::Streams.inline!` (see [Test the streams](#test-the-streams)).

### around_perform

```ruby
class BillingPaid
  include Ndb::Streams

  stream :order_events, group: :billing

  around_perform do |event, block|
    Current.set(tenant: event[:tenant]) { block.call }
  end

  def perform(event) = Invoice.create!(order_id: event[:order_id])
end
```

- The block runs around each `perform`, on the instance (`self` is the consumer object). It gets the event and a callable. `block.call` continues. Without `block.call`, `perform` does not run and the entry is acked.
- Many `around_perform` blocks run in the order of their declaration; the first one is the outer one.
- `around_perform` is the only hook. There is no `before_perform` or `after_perform`.

### Failure and retry

```ruby
stream :order_events,
  group: :billing,
  claim_idle: 30.seconds,
  max_deliveries: 5,
  discard_on: [ActiveRecord::RecordNotFound]
```

- **An error in `perform`** leaves the entry pending. The error goes to the log (`Rails.logger`), and the thread continues with the next entry. Any exception is an error here, also one that is not a `StandardError` (`NotImplementedError`, `SystemStackError`). Only `SignalException` (with `Interrupt`) and `SystemExit` go on and stop the thread.
- **Retry.** Without `retry:`, a thread of the group claims the entries that are pending for `claim_idle` or longer, of any consumer, and runs them again. It looks for them every `claim_idle / 2` (at least 1 second). So a retry comes between `claim_idle` and 1.5 times `claim_idle` after the failure. With `retry:`, the wait comes from the schedule (see [Delayed retry](#delayed-retry)).
- **`discard_on`.** An error of one of these classes (or of a subclass) is a warning in the log, and the entry is acked. Nothing goes to the dead letter.
- **`max_deliveries`.** An entry that fails on its `max_deliveries`-th delivery is poison. At that failure, the thread appends the entry to the dead letter stream, acks it and writes a warning. With `max_deliveries: 5`, `perform` runs 5 times at most.
- **The dead letter** is the stream `<stream>_dead` (or the name in `dead_letter:`). Each entry of it is `{stream, group, id, deliveries, reason, doc}`; `reason` is `"max_deliveries"` or `"max_age"`. With `dead_letter: false`, a poison entry is only acked and logged.
- **After a restart**, each thread reads its own pending entries first. That read counts as one delivery, as in Redis.
- **A crash in `perform`.** When `perform` stops the process (an out-of-memory kill, a segfault), the entry is not acked and gets no failure. With `max_deliveries`, the read of the pending entries after the restart sends an entry with more than `max_deliveries` deliveries to the dead letter, without `perform`. So a poison entry that crashes the process stops after `max_deliveries` runs.

```ruby
Ndb::Rails.client.stream("order_events_dead").range.each do |entry|
  entry.doc   # {"stream" => "order_events", "group" => "billing", "id" => "...", "deliveries" => 5, "reason" => "max_deliveries", "doc" => {...}}
end
```

Limits:

- Without `max_deliveries` or `max_age`, an entry that always fails is tried again with no end.
- The dead letter has no consumer of its own. Read it with the low-level client, or write a class with `stream :order_events_dead`.

### Delayed retry

ndb only (it uses `nack` of the daemon; see the Ruby client). The fsw-esl outbox schedule: try 1 waits 15 s, tries 2 to 5 wait 1 min, 6 to 11 wait 10 min, 12 to 23 wait 1 h, then 6 h, for at most 15 days:

```ruby
# app/streams/post_call_job.rb
class PostCallJob
  include Ndb::Streams

  stream :postcall_jobs,
    group: :jobs,
    retry: [
      { wait: 15.seconds, up_to: 1 },
      { wait: 1.minute, up_to: 5 },
      { wait: 10.minutes, up_to: 11 },
      { wait: 1.hour, up_to: 23 },
      { wait: 6.hours }
    ],
    max_age: 15.days

  def perform(event)
    PostCall.deliver(event[:call_id])
  end
end
```

- A failed `perform` gives the entry back to the group after the wait for its delivery count: 15 s after the first failure, 1 min after failures 2 to 5, and so on; 6 h after each failure from the 24th. An entry that always fails runs 81 times in 15 days. A plain list (`retry: [15.seconds, 1.minute, 6.hours]`) is one wait for each try, and the last one repeats. It does not come back before that wait, also after a restart, and any thread of the group takes it when it is due (in about 1 second).
- `retry:` also takes a callable, `retry: ->(deliveries) { 2**deliveries }`, or `Ndb::Stream.tiers(...)` (with steps, see the Ruby client).
- `max_age: 15.days`: an entry older than 15 days goes to `postcall_jobs_dead` with the reason `max_age`, without `perform`. An entry that fails when it is older than that goes there at once, with no more wait.
- With `max_deliveries:` too, an entry that fails on its last delivery goes to the dead letter, not to a retry.
- `discard_on:` still acks without a retry.
- `bundle exec ndb streams` writes the schedule of each consumer to the log at the start: `PostCallJob retry: 15s x1, 1m x4, 10m x6, 1h x12, then 6h; max age 15d`. A consumer without `retry:` writes `retry: after claim_idle (1m)`.
- **The dead letter** is a normal stream, and nothing delivers its entries again. Send one back with `append` (a new entry: new ID, delivery count 0, new max age):

```ruby
dead = Ndb::Rails.client.stream("postcall_jobs_dead")

dead.range.each do |entry|
  next unless entry.doc["reason"] == "max_age"

  PostCallJob.append(entry.doc["doc"])
  dead.delete(entry.id)
end
```

  For a rule of the app, a class can consume the dead letter: `stream :postcall_jobs_dead, group: :replay` with a `perform` that appends `event[:doc]` to the source. See [The dead letter](../streams.md#the-dead-letter).
- **Inline mode** (`Ndb::Streams.inline!`) runs `perform` one time, in the caller's thread. A failure raises to the caller, as without `retry:`: the schedule and `max_age` do not apply inline.

### ApplicationStream

```ruby
# app/streams/application_stream.rb
class ApplicationStream
  include Ndb::Streams

  stream claim_idle: 30.seconds, max_deliveries: 5

  around_perform { |event, block| Current.set(tenant: event[:tenant]) { block.call } }
end

class BillingPaid < ApplicationStream
  stream :order_events, group: :billing

  def perform(event) = Invoice.create!(order_id: event[:order_id])
end
```

- A subclass starts with the stream name, the options and the `around_perform` blocks of its parent. Its own `stream` call merges with them.
- A parent without a stream name gives none: the name of each subclass comes from its own class name.
- A subclass of a consumer class is a consumer too, with the same stream and group. Give it its own `group:`, or the two classes are a [`DuplicateGroup`](#stream-errors).

### Run the consumers

```sh
bundle exec ndb streams                           # every consumer class of app/streams
bundle exec ndb streams --only BillingPaid -c 4   # one class, 4 threads
bundle exec ndb streams -e production
bundle exec ndb streams -r ./consumers.rb         # without Rails
```

The `ndb` command comes with the gem `ndb`; `streams` needs the gem `ndb-rails`. In a Rails app (a directory with `config/environment.rb`), it loads the app, then loads the classes of `app/streams` in its own thread (in the reloader of the app) before it reads them, also when the app does not eager load. It starts the threads; each thread makes its group, and the command runs until a signal.

| Flag | Meaning |
|---|---|
| `-r`, `--require PATH` | A file that loads the stream classes. Required without Rails. With Rails, it loads after the app. |
| `-e`, `--environment NAME` | Sets `RAILS_ENV`. |
| `--only A,B` | Run only these consumer classes. A name that is not a consumer class is an error. |
| `-c`, `--concurrency N` | The threads of each class. It replaces `concurrency:` of every class. |
| `-h`, `--help` | Print the help. |

- A wrong flag, or a wrong config of a class, prints `ndb streams: <message>` and exits with status 2. Any other error at the boot of the app (also an `ArgumentError`) is raised with its backtrace (status 1).
- **Stop.** `SIGTERM` and `SIGINT` stop the process: each thread ends its current entries, acks them and stops. A read waits 2 seconds at most, so an idle process stops in about 2 seconds. A signal during the start starts no more threads.
- **A second signal.** In the first 2 seconds after the first signal, a second signal does nothing (a terminal and Puma can send two). After that, a second signal interrupts the current `perform` calls: the entries done before them are acked, and the interrupted entries stay pending. A third signal ends the process at once, with status 1.
- **The daemon is down.** Each thread makes its group, then reads. When the daemon is down, the thread writes the error to the log and tries again, with a wait of up to 5 seconds. The process does not exit, also when the daemon is down at the start.
- **A thread that ends.** When a thread ends before the stop (an exception outside `perform`, a `Thread#kill`), the process writes an error and starts the thread again.
- **Consumer names.** Each thread is a consumer with a stable name: `NDB_CONSUMER` (default: the hostname), then `:` and the thread index (`web-1:0`). After a restart with the same name, a thread reads its own pending entries first. **Two processes on one host must have different `NDB_CONSUMER` values.** With the same name they are one consumer for the daemon, and each one also reads the pending entries of the other at its start.
- Any other command (`bundle exec ndb serve`, ...) goes to the daemon binary: see [The ndb executable](ruby.md#the-ndb-executable).

**Defaults in `config/ndb.yml`.** The `streams:` block has two keys:

```yaml
production:
  streams:
    concurrency: 4
    only: [BillingPaid]
```

- `concurrency` replaces `concurrency:` of every class, as `-c` does. `only` is the default of `--only`. A flag wins over the file.
- The file says nothing about what a process does: the command does that.

**With Puma.** The plugin runs the same command next to Puma:

```ruby
# config/puma.rb
plugin :ndb
```

- After Puma boots, the plugin starts `bin/ndb streams` when the app has a `bin/ndb` file, or `bundle exec ndb streams`. It is a child process, not threads in Puma.
- The process has its own process group, so a Ctrl-C in the terminal goes to Puma only. When Puma stops or restarts, the plugin sends one `SIGTERM` to the process, waits up to 30 seconds, then kills it.
- When the consumers stop by themselves, Puma stops too. So a broken consumer is not hidden.
- Without `NDB_STREAMS_IN_PUMA`, the plugin runs only in development (`RAILS_ENV`, `RACK_ENV` or `APP_ENV`; none of them is development). `NDB_STREAMS_IN_PUMA=true` (or `1`, `yes`, `on`) turns it on, and `false` (or `0`, `no`, `off`) turns it off. Any other value is an error at the start of Puma.
- In production, a process of its own (`bundle exec ndb streams`) is the usual choice: it scales and restarts apart from the web process.

### Groups at boot

- Any process of the Rails app (web, console, a job worker) loads `app/streams` at boot and makes the missing groups, in a background thread. The boot does not wait for ndb.
- So a group exists before the first `ndb streams` process runs, and a group with `start: :new` gets the entries that the web process appends from then on.
- When the daemon is down, the thread tries 5 times (it waits 1, 2, 4 and 8 seconds), then writes a warning. `ndb streams` makes the groups again at its start, and does not wait for this thread: it loads `app/streams` itself.
- A stream with `retain:` is made here too, with its retention. A stream without `retain:` is made by the first append or by its first group.
- Limit: with `start: :new`, the entries appended before the group is made are not delivered to it. For a new consumer of an old stream, use `start: :all`.
- Without Rails, `Ndb::Streams.ensure_groups!` makes them.

### Notifications

`ActiveSupport::Notifications` gets two events:

| Event | Payload |
|---|---|
| `append.ndb_streams` | `stream` (the name), `class`, `count` (the entries of this append) |
| `perform.ndb_streams` | `stream`, `group`, `class`, `event` |

```ruby
ActiveSupport::Notifications.subscribe("perform.ndb_streams") do |name, start, finish, _id, payload|
  Rails.logger.info("#{payload[:class]} #{payload[:event].id} #{((finish - start) * 1000).round} ms")
end
```

- `perform.ndb_streams` covers `perform` and its `around_perform` blocks. When `perform` raises, the payload has `exception`, as for every notification.

### Test the streams

```ruby
# test/test_helper.rb
require "ndb/rails/minitest"

Ndb::Streams.inline!
```

- **`PostCallJob.perform_now(...)`** runs one consumer on one payload, with no ndb: see [perform_now](#perform-now).
- **`Ndb::Streams.inline!`**: an append writes the entry, then calls `perform` of each consumer class of that stream, in the same thread. No `ndb streams` process is necessary. `Ndb::Streams.inline = false` turns it off.
- In inline mode, an error in `perform` goes to the caller of `append` (an error of `discard_on` is only logged). `event.deliveries` is 1. There is no retry and no dead letter, and the groups are not used.
- The first inline `append` loads `app/streams` (in the reloader of the app), so the consumers are there also right after the boot, when the app does not eager load.
- In a transaction, the `perform` calls run after the commit, with the append.
- `ndb/rails/minitest` and `ndb/rails/rspec` truncate the streams that a test appended to, with the collections (see [Testing](#testing)). The stream names get the prefix of the parallel worker.

```ruby
class CheckoutTest < ActiveSupport::TestCase
  test "it appends the event" do
    Checkout.call(order)

    assert_streamed OrderEvent, order_id: order.id
    assert_no_streamed OrderEvent, order_id: 0
  end
end
```

- `assert_streamed(klass, **fields)` passes when the stream of the class has an entry with these fields. Without fields, any entry passes. It returns the events that match.
- `assert_no_streamed(klass, **fields)` passes when no entry has these fields.
- The values are compared as JSON: a Symbol is equal to its String.
- The two read the newest 1 000 entries of the stream.

```ruby
# spec/rails_helper.rb
require "ndb/rails/rspec"

expect(OrderEvent).to have_streamed(order_id: 42)
expect(OrderEvent).not_to have_streamed(order_id: 0)
```

### Stream errors

| Class | When |
|---|---|
| `Ndb::Streams::MissingGroup` | A class has `perform` and its `stream` has no `group:`. |
| `Ndb::Streams::DuplicateGroup` | Two consumer classes have the same group on one stream. |
| `Ndb::Streams::ConfigurationError` | The parent of the two. Also: a class without a name and without `stream :name`. |

- All are subclasses of `ArgumentError`.
- The two group errors are not raised when the class loads: the check runs when the consumers are listed. `ndb streams` prints the message and exits with status 2. The boot of another process writes a warning. In inline mode, the `append` raises.
- An unknown option or a bad value of `stream` raises `ArgumentError` when the class loads.
- The errors of the daemon are the `Ndb::Error` classes of the gem `ndb`: see [Stream errors](ruby.md#stream-errors) of the Ruby SDK.

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

## Method reference

Every public method of the gem, grouped. The names and return values follow ActiveRecord; what is not in these lists does not exist (no `joins`, `transaction`, `dependent:`, migrations).

::: details Declare a model (class methods)

| Method | |
|---|---|
| `attribute :name, :type, index:, unique:, time:, expires:` | an ActiveModel attribute; `index`/`unique` declare an index or a unique key; `time` is the time field; `expires` holds the expiry of each document |
| `self.collection_name = "calls"` / `collection_name` | the collection (default: the plural model name). A pattern (`"logs-2026-*"`) reads many collections and writes none |
| `ttl 24.hours` | documents expire this long after the time field |
| `source "/var/log/app/*.ndjson"` | a direct collection: the app files are the data, ndb only reads |
| `belongs_to`, `has_many`, `has_one` | associations, see [Lookup](#lookup) |
| `self.abstract_class = true` / `abstract_class?` | a base class with no collection |
| `primary_key` | always `"_id"` |
| `time_field`, `expires_field`, `indexed_fields`, `unique_fields`, `collection_definition` | what the model declares |
| `collection_pattern?` | `true` for a pattern |
| `ensure_collection!` | declares the collection in the daemon (done by itself on first use, and at boot by the Railtie) |

:::

::: details Query (on the model and on a relation)

| Method | Returns |
|---|---|
| `all` | a relation of every document |
| `where(hash)`, `where.not(hash)`, `where_raw(hash)`, `or(relation)` | a relation. A Range is `$gte`/`$lte`, an Array is `$in`, `nil` is a missing field; `where_raw` sends the Hash as is |
| `order(:a, b: :desc)`, `limit(n)`, `offset(n)`, `page(n)`, `per(n)`, `after(cursor)` | a relation |
| `includes(:a, b: :c)` | a relation that loads associations in the same call |
| `group(*fields)` | a relation for `count`/`sum`/... by group |
| `find(id)`, `find(id1, id2)` | a record, or an Array; `Ndb::NotFound` when missing |
| `find_by(hash)`, `find_by!(hash)`, `find_by_<attr>[_and_<attr>](values)` | a record or `nil` (`!`: `Ndb::NotFound`) |
| `to_ndb`, `to_ndb(pretty: true)` | the JSON body of the search, as a String; no request |
| `first`, `first(n)`, `last`, `last(n)` | a record or `nil`, or an Array. The daemon gives only `n` rows. With no `order`, the order is `_id` (first and last are its two ends). On a relation with `limit`, `offset` or `page`, or a loaded one, `last` uses the loaded rows. |
| `to_a`, `each`, `map`... (Enumerable), `load`, `loaded?`, `records` | the records (one call, then memory) |
| `size`, `length`, `empty?` | from the loaded records, else a count |
| `count`, `exists?`, `pluck(:a, :b)` | a number, a boolean, an Array of values |
| `sum(:f)`, `average(:f)`, `minimum(:f)`, `maximum(:f)` | a number (or a Hash by group) |
| `find_each(batch_size:)`, `in_batches(of:)` | every record, page by page with the cursor |
| `next_cursor` | the cursor of the next page |
| `search(body)` | a raw search body; `SearchResult` (`records`, `next_cursor`, `total`) |
| `to_query` | the JSON body the relation sends |

:::

::: details Write

| Method | |
|---|---|
| `Model.create(attrs)`, `create!(attrs)`, `create([attrs, ...])` | insert with validations and callbacks (`!` raises) |
| `Model.upsert(attrs)`, `upsert_all([attrs, ...])` | create or merge by `_id`, no validations or callbacks |
| `Model.find_or_create_by(hash)`, `find_or_create_by!(hash)` | atomic (an upsert) |
| `Model.find_or_initialize_by(hash)` | a found record, or a new one not saved |
| `Model.destroy(id)`, `Model.delete(id)` | by id, with or without callbacks |
| `relation.update(ops)` | one atomic update on every match (`{ "$inc" => { "n" => 1 } }`); the number modified |
| `relation.delete_all`, `relation.destroy_all` | delete every match (no callbacks / with callbacks) |
| `Model.truncate!`, `Model.drop!` | empty the collection / remove it (keeps / removes the definition) |

:::

::: details A record (instance methods)

| Method | |
|---|---|
| `save(validate: true, expire_in: nil)`, `save!` | insert or update the changed fields; `Ndb::StaleObjectError` when another write changed it |
| `update(attrs)`, `update!(attrs)` | assign and save |
| `destroy`, `destroy!`, `delete` | remove, with or without callbacks |
| `reload` | read the document again: attributes, version, associations |
| `increment!(:n, by)`, `decrement!(:n, by)` | an atomic `$inc` |
| `expire(30.minutes)`, `persist_ttl!`, `ttl` | the expiry of this document |
| `create_unless_exists(attrs)`, `create_unless_exists!(attrs)` | insert only when the `_id` is free |
| `id`, `id=`, `_id` | the primary key (`id` is an alias) |
| `version` | `_v`, the version the record was read with |
| `persisted?`, `new_record?`, `destroyed?`, `to_key`, `==` | state and identity, like ActiveRecord |
| `changed?`, `changes`, `<attr>_changed?`, `valid?`, `errors`, `as_json`, `to_json` | from ActiveModel (Dirty, Validations, Serializers) |
| `source_collection` | the collection a record came from, when the model reads a pattern |
| `customer`, `items`, ... | one method per association: a record or `nil` (`belongs_to`, `has_one`), a relation (`has_many`) |

:::

::: details Blobs

| Method | |
|---|---|
| `attribute :recording, :blob` | a large value stored apart, referenced by the document |
| `Ndb::Blob.new(io_or_string, content_type:)` | a blob to assign; it uploads on save |
| `blob.read(n)`, `each_chunk`, `rewind`, `eof?` | stream the content |
| `blob.uploaded?`, `upload!`, `to_reference` | its state and its `sha256:` reference |

:::

::: details Tests

`Ndb::Testing.clean!`, `clean_all!`, `worker!`, `touched`; `ndb/rails/minitest` (Minitest), `ndb/rails/rspec`, `ndb/rails/database_cleaner`. See [Testing](#testing).

:::

::: details The low layer

`Ndb::Rails.client` is the `Ndb::Client` of the gem: `watch`, `ack`, `aggregate`, `restore`, `rename`, `pitr`, `ensure_index` and every route of the HTTP API. See the [Ruby SDK](ruby.md).

:::

## Errors

The transport and daemon errors are defined by the gem `ndb`. They all live in the `Ndb` module (`Ndb::Error`, `Ndb::NotFound`, ...). For an ActiveRecord-like `rescue`, this gem adds one alias: `Ndb::RecordNotUnique` is `Ndb::UniqueViolation` (the same class). `Ndb::StaleObjectError`, `Ndb::RecordInvalid`, `Ndb::RecordNotSaved` and `Ndb::RecordNotDestroyed` exist only here, and they inherit from `Ndb::Error`. Every error has `code` (the daemon code) and `status` (the HTTP status).

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
| `InvalidQuery` | `invalid_query`, `invalid_request`, `invalid_pipeline` |
| `InvalidCursor` | `cursor_invalid`, `cursor_mismatch`, `cursor_expired` |
| `WatchLagged` | `watch_lagged` |
| `HistoryTruncated` | `history_truncated` |
| `TtlNotEnabled`, `TtlDerived` | `ttl_not_enabled`, `ttl_derived` |
| `RecordInvalid` | ActiveModel validation failed (has `record`) |
| `RecordNotSaved` | `save!` returned false without errors, or `reload`/`update` of a record that is not saved |
| `RecordNotDestroyed` | `destroy!` when a `before_destroy` callback aborts (has `record`) |
| `ConnectionError` | cannot reach the daemon. Subclasses: `ConnectError` (nothing was sent), `TimeoutError` |

The gem `ndb` maps more codes to classes (`Forbidden`, `InvalidDocument`, `Closed`, ...); the full list is on [Ruby errors](ruby.md#errors). Any other code is a plain `Ndb::Error`. A response that is not a daemon error has the code `http_status`.

```ruby
rescue Ndb::RecordNotUnique => e
  e.code    # "unique_violation"
  e.status  # 409
```

## Testing

ndb has no transactions, so a test cannot roll back like `use_transactional_tests`. Instead, the gem **truncates the collections a test used**, after it: a few ms per collection whatever its size, and the definition and indexes stay. It knows what a test used from the models (every query and write of a model marks its collection).

### Minitest

```ruby
# test/test_helper.rb
ENV["RAILS_ENV"] ||= "test"
require_relative "../config/environment"
require "rails/test_help"
require "ndb/rails/minitest"
```

Every `ActiveSupport::TestCase` (models, controllers, integration, system tests) cleans after each test. To keep the data of a test case:

```ruby
class ImportTest < ActiveSupport::TestCase
  self.ndb_clean = false
end
```

### RSpec

```ruby
# spec/rails_helper.rb
require "ndb/rails/rspec"
```

Each example cleans after itself. `ndb_clean: false` on an example or a group keeps its data:

```ruby
it "imports the file", ndb_clean: false do
  # ...
end
```

### DatabaseCleaner

For a suite that already uses [DatabaseCleaner](https://github.com/DatabaseCleaner/database_cleaner) (2.x, the `database_cleaner-core` gem):

```ruby
require "ndb/rails/database_cleaner"

DatabaseCleaner[:ndb].strategy = :touched                     # the default: what the test used
DatabaseCleaner[:ndb].clean_with(:truncation)                 # every collection, before the suite
DatabaseCleaner[:ndb].strategy = :truncation, { except: %w[plans] }

# RSpec
config.before(:suite) { DatabaseCleaner[:ndb].clean_with(:truncation) }
config.around { |example| DatabaseCleaner[:ndb].cleaning { example.run } }
```

| Strategy | Cleans |
|---|---|
| `:touched` | the collections the tests used since the last clean (fast; the default) |
| `:truncation` | every engine collection of this worker, or `only:`; less `except:` |

Both truncate: ndb has no `:transaction` or `:deletion` strategy.

### Parallel tests

The daemon is shared by every worker, so each one gets collections of its own: the models read and write `test<n>_calls` instead of `calls`.

- **Rails `parallelize`:** `ndb/rails/minitest` sets it in `parallelize_setup`.
- **parallel_tests (`TEST_ENV_NUMBER`):** `ndb/rails/minitest` and `ndb/rails/rspec` read it.
- By hand: `Ndb::Testing.worker!(2)`, or `config.collection_prefix = "ci_"` in `Ndb::Rails.configure`.

The prefix covers the models (and their associations). A collection written with the raw client (`Ndb::Rails.client`) is not prefixed and not cleaned unless you pass it: `Ndb::Testing.clean!(only: %w[raw_events])`.

### The API

| Method | |
|---|---|
| `Ndb::Testing.clean!(only: nil, except: [])` | truncate what the tests used since the last clean (or only these) |
| `Ndb::Testing.clean_all!(except: [])` | truncate every engine collection of this worker (its prefix) |
| `Ndb::Testing.worker!(n)` | collections of their own for worker n (`test<n>_`) |
| `Ndb::Testing.touched` | the collections used since the last clean |

- **Safeguard:** a clean refuses to run outside the test environment (`Ndb::Testing::UnsafeClean`), so a test helper loaded by mistake against the development daemon deletes nothing. `NDB_ALLOW_CLEAN=1` forces it.
- Direct collections (`source`) and patterns are never cleaned: their data is the app files.
- **The test daemon:** run it with `pitr: off`. A truncate with PITR on keeps the old data in the trash for the window, so a long suite would fill the disk. A data dir in a temp dir (`-data-dir "$(mktemp -d)"`) starts each run empty. `-tcp ''` turns off the default TCP listener (`127.0.0.1:7070`), so the test daemon does not take the port of your local daemon.

```sh
ndb serve -config test/ndb.yml -data-dir "$(mktemp -d)" -unix /tmp/ndb-test.sock -tcp ''   # test/ndb.yml: pitr: off
```

### The tests of this gem

The tests of this gem build the daemon from the repository (`go build ./cmd/ndb`), start it on a short temp unix socket (macOS allows 104 bytes), wait for `GET /_health` to return 200, and stop it at exit. Run them with `bundle exec rake test` in `sdk/rails`; its Gemfile uses the gem `ndb` from `../ruby`.
