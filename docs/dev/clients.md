# Client guides

Every SDK has two layers: a small transport (HTTP over a unix socket or TCP) and an idiomatic model layer that declares the collection (`PUT /v1/c/{coll}`) on first use and builds query JSON. The daemon decides what a query means. See [query.md](query.md).

Sources are in `sdk/`: `sdk/go`, `sdk/ruby` (gem `ndb`), `sdk/rails` (gem `ndb-rails`), `sdk/ts`. Version 0.1.0 is prepared for release; until the registries have it, use a path or Git source.

| SDK | Install |
|---|---|
| Go | `go get github.com/thadeu/ndb/sdk/go@v0.1.0` (the module is tagged `sdk/go/v0.1.0`) |
| Ruby (plain client, no dependencies) | `gem "ndb"` |
| Rails (ActiveModel layer) | `gem "ndb-rails"` (installs `ndb`) |
| TypeScript | `pnpm add @thadeu/ndb` (the name `ndb` is taken on npm) |

Addresses are `unix:///run/ndb/ndb.sock` or `tcp://host:port`. All three SDKs speak both. Without an address, each SDK reads `NDB_URL`.

When the daemon runs with `-token-file`, the TCP listener needs a bearer token (the unix socket never does). Each SDK takes a `token` option, or reads `NDB_TOKEN`. The token goes only in the `Authorization` header. A wrong or missing token is `401 unauthorized`. See [configuration.md](configuration.md#authentication).

| SDK | Address | Token |
|---|---|---|
| Ruby | `Ndb::Client.new(url:)` or `socket:` | `Ndb::Client.new(token:)` |
| Rails | `url:` or `socket:` in `config/ndb.yml` | `token:` in the same place |
| Go | `ndb.Open(addr)`; empty `addr` reads `NDB_URL` | `ndb.Open(addr, ndb.WithToken(t))` |
| TypeScript | `connect(addr)`; no `addr` reads `NDB_URL` | `connect(addr, { token })` |

## Ruby (gem `ndb`)

The plain client. Stdlib only, no runtime dependency. Use it in a script, a worker or any Ruby project. Full guide: [sdk/ruby/README.md](https://github.com/thadeu/ndb/blob/main/sdk/ruby/README.md).

```ruby
require "ndb"

ndb = Ndb::Client.new(url: "tcp://ndb.internal:7070", token: ENV["NDB_TOKEN"])   # or socket: "/run/ndb/ndb.sock"; no args reads NDB_URL and NDB_TOKEN

ndb.ensure_collection("calls", id: "call_id", indexes: ["status"])
ndb.insert("calls", [{ call_id: "c1", status: "answered" }])
ndb.update("calls", "c1", set: { status: "done" }, if_version: 1)   # a stale write raises Ndb::VersionConflict

page = ndb.find("calls", where: { status: "done" }, sort: { call_id: 1 }, limit: 50, count: true)
page.rows; page.total; page.next_cursor

ndb.watch("calls", "sweeper", where: { status: "failed" }) do |ev|
  process(ev["doc"])

  ndb.ack("calls", "sweeper", ev["pos"])
end
```

Errors are `Ndb::Error` subclasses with `code` and `status`: `NotFound`, `VersionConflict`, `UniqueViolation`, `CatalogConflict`, `ReadOnlyCollection`, `InvalidQuery`, `InvalidCursor`, `ConnectionError`, `NotReady`, `Unauthorized`, `WatchLagged`, `HistoryTruncated`.

## Rails (gem `ndb-rails`)

The ActiveModel layer. It depends on the gem `ndb` and uses `Ndb::Client` inside (`Ndb::Rails.client`). Gemfile, from a checkout (the gem `ndb` is in the same repository):

```ruby
gem "ndb", path: "vendor/ndb/sdk/ruby"
gem "ndb-rails", path: "vendor/ndb/sdk/rails"
```

`config/ndb.yml`, one block per environment:

```yaml
production:
  url: <%= ENV.fetch("NDB_URL", "unix:///run/ndb/ndb.sock") %>
  token: <%= ENV["NDB_TOKEN"] %>
  timeout: 30
  pool_size: 16
development:
  socket: /tmp/ndb.sock
```

`url:` is `unix://...` or `tcp://host:port`. `socket:` is the short form for a unix socket path. Keys left out fall back to `NDB_URL` and `NDB_TOKEN`, then to the default socket.

Models:

```ruby
class ApplicationRecord < Ndb::Record
  self.abstract_class = true
end

class Call < ApplicationRecord
  primary_key :call_id

  attribute :status, :string, index: true
  attribute :dnis, :string, index: true
  attribute :duration, :integer
  attribute :ts, :datetime, time: true

  validates :status, presence: true
end
```

The collection name follows Rails naming (`Call` becomes `calls`); `self.collection_name = "x"` changes it. `ttl 24.hours` and `attribute :x, expires: true` set expiry. `source "glob"` declares a direct-mode collection. The railtie declares every model's collection on boot when `eager_load` is on (production), and on first use otherwise.

```ruby
Call.create!(call_id: "c1", status: "answered", duration: 42, ts: Time.current)

call = Call.find("c1")
call.update!(status: "done")          # sends if_version; a stale write raises Ndb::StaleObjectError
call.destroy

Call.where(status: "answered", ts: 1.day.ago..).order(ts: :desc).limit(50).to_a
Call.where(dnis: %w[1133 1144]).count
Call.where.not(status: "failed").page(3).per(50)
Call.group(:status).count
Call.group(:status).average(:duration)

Call.where(status: "answered").limit(100).find_each { |c| puts c.call_id }   # cursor pagination
```

Regular expressions: a Ruby `Regexp` works in `where` values (`Call.where(caller: /^55/i)`, `where.not`, `or`, `watch where:`). It is sent as `{"$regex": source, "$options": "m" + i/s/x}`: `m` is always set (Ruby `^`/`$` are line anchors), `/i` is `i`, `/m` is `s`, `/x` is `x`. Look-around, backreferences, `(?>`, possessive quantifiers, `\h`, `\G`, `\R` and `\X` raise `Ndb::InvalidQuery` before any request (RE2 has none of them). A String pattern, `{"$regex" => "^abc"}`, is sent unchanged, and a literal `^prefix` without `i`/`m` uses the index. A `Regexp` always carries `m`, so it is a scan filter. See [query.md](query.md#regular-expressions).

Errors are the classes of the gem `ndb` (`Ndb::Error` and its subclasses). The gem `ndb-rails` adds the ActiveRecord-like aliases `Ndb::StaleObjectError` (is `Ndb::VersionConflict`) and `Ndb::RecordNotUnique` (is `Ndb::UniqueViolation`). They have `code` and `status`: `NotFound`, `VersionConflict`, `UniqueViolation`, `CatalogConflict`, `ReadOnlyCollection`, `InvalidQuery`, `InvalidCursor`, `ConnectionError`, `Unauthorized`, `WatchLagged`, `HistoryTruncated`. `RecordInvalid` and `RecordNotSaved` belong to `ndb-rails`.

The Ruby SDK has no model-level `watch`. Use the client, which streams events for a named consumer:

```ruby
client = Ndb::Rails.client

client.watch("calls", "sweeper", where: { status: "failed" }) do |ev|
  process(ev["doc"])

  client.ack("calls", "sweeper", ev["pos"])
end
```

Without a block, `watch` returns an Enumerator.

## Go (module `github.com/thadeu/ndb/sdk/go`)

Stdlib only, its own `go.mod`.

```go
type Call struct {
	CallID   string    `ndb:"call_id,id"`
	Status   string    `ndb:"status,index"`
	DNIS     string    `ndb:"dnis,index"`
	Duration int       `ndb:"duration"`
	TS       time.Time `ndb:"ts,time"`
}

db, err := ndb.Open("unix:///run/ndb/ndb.sock")
if err != nil {
	return err
}

defer db.Close()

calls, err := ndb.Collection[Call](ctx, db, "calls") // declares the collection

_, err = calls.Insert(ctx, Call{CallID: "c1", Status: "answered", TS: time.Now().UTC()})

rows, err := calls.Where(ndb.Eq("status", "answered")).Sort(ndb.Desc("ts")).Limit(10).All(ctx)

n, err := calls.Where(ndb.In("dnis", "1133", "1144")).Count(ctx)

v, err := calls.Update(ctx, "c1", map[string]any{"status": "done"}, ndb.IfVersion(1))
if errors.Is(err, ndb.ErrVersionConflict) {
	// reload and retry
}

err = calls.Delete(ctx, "c1")
```

Filters: `Eq Ne Gt Gte Lt Lte In Exists Or Not`. Query methods: `Where Sort Limit Skip Page Fields After All First Count Iter NextCursor`. Aggregate with `calls.Aggregate(ctx, ndb.Pipeline{...})` or `ndb.AggregateAs[R](...)` for typed rows.

Outbox consumer. `ndb.WatchWhere(...)` delivers only the events whose document matches:

```go
for ev, err := range calls.Watch(ctx, "sweeper", ndb.WatchWhere(ndb.Eq("status", "failed"))) {
	if err != nil {
		break // call Watch again: it resumes after the last Ack
	}

	process(ev.Doc)

	if err := calls.Ack(ctx, ev); err != nil {
		return err
	}
}
```

`ndb.ErrHistoryTruncated` means the consumer position was removed by `wal_retention`. A runnable example is in `sdk/go/examples/basic`.

## TypeScript (package `@thadeu/ndb`)

Node 24 or newer, ESM and CJS. Built with `pnpm build` in `sdk/ts`.

```ts
import { connect } from '@thadeu/ndb'

const ndb = connect('unix:///run/ndb/ndb.sock')

const calls = ndb.model('calls', {
  id: 'call_id',
  time: 'ts',
  fields: {
    call_id: { type: 'string' },
    status: { type: 'string', index: true },
    duration: { type: 'number', optional: true },
    ts: { type: 'date' },
  },
})

await calls.insert({ call_id: 'c1', status: 'answered', duration: 42, ts: new Date() })

const rows = await calls.where({ status: 'answered' }).sort({ ts: -1 }).limit(10)

const row = await calls.find('c1')
await calls.update('c1', { status: 'done' }, { ifVersion: row?._v })

const byStatus = await calls.aggregate([{ $group: { _id: '$status', n: { $count: {} } } }])

for await (const ev of calls.watch('sweeper', { where: { status: 'failed' } })) {
  await handle(ev.doc)
  await calls.ack(ev)
}

await ndb.close()
```

Errors are `NdbError` subclasses with `code` and `status` (`NotFoundError`, `VersionConflictError`, `DuplicateIdError`, `UniqueViolationError`, `InvalidQueryError`, `CursorError`, `WatchLaggedError`, `HistoryTruncatedError`, `UnauthorizedError`). A runnable example is `sdk/ts/examples/basic.mjs`.

## Retry while the daemon starts

The daemon opens its socket before recovery ends and answers every request with HTTP 503 and the code `not_ready` ("ndb is recovering"; `/health` answers 503 `{"status":"recovering"}`, which the SDKs also read as `not_ready`). The socket is also missing or refuses connections while the daemon restarts. The Go, Ruby and TypeScript SDKs retry these cases by default:

- Backoff: start 50 ms, factor 2, cap 1 s, with jitter. Total deadline 10 s.
- When the deadline passes, the last error surfaces unchanged (`ErrNotReady` / `NotReady` / `NdbError` with code `not_ready`, or the connect error).
- Set the deadline with `WithReadyTimeout(d)` (Go), `ready_timeout:` in `Ndb::Client.new`, `config.ready_timeout` or `ready_timeout` in `config/ndb.yml` (Ruby, seconds), `{ readyTimeout }` in `connect()` (TypeScript, ms). `0` turns the retry off.
- Writes are safe: a write is retried only when it certainly was not processed (a failed connect, or a 503 `not_ready` answer). A write whose connection dropped after the request was sent is never retried.
- Reads (`find`, `count`, `aggregate`, `get`, `stats`, and `GET`) are idempotent, so they are also retried when the connection drops before an answer.
- A watch stream is retried only for the initial open, never mid-stream.
- The caller's context (Go) or `AbortSignal` (TypeScript) stops the wait at once. Ruby has no cancellation: the deadline bounds the wait. A timeout error is never retried.

## Direct mode from an app

An app in any language can skip the SDK for writes: declare the collection in `ndb.yml` with `write: direct` and append one JSON line per event to its own file. Use the SDK (or `curl`) to read. See [storage.md](storage.md#write-modes).

For a deployment with several apps and languages, see [deployment.md](deployment.md).
