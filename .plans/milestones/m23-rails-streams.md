# M23: Streams in Rails (Ndb::Streams) and the Redis commands

Goal: streams built into the `ndb-rails` gem, like workers. One class in
`app/streams` appends, consumes, or does both. The consumers run with one
command, `bundle exec ndb streams`. Each SDK also gets the stream commands of its Redis library (redis-rb,
go-redis, ioredis, node-redis), so a move from Redis Streams is a small
change.

Out of scope: many streams in one class, typed fields (`attribute`), an outbox on ActiveRecord models (`streams_to`), generators,
an ActiveJob adapter, an Action Cable adapter, the Redis protocol (RESP) on
the wire.

## Decisions

- Name: `include Ndb::Streams`, a module defined by `ndb-rails`. There is
  no `Base` class: the `included` hook registers the class and the
  `inherited` hook gives the config to its subclasses, so the app can write
  its own `ApplicationStream` with the include. `Ndb::Stream` (singular) is
  the low-level client of the `ndb` gem; the `ndb` gem never defines
  `Ndb::Streams`.
- Files: `app/streams`. A class name needs no suffix.
- Config: all in `stream :name, **options`. Other DSL: only `around_perform`.
- The minimum:
  - writer: `include Ndb::Streams`. The stream name comes from the class
    name (`OrderEvent` is `order_events`); `stream :orders` sets it.
  - consumer: `include Ndb::Streams`, `stream :orders, group: :billing` and
    `perform(event)`.
  - every option has a default: `max_len` and `retain` (none: the stream
    keeps everything), `concurrency` 1, `max_deliveries` (none: no dead
    letter), `claim_idle` 1 min, `start` new entries, `discard_on` none.
- Role: a class without `perform` is a writer. A class with `perform` is a
  consumer, and `group:` is required (no `group:` is an error at load).
  Two classes with the same group on one stream are an error at load.
- One stream and one group for each class. Many streams in one class is
  out of scope (shared code goes in a module).
- The event that `perform` gets: `event[:key]` (one field, by symbol or
  string), `event.to_h` (the payload, without `_id` and `_ts`), `event.id`,
  `event.time` (a Time, from the ID), `event.deliveries`. No `event.doc`,
  no `event.type`.
- No typed fields in this version (`attribute` is out of scope): the
  payload is JSON and comes as it is. A date is a String.
- Payload: a Hash, an Array (many entries) or a JSON string.
- Write: `append`, with the aliases `enqueue` and `perform_async`. In an
  ActiveRecord transaction, the append waits for the commit.
- Read: `last`, `find`, `range`, `count` on every class.
- Group start: new entries only (`$`) when the group is made; `start: :all`
  reads from the start. Any process makes the missing groups at boot, in a
  background thread.
- Consumer name: stable (`NDB_CONSUMER`, or hostname + thread index), so a
  restart reads its own pending entries first.
- Failure: an error in `perform` leaves the entry pending; it comes again
  after `claim_idle`. `discard_on` acks without a retry. After
  `max_deliveries`, the entry goes to `<stream>_dead` and is acked.
- Run: `bundle exec ndb streams`, an executable of the `ndb` gem. With
  Rails, it loads the app and `eager_load_dir("app/streams")`. Without
  Rails, `-r file` loads the classes. Any other command (`serve`, ...) is
  passed to the daemon binary when it is installed.
- Puma: `plugin :ndb` is optional. It starts the same command next to Puma
  and stops it with Puma.
- `config/ndb.yml` has options only (`streams: concurrency, only`). The
  command says what the process does.

```ruby
class OrderEvent            # writer, the minimum
  include Ndb::Streams
end

OrderEvent.append(order_id: 42)

class BillingPaid           # consumer, the minimum
  include Ndb::Streams

  stream :order_events, group: :billing

  def perform(event) = Invoice.create!(order_id: event[:order_id])
end
```

## E23.1 Core (ndb-rails) — done

`Ndb::Streams` (`included` and `inherited`), `stream` with its options and
defaults, the registry of classes, the errors for a missing and a repeated group.

## E23.2 Write and read (ndb-rails) — done

`append` with `enqueue` and `perform_async`, the wait for the commit,
`last`, `find`, `range`, `count`, the `event` object (`[]`, `to_h`, `id`, `time`,
`deliveries`).

## E23.3 Runner (ndb-rails) — done

Threads for each group on `Ndb::Worker` of the client, the stable consumer
name, the Rails executor around each `perform`, `around_perform`,
`discard_on`, the dead letter, the stop on SIGTERM.

## E23.4 Process (ndb and ndb-rails) — done

- `ndb` gem: the `ndb` executable with `streams`, and the pass-through of
  other commands to the daemon binary.
- `ndb-rails`: the load of the app and of `app/streams`, the Puma plugin,
  the groups made at boot, `ActiveSupport::Notifications` events.

## E23.5 Tests (ndb-rails) — done

`Ndb::Streams.inline!` (an append calls each `perform` at once),
`assert_streamed`, the clean between tests (Minitest and RSpec).

## E23.6 Redis commands in Ruby (ndb gem) — done

First, for the three languages: check `XREAD` with `BLOCK` (a read without
a group that waits for new entries). If the daemon has no route for it,
add one (`POST /{s}/_streams/read`), with its spec cases and docs.

The `x*` methods of redis-rb on `Ndb::Client` (module
`Ndb::RedisCommands`), on top of ndb streams. An app that uses Redis
Streams picks the backend in one place (its factory) and keeps its calls.

```ruby
# config/initializers/streams.rb: the factory of the app
STREAMS = ENV["STREAMS_BACKEND"] == "ndb" ? Ndb.connect : Redis.new

STREAMS.xadd("orders", { order_id: 42 }, maxlen: 1000)
STREAMS.xgroup(:create, "orders", "billing", "$", mkstream: true)
STREAMS.xreadgroup("billing", "w1", "orders", ">", count: 10, block: 5000)
STREAMS.xack("orders", "billing", id)
```

- Methods: `xadd`, `xlen`, `xrange`, `xrevrange`, `xread`, `xdel`, `xtrim`,
  `xgroup` (create, setid, destroy, createconsumer, delconsumer),
  `xreadgroup`, `xack`, `xpending`, `xclaim`, `xautoclaim`, `xinfo`
  (stream, groups, consumers).
- Arguments and answers have the shapes of redis-rb.
- No new class and no new dependency.
- What is different from Redis is written in the docs: nested values are
  kept as JSON; values come back as strings, as Redis gives them.
- Tests: the cases of `spec/streams`, through these methods.

## E23.7 Redis commands in Go (sdk/go/ndbredis) — done

A module of its own, with its own `go.mod`: the methods return go-redis v9
types (`*redis.StringCmd`, `*redis.XStreamSliceCmd`, ...), so it imports
go-redis. The main SDK stays without dependencies.

```go
type Streams interface { // the interface of the app
	XAdd(ctx context.Context, a *redis.XAddArgs) *redis.StringCmd
	XReadGroup(ctx context.Context, a *redis.XReadGroupArgs) *redis.XStreamSliceCmd
	XAck(ctx context.Context, stream, group string, ids ...string) *redis.IntCmd
}

var streams Streams = rdb

if cfg.Backend == "ndb" {
	streams = ndbredis.New(db)
}
```

- Methods: the stream methods of `redis.Cmdable` (`XAdd`, `XLen`, `XRange`,
  `XRevRange`, `XRead`, `XDel`, `XTrim*`, `XGroup*`, `XReadGroup`, `XAck`,
  `XPending`, `XPendingExt`, `XClaim`, `XClaimJustID`, `XAutoClaim`,
  `XAutoClaimJustID`, `XInfo*`).
- A compile-time check that `*ndbredis.Client` and `*redis.Client` satisfy
  one interface with these methods.
- Tests: each case runs on the adapter and on a real Redis (when
  `NDB_REDIS_ADDR` is set), with the same assertions.
- CI: a step for the new module; its release tag (`sdk/go/ndbredis/vX`).

## E23.8 Redis commands in TypeScript (sdk/ts) — done

Two subpaths, because the two libraries have different shapes. No
dependency: the types are structural.

```ts
import { ioredis } from 'ndb/ioredis'

const streams = env.STREAMS_BACKEND === 'ndb' ? ioredis(db) : new Redis()

await streams.xadd('orders', '*', 'order_id', '42')
```

- `ndb/ioredis`: `xadd`, `xreadgroup`, ... with variadic arguments and
  array answers.
- `ndb/node-redis`: `xAdd`, `xReadGroup`, ... with object arguments and
  object answers.
- Type tests: each adapter is assignable to the stream part of the type of
  its library (the libraries are dev dependencies only).
- Tests on the daemon, the same cases as E23.6.

## E23.9 Docs — done

- A Streams section in `docs/dev/clients/rails.md`, one `###` for each
  method.
- The Redis commands in `docs/dev/clients/ruby.md`, `go.md` and
  `typescript.md`, and in the Redis command map of `docs/dev/streams.md`.
- "Move from Redis Streams" in `docs/dev/streams.md`: the factory in each
  language, and what is different from Redis.
- "The contract between languages" in `docs/dev/streams.md`: field names
  in snake_case, JSON types only, one group for each service, a version
  field.
- README of each gem; CHANGELOG.
