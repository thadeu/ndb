# Streams

A stream is an append-only log with consumer groups: the model of Redis
Streams, on disk. Apps append entries; workers in a group each take a part
of them, process them and ack them. An entry that a dead worker did not ack
stays pending, and another worker can claim it.

Use it as a durable work queue or event log that you can also search: a
stream is a collection, so `_search`, `_count` and `_aggregate` read it with
the [filters](query.md#filters).

The wire contract is on [Stream routes](http/streams.md); this page is how
to use them.

## Compared with Redis Streams

| | Redis Streams | ndb streams |
|---|---|---|
| Where the entries are | memory (RDB/AOF as a copy) | disk (WAL, then segments) |
| An acked append | `fsync` every 1 s by default | `fsync` before the answer (`strict`) |
| Group state (PEL) | memory | disk, before the answer |
| Entry | a flat map of strings | any JSON object |
| Read the log | by ID range | by ID range, and every filter of `_search` |
| Speed | RAM | a strict append costs one fsync (a few ms); `docs` batches share it |

Each Redis command has an ndb route; the table is in
[the map](#redis-command-map) below.

## A worker

```bash
# once: the group reads only new entries
curl --unix-socket /run/ndb/ndb.sock -X PUT http://ndb/orders/_streams/groups/workers -d '{"start":"$"}'

# an app appends
curl --unix-socket /run/ndb/ndb.sock http://ndb/orders/_streams/append -d '{"doc":{"order":42}}'
# {"_id":"1731163600000-0"}

# each worker, in a loop
curl --unix-socket /run/ndb/ndb.sock http://ndb/orders/_streams/groups/workers/read \
  -d '{"consumer":"w1","count":100,"block_ms":5000}'
# [{"_id":"1731163600000-0","_ts":"2026-11-09T15:00:00Z","order":42}]

curl --unix-socket /run/ndb/ndb.sock http://ndb/orders/_streams/groups/workers/ack \
  -d '{"ids":["1731163600000-0"]}'
```

The loop of a worker:

1. After a start or a restart, read its own pending entries first
   (`"pending": true`): the ones it took before and did not ack.
2. Then read new entries, with `block_ms` so that it waits when there are
   none.
3. Process each entry, then ack it.
4. From time to time, claim the entries that other workers left idle
   (`claim` with `min_idle_ms` and a `cursor`). An entry with many
   deliveries (`deliveries` in `pending`) is a poison message: append it to
   a dead-letter stream and ack it.

Delivery is **at least once**: after a crash or a claim, an entry can come
again. Make the work safe to repeat.

### The worker in the SDKs

Each SDK runs this loop for you: a handler that returns acks the entry, one
that fails leaves it pending. The details are on the page of each client:
[Go](clients/go.md#work), [Ruby](clients/ruby.md#group-worker),
[Rails](clients/rails.md#streams), [TypeScript](clients/typescript.md#work).

Go:

```go
orders, _ := ndb.StreamOf[Order](db, "orders")
g := orders.Group("billing")

g.Ensure(ctx, ndb.StartNew)

g.Work(ctx, "worker-1", func(ctx context.Context, e ndb.Entry[Order]) error {
	return charge(ctx, e.Doc)
}, ndb.Work{ClaimIdle: time.Minute, MaxDeliveries: 5, DeadLetter: "orders_dead"})
```

Ruby:

```ruby
group = client.stream("orders").group("billing")
group.ensure(Ndb::Stream::START_NEW)

worker = group.worker("worker-1", claim_idle: 60, max_deliveries: 5, dead_letter: "orders_dead") do |entry|
  charge(entry.doc)
end

worker.run # worker.stop from another thread ends it
```

Rails, with a class in `app/streams` (the gem `ndb-rails`). `bundle exec ndb
streams` runs the loop for every class:

```ruby
class BillingPaid
  include Ndb::Streams

  stream :orders, group: :billing, claim_idle: 1.minute, max_deliveries: 5

  def perform(event)
    charge(event.to_h)
  end
end
```

TypeScript:

```ts
const group = db.stream<Order>('orders').group('billing')

await group.ensure(startNew)

await group.work('worker-1', async (entry) => {
  await charge(entry.doc)
}, { claimIdleMs: 60_000, maxDeliveries: 5, deadLetter: 'orders_dead', signal })
```

## How the engine runs it

- **Append** is a normal write: the whole entry goes to the WAL (`fsync` in
  `strict`), then to the memtable, then to a segment when it is sealed.
  `docs` appends many entries with one fsync.
- **Read of new entries** is one range read: `_id >
  last_delivered_id`, in ID order, up to `count`. IDs grow with the insert
  order, so the next entries are together: at the end of the memtable for a
  group that keeps up, or in row groups that follow each other in a segment
  for a group that is late. The IDs and the entries come in the same pass.
- **Read of pending entries** is a batch get by id of the IDs in the PEL.
- **The cost is the PEL write, not the read.** Each read writes which
  entries went to which consumer, with one fsync, before the answer. Read in
  batches (`count`) so one fsync covers many entries; `no_ack` writes
  nothing (at most once).
- Streams do not use `_watch`: a worker pulls entries; `block_ms` is a
  request that waits until an append wakes it.

## Trim and retention

- `trim` with `max_len` or `min_id`, or `trim` in the append. `approx: true`
  deletes whole blocks of 1 024 entries only: with a trim on every append,
  it deletes once per block instead of once per append.
- `retention` of the stream deletes old entries by time (`_ts`), as for any
  collection.
- A trim does not change the groups: the PEL keeps the IDs of deleted
  entries, a pending read gives them with `"_deleted": true`, and `claim`
  gives them in `deleted` and removes them.

## Metrics

`/_metrics` has, for each stream used since the daemon started:

- `ndb_stream_entries_added_total{stream}`: every entry ever appended.
- `ndb_stream_group_pending{stream,group}`: entries delivered and not acked.
- `ndb_stream_group_lag{stream,group}`: entries not delivered to the group
  yet (left out when it cannot be known, after a trim in the part the group
  did not read).

## Redis command map

| Redis | ndb |
|---|---|
| `XADD s * f v` | `POST /s/_streams/append {"doc":{"f":"v"}}` |
| `XADD s MAXLEN ~ 1000 * ...` | the same with `"trim":{"max_len":1000,"approx":true}` |
| `XADD s NOMKSTREAM ...` | `"no_create": true` |
| `XTRIM s MAXLEN 1000` / `MINID id` | `POST /s/_streams/trim {"max_len":1000}` / `{"min_id":"id"}` |
| `XLEN s` | `POST /s/_count`, or `length` of `GET /s/_streams` |
| `XRANGE s a b COUNT n` | `POST /s/_search {"query":{"_id":{"$gte":"a","$lte":"b"}},"sort":{"_id":1},"limit":n}` |
| `XREVRANGE s b a` | the same with `"sort":{"_id":-1}` |
| `XDEL s id` | `DELETE /s/_doc/id` |
| `XREAD COUNT n BLOCK ms STREAMS s id` | `POST /s/_streams/read {"after":"id","count":n,"block_ms":ms}` |
| `XGROUP CREATE s g $ MKSTREAM` | `PUT /s/_streams/groups/g {"start":"$"}` |
| `XGROUP SETID s g id` | `POST /s/_streams/groups/g/setid {"id":"id"}` |
| `XGROUP DESTROY s g` | `DELETE /s/_streams/groups/g` |
| `XGROUP CREATECONSUMER s g c` | `PUT /s/_streams/groups/g/consumers/c` |
| `XGROUP DELCONSUMER s g c` | `DELETE /s/_streams/groups/g/consumers/c` |
| `XREADGROUP GROUP g c COUNT n BLOCK ms STREAMS s >` | `POST /s/_streams/groups/g/read {"consumer":"c","count":n,"block_ms":ms}` |
| `XREADGROUP ... STREAMS s 0` | the same with `"pending":true` |
| `XREADGROUP ... NOACK` | `"no_ack": true` |
| `XACK s g id ...` | `POST /s/_streams/groups/g/ack {"ids":[...]}` |
| `XPENDING s g` | `POST /s/_streams/groups/g/pending {}` |
| `XPENDING s g IDLE ms - + n c` | `... /pending {"idle_ms":ms,"start":"-","end":"+","count":n,"consumer":"c"}` |
| `XCLAIM s g c min-idle id ...` | `POST /s/_streams/groups/g/claim {"consumer":"c","min_idle_ms":ms,"ids":[...]}` |
| `XAUTOCLAIM s g c min-idle start COUNT n` | `... /claim {"consumer":"c","min_idle_ms":ms,"cursor":"start","count":n}` |
| `JUSTID` | `"just_ids": true` |
| `XINFO STREAM s` | `GET /s/_streams` |
| `XINFO GROUPS s` | `GET /s/_streams/groups` |
| `XINFO CONSUMERS s g` | `GET /s/_streams/groups/g/consumers` |

`XREAD`, with or without `BLOCK`, reads one stream for each request.

Not supported: a read of many streams in one request (`XREAD` /
`XREADGROUP` with many keys; send one request for each stream); `XSETID`;
the Redis 8.2 commands and options `XACKDEL`, `XDELEX`, `DELREF` and `ACKED`
(a trim keeps the PEL references, as `KEEPREF` and as up to Redis 8.0).

Each SDK also has these commands with the names and the shapes of its Redis
library: see [Move from Redis Streams](#move-from-redis-streams).

## Move from Redis Streams

An app that uses Redis Streams can change to ndb and keep its calls. Each
SDK has the stream commands of its Redis library, with the same arguments
and the same answers, on top of the routes of the
[map](#redis-command-map). The app picks its backend in one place, its
factory. So the two backends can run side by side, and the change is one
setting.

The adapters are code in the SDKs. The daemon does not speak the Redis
protocol (RESP): a Redis client cannot connect to ndb.

Ruby, with [redis-rb](clients/ruby.md#redis-streams-commands). The methods
are on `Ndb::Client`:

```ruby
# config/initializers/streams.rb
STREAMS = ENV["STREAMS_BACKEND"] == "ndb" ? Ndb.connect : Redis.new

STREAMS.xadd("orders", { order_id: 42 }, maxlen: 1000)
STREAMS.xreadgroup("billing", "w1", "orders", ">", count: 10, block: 5000)
```

Go, with [go-redis](clients/go.md#redis-streams-commands-ndbredis). The
module `ndbredis` returns go-redis types; the app writes an interface with
the commands it uses:

```go
type Streams interface {
	XAdd(ctx context.Context, a *redis.XAddArgs) *redis.StringCmd
	XReadGroup(ctx context.Context, a *redis.XReadGroupArgs) *redis.XStreamSliceCmd
	XAck(ctx context.Context, stream, group string, ids ...string) *redis.IntCmd
}

var streams Streams = rdb // a *redis.Client

if cfg.Backend == "ndb" {
	streams = ndbredis.New(db) // db is a *ndb.DB
}
```

TypeScript, with [ioredis](clients/typescript.md#ioredis):

```ts
import { connect } from '@thadeu/ndb'
import { ioredis } from '@thadeu/ndb/ioredis'
import { Redis } from 'ioredis'

const streams = process.env.STREAMS_BACKEND === 'ndb' ? ioredis(connect()) : new Redis()

await streams.xadd('orders', '*', 'order_id', '42')
```

TypeScript, with [node-redis](clients/typescript.md#node-redis):

```ts
import { connect } from '@thadeu/ndb'
import { nodeRedis } from '@thadeu/ndb/node-redis'
import { createClient } from 'redis'

const streams = process.env.STREAMS_BACKEND === 'ndb' ? nodeRedis(connect()) : await createClient().connect()

await streams.xAdd('orders', '*', { order_id: '42' })
```

What is different from Redis, in every language:

| | Redis | ndb, through the Redis commands |
|---|---|---|
| Values in an answer | text | text too. A value that ndb keeps as a number, a boolean or an object comes back as its text (`42` is `"42"`, an object is its JSON text). |
| Order of the fields of an entry | the order of the `XADD` | name order |
| Field names | any | a name that starts with `_` is reserved (`_id`, `_ts`) |
| Stream names | any key (`orders:eu`) | an ndb collection name: `a-z`, digits, `_` and `-`, at most 64 characters, the first one a letter or a digit |
| A read of many streams | one command | one request for each stream, one after the other |
| A blocked read (`BLOCK`) | many streams | one stream. With more than one, the call fails. |
| `MULTI` and pipelines | yes | no. Each call sends its requests at once. |
| Other Redis commands | yes | no. Only the stream commands (`X...`). |
| Errors | the error class of the library | the error type of the ndb SDK. The message starts as in Redis (`BUSYGROUP`, `NOGROUP`, ...). |
| Entries deleted while pending | the PEL keeps the ID (up to Redis 8.0) | the same |

Some differences are in one adapter only: what a write keeps (the Ruby and
Go adapters keep the JSON type of a value; the TypeScript adapters keep
text), the most entries of a read without a count, and the options that
are not supported. Each client page has its full list:
[Ruby](clients/ruby.md#differences-from-redis),
[Go](clients/go.md#differences-from-redis),
[TypeScript](clients/typescript.md#differences-from-redis).

After the move, an app can change to the native calls of the SDK (`append`,
`read`, the [worker](#the-worker-in-the-sdks)) one call at a time: the two
forms read and write the same streams.

## The contract between languages

A stream often has a writer in one language and consumers in others. The
entry is JSON, so the contract is the JSON object. Four rules keep it
stable:

- **Field names in snake_case.** `order_id`, not `orderId` or `OrderID`.
  Each SDK then maps the name to its own style (a Go `ndb:"order_id"` tag,
  a Ruby Hash key, a TypeScript type). A name cannot start with `_`.
- **JSON types only.** A string, a number, a boolean, `null`, an array or an
  object. A date is an RFC 3339 string (`"2026-11-09T15:00:00Z"`): JSON has
  no date type, and each consumer parses the string. Do not send a
  language-specific value (a Ruby Symbol as a value, a Go `time.Duration`
  as a number of nanoseconds). The time of the entry itself is in its ID
  and in `_ts`.
- **One group for each service.** A group gets every entry one time. The
  consumers of one group share the entries. So billing and mail each have
  their own group and each get all the entries. Two services in one group
  split the entries: each one gets only a part.
- **A version field `v`.** Each entry has `"v": 1`. A consumer reads `v`
  first and ignores (or sends to its dead letter) a version that it does
  not know. Add a field without a new version; change the meaning or the
  type of a field with a new version.

```json
{ "v": 1, "order_id": 42, "total": 99.9, "paid_at": "2026-11-09T15:00:00Z" }
```

- An entry that was written through the TypeScript Redis adapters has text
  values only (`"42"`, not `42`). When native consumers read the same
  stream, write with the native `append` or parse the text.
- ndb does not check the contract: a stream takes any JSON object.

## Backup and PITR

- A [backup](operations.md) keeps the groups: their position and their
  pending entries, as they were when the backup ran. A pending entry that
  points to an entry appended after the backup's WAL mark comes back as a
  deleted entry (`claim` drops it).
- A PITR restore gives the entries as they were at the instant, not the
  groups: the PEL has no history of its changes. Make the groups again on
  the restored stream, with `start` at the last ID each one processed.
