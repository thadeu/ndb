# Client guides

Every SDK has two layers: a small transport (HTTP over a unix socket or TCP) and an idiomatic model layer that declares the collection (`PUT /v1/c/{coll}`) on first use and builds query JSON. The daemon decides what a query means. See [query.md](query.md).

Sources are in `sdk/`: `sdk/go`, `sdk/ruby`, `sdk/ts`. They are not published to a package registry yet (that is milestone E13.2), so use a path or Git source for now.

Addresses are `unix:///run/ndb/ndb.sock` or `tcp://host:port`. All three SDKs speak both. Without an address, each SDK reads `NDB_URL`.

When the daemon runs with `-token-file`, the TCP listener needs a bearer token (the unix socket never does). Each SDK takes a `token` option, or reads `NDB_TOKEN`. The token goes only in the `Authorization` header. A wrong or missing token is `401 unauthorized`. See [configuration.md](configuration.md#authentication).

| SDK | Address | Token |
|---|---|---|
| Ruby | `url:` or `socket:` in `config/ndb.yml`, or `NdbRecord::Client.new(url:)` | `token:` in the same place |
| Go | `ndb.Open(addr)`; empty `addr` reads `NDB_URL` | `ndb.Open(addr, ndb.WithToken(t))` |
| TypeScript | `connect(addr)`; no `addr` reads `NDB_URL` | `connect(addr, { token })` |

## Rails (gem `ndb_record`)

Gemfile, from a checkout:

```ruby
gem "ndb_record", path: "vendor/ndb/sdk/ruby"
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
class ApplicationNdbRecord < NdbRecord::Base
  self.abstract_class = true
end

class Call < ApplicationNdbRecord
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
call.update!(status: "done")          # sends if_version; a stale write raises NdbRecord::StaleObjectError
call.destroy

Call.where(status: "answered", ts: 1.day.ago..).order(ts: :desc).limit(50).to_a
Call.where(dnis: %w[1133 1144]).count
Call.where.not(status: "failed").page(3).per(50)
Call.group(:status).count
Call.group(:status).average(:duration)

Call.where(status: "answered").limit(100).find_each { |c| puts c.call_id }   # cursor pagination
```

Errors are `NdbRecord::Error` subclasses with `code` and `status`: `NotFound`, `StaleObjectError`, `RecordNotUnique`, `CatalogConflict`, `ReadOnlyCollection`, `InvalidQuery`, `InvalidCursor`, `ConnectionError`, `Unauthorized`, `WatchLagged`, `HistoryTruncated`.

The Ruby SDK has no model-level `watch`. Use the client, which streams events for a named consumer:

```ruby
client = NdbRecord.client

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

## TypeScript (package `ndb`)

Node 24 or newer, ESM and CJS. Built with `pnpm build` in `sdk/ts`.

```ts
import { connect } from 'ndb'

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

## Direct mode from an app

An app in any language can skip the SDK for writes: declare the collection in `ndb.yml` with `write: direct` and append one JSON line per event to its own file. Use the SDK (or `curl`) to read. See [storage.md](storage.md#write-modes).

For a deployment with several apps and languages, see [deployment.md](deployment.md).
