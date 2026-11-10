# @thadeu/ndb

<img src="https://raw.githubusercontent.com/thadeu/ndb/main/brand/png/lockup-on-light.png" width="220">

TypeScript client for [ndb](https://github.com/thadeu/ndb), an NDJSON query daemon. Typed collections, a chainable query builder, aggregate, watch and blobs. The client builds JSON. The daemon decides what every query means.

Node 24 or newer, or Bun. ESM and CJS. One runtime dependency (`undici`, used on Node; Bun uses its `fetch`).

Docs: [client guide](https://github.com/thadeu/ndb/blob/main/docs/dev/clients.md#typescript-package-thadeundb), [query language](https://github.com/thadeu/ndb/blob/main/docs/dev/query.md), [HTTP API](https://github.com/thadeu/ndb/blob/main/docs/dev/http.md).

## Install

```sh
pnpm add @thadeu/ndb
# or
npm install @thadeu/ndb
bun add @thadeu/ndb
```

You also need a running ndb daemon. See the repository for how to run one.

## Connect

```ts
import { connect } from '@thadeu/ndb'

// Unix socket.
const ndb = connect('unix:///run/ndb/ndb.sock')

// TCP with a bearer token.
const remote = connect('tcp://10.0.0.5:7070', { token: 'secret' })

// Address and token from the environment: NDB_URL and NDB_TOKEN.
const fromEnv = connect()

// Retry for up to 5 s while the daemon starts or recovers.
const patient = connect('unix:///run/ndb/ndb.sock', { readyTimeout: 5000 })

await ndb.health() // resolves when the daemon is ready (GET /_health)
await ndb.close()
```

- `connect` does not connect. The first call does.
- The address is `unix:///path` or `tcp://host:port`.
- The token goes only in the `Authorization` header. A unix socket does not need it.
- `readyTimeout` is in milliseconds. It sets how long a call retries when the daemon is not ready (`not_ready`, or the socket is missing or refuses). Default 10000. Zero turns the retry off. Writes are retried only when the daemon certainly did not process them. Reads are also retried after a dropped connection.
- The pool has up to 16 connections. There is no setting for it.
- `new Ndb(addr, opts)` does the same as `connect`.

## Define a collection

```ts
const calls = ndb.collection('calls', {
  time: 'at',
  fields: {
    status: { type: 'string', index: true },
    caller: { type: 'string', unique: true },
    duration: { type: 'number', optional: true },
    at: { type: 'date' },
    meta: { type: 'json', optional: true },
  },
})
```

- Field types: `string`, `number`, `boolean`, `date` (a `Date`), `json` (any value).
- Field options: `index`, `unique`, `optional`.
- `_id` is the primary key of every document. It is not a schema field: leave it out on insert and the daemon makes a UUIDv7 (or use `newId()`). For a natural key (email, SKU), keep `_id` and mark that field `unique`.
- `time` is the collection time field. Used by zone maps and `$dateTrunc`.
- The collection is declared on first use (`PUT /calls`). You can call `await calls.ready()` to do it at once.
- A name that ends with `*` is a collection pattern: `ndb.collection('logs-2026-*', schema)` reads every collection whose name starts with `logs-2026-`. It declares nothing, and a write throws with code `pattern_read_only`. Each row has `_coll`, the name of its collection.
- Types come from the schema. Rows have the schema fields, `_id` and `_v`, the document version. A `date` field is a `Date` in rows. Input takes `Date` values and ISO strings in filters.
- `expires` is the `date` field that holds when a document expires. It turns on `expireIn`, `expire`, `persist` and `ttl` (below).
- The client has no `ttl` catalog option. Use the HTTP API for it.

## Insert, get, update, delete

```ts
// One document (POST /calls/_doc): { _id, _v }.
const one = await calls.insert({ _id: 'c1', status: 'answered', caller: 'a', duration: 30, at: new Date() })

// Many in one transaction, all or nothing (POST /calls/_bulk): an array.
const results = await calls.insert([
  { _id: 'c2', status: 'failed', caller: 'b', at: new Date() },
  { _id: 'c3', status: 'failed', caller: 'c', at: new Date() },
])
// results: [{ _id: 'c2', _v: 1 }, { _id: 'c3', _v: 1 }]

// Upsert: create by _id, or merge the fields into the stored document (the
// others stay), like doc_as_upsert in Elasticsearch. One transaction.
await calls.upsert({ _id: 'c2', status: 'answered' })
await calls.upsertAll([{ _id: 'c2', status: 'answered' }, { _id: 'c4', status: 'new', at: new Date() }])

// Get by _id (GET /calls/_doc/c1). Returns null when missing.
const row = await calls.get('c1')

// Atomic update by _id (POST /calls/_update/c1). guard adds conditions on
// the same document: a _v guard is an optimistic lock.
const res = await calls.update('c1', { $set: { status: 'done' }, $unset: { duration: true } }, { guard: { _v: row!._v } })
// res: { matched, modified, doc } (doc is the new row with _v; absent for many)

// A guarded decrement: exactly one of two racing calls wins.
await stock.update('a', { $inc: { qty: -1 } }, { guard: { qty: { $gte: 1 } } })

// Create the document with this _id when it is missing.
await calls.update('c9', { $inc: { duration: 1 } }, { upsert: true })

// Atomic update by query (POST /calls/_update_by_query): the first match, or
// every match with many (the query needs _id or an indexed field).
await calls.updateBy({ status: 'failed' }, { $set: { status: 'retry' } }, { many: true })

// Delete by _id (DELETE /calls/_doc/c2), or by query: { deleted }.
await calls.delete('c2')
await calls.deleteBy({ status: 'failed' }, { many: true })
```

- Operators: `$set`, `$setOnInsert` (like `$set`, but only when an upsert creates the document), `$unset`, `$inc`, `$push` (with `{ $each: [...] }`), `$pull`, `$min`, `$max`. Field names can be dot paths.
- Nothing matched is not an error: `matched` is 0 (for example when `_v` is stale, so read again and retry). `modified` is 0 when the operators leave the document as it is.
- `update` takes `guard`, `upsert` (creates the document with that `_id`) and `expireIn`. `updateBy` takes `many`, `upsert` (creates the document from the equality fields of the query) and `expireIn`. `deleteBy` takes `many`.
- `many` needs `_id` or an indexed field in the query. An empty query is refused (`empty_filter`).
- A key of the operators without `$` throws `InvalidUpdateError` before anything is sent. The daemon contract errors (`unknown_operator`, `no_update`, `conflicting_update`, `invalid_field`, `unindexed_many`, `empty_filter`) are `InvalidUpdateError` too.

An insert with a duplicate `_id` throws `DuplicateIdError` (a `_bulk` insert then writes nothing). A duplicate `unique` value throws `UniqueViolationError`. An empty or non-string `_id` fails with code `invalid_id`.

### Truncate and drop

```ts
await calls.truncate() // every document goes; the definition and indexes stay
await calls.drop()     // the collection, its definition and its watch consumers go
```

- Both take the same time for 10 or 10 million documents. Watch consumers get one event with `op: 'truncate'` (`_id` is `''`), then the new changes.
- After `drop()`, the next call on the collection declares it again. A collection declared in `ndb.yml` comes back, empty, when the daemon restarts.

### Expiry (TTL like Redis)

```ts
const sessions = ndb.collection('sessions', {
  expires: 'exp',
  fields: { exp: { type: 'date', optional: true } },
})

await sessions.insert({ _id: 's1' }, { expireIn: '1h' }) // or 3600; all docs of the call
await sessions.update('s1', { $set: { user: 'u' } }, { expireIn: '30m' })

const { _v, expiresAt } = await sessions.expire('s1', '30m') // EXPIRE
await sessions.expireAt('s1', new Date(Date.now() + 3_600_000))
await sessions.ttl('s1') // seconds left, -1 = never expires, null = missing or expired
await sessions.persist('s1') // PERSIST
```

- A duration is seconds or a string such as `"90m"`, `"1h30m"`, `"1d"`. `expireIn` wins over a value of the `expires` field in the same call.
- `expire` and `persist` throw `NotFoundError` for a missing or expired document. `TtlError` (`ttl_not_enabled`, `ttl_derived`) means the schema has no `expires` field, or the expiry comes from `ttl` + `time`.
- Disk space of expired documents returns after compaction, and fully only with `wal_retention`; see [Freeing disk](https://github.com/thadeu/ndb/blob/main/docs/dev/configuration.md#freeing-disk).

## Query

A query is lazy and immutable (`POST /{coll}/_search`). It is also thenable: `await` runs it.

```ts
const rows = await calls
  .query({ status: 'answered', duration: { $gte: 30 } })
  .query({ caller: { $in: ['a', 'b'] } })
  .not({ caller: 'zzz' })
  .or({ duration: { $lt: 5 } }, { 'meta.agent.id': 7 })
  .sort({ at: -1, _id: 1 })
  .fields('_id', 'status')
  .limit(50)
  .skip(100)
```

- Operators: `$eq`, `$ne`, `$gt`, `$gte`, `$lt`, `$lte`, `$in`, `$nin`, `$exists`. Several `query` calls are ANDed. Dot paths work on `json` fields.
- `$regex` (with `$options` `i`, `m`, `s`, `x`) uses Go RE2: no backreferences, no look-around. Pass a string, or a `RegExp` (`{ name: /^al/i }` or `{ name: { $regex: /^al/i } }`). Only the `i`, `m` and `s` flags are accepted; any other flag (`g`, `y`, `u`, ...) throws `InvalidQueryError` before the request. An anchored prefix such as `^abc` can use an index.
- The order of the `sort` keys is the order of the sort. The id is always the last key.
- `fields(...)` returns only these fields. The rows are `Partial`.
- `page(n, size)` sets `skip` and `limit` for page `n` (from 1).
- `first()` returns the first row, or `null`.
- The collection has the same shortcuts: `calls.query(...)`, `calls.sort(...)`, `calls.all()`, `calls.first()`, `calls.count(...)`, `calls.each()`.

### A query built elsewhere

A query from a search form or a saved search is not type-checked. `rawQuery` sends it as is (the daemon validates it), ANDed with the other clauses:

```ts
const rows = await calls.rawQuery({ status: 'answered', at: { $gte: 'now-7d' } }).limit(50)
```

For a whole search body (`query`, `sort`, `limit`, `after`, `count`), use `search`. It answers one page:

```ts
const page = await calls.search({ query: { status: 'answered' }, sort: { at: -1 }, limit: 50, count: true })

page.rows  // decoded like the rows of query()
page.next  // cursor for the next page ('' on the last one)
page.total // with count: true
```

### Count

```ts
const answered = await calls.count({ status: 'answered' }) // POST /calls/_count
const failed = await calls.query({ status: 'failed' }).count()
const total = await calls.count()
```

### Cursor pagination

Offset pages get slower as `skip` grows. Use a cursor for deep pages.

```ts
const q = calls.query({ status: 'answered' }).sort({ _id: 1 }).limit(100)
const page = await q
const token = q.nextCursor // '' on the last page

const next = await calls.query({ status: 'answered' }).sort({ _id: 1 }).limit(100).after(token)
```

A token works only with the same query and `sort`. Another query shape throws `CursorError`.

To walk every row, use `each()`. It follows the cursor, with `limit` as the batch size:

```ts
for await (const row of calls.query({ status: 'answered' }).sort({ _id: 1 }).limit(500).each()) {
  console.log(row._id)
}
```

### Explain

There is no explain method. Send `explain: true` in a raw request and read the header:

```ts
const res = await ndb.stream('POST', calls.path('_search'), { query: { status: 'failed' }, explain: true, limit: 1 })

console.log(res.header('x-ndb-plan'))    // 'index:status'
console.log(res.header('x-ndb-explain')) // JSON string with the counters

await res.body.dump()
```

## Aggregate

The pipeline needs exactly one `$group`. Before it: `$match` and one `$sort`. After it: `$match`, `$sort`, `$limit`, `$skip`, `$project`. Accumulators: `$count`, `$sum`, `$avg`, `$min`, `$max`, `$first`, `$last`.

```ts
const res = await calls.aggregate(
  [
    { $match: { status: { $in: ['answered', 'failed'] } } },
    {
      $group: {
        _id: { status: '$status', hour: { $dateTrunc: { date: '$at', unit: 'hour' } } },
        n: { $count: {} },
        avg: { $avg: '$duration' },
      },
    },
    { $sort: { n: -1 } },
  ],
  { limit: 100, count: true },
)

res.rows  // the group rows
res.next  // cursor for the next page ('' on the last one)
res.total // with count: true
```

Options: `limit`, `skip`, `after`, `count`. Pass a type to type the rows: `calls.aggregate<{ _id: string; n: number }>(...)`. `$dateTrunc` units are `minute`, `hour`, `day`, `week` and `month`.

## Watch

A watch streams the changes of a collection to a named consumer. First it sends the events after the last ack. Then it sends new events as they happen.

```ts
import { HistoryTruncatedError, WatchLaggedError } from '@thadeu/ndb'

const controller = new AbortController()

try {
  for await (const ev of calls.watch('relay', { query: { status: 'answered' }, signal: controller.signal })) {
    // ev: { consumer, pos, op: 'put' | 'del' | 'truncate', _id, _v, doc }
    await handle(ev)

    await calls.ack(ev)
  }
} catch (err) {
  if (err instanceof WatchLaggedError) {
    // The consumer was too slow. Call watch again. It continues after the last ack.
  } else if (err instanceof HistoryTruncatedError) {
    // The saved position is too old. Start over.
  } else {
    throw err
  }
}
```

- Options: `from: 'now'` (a new consumer starts at the end), `limit` (stop after n events), `query` (same filters as a query), `signal` (an `AbortSignal` that stops the stream).
- A delete event has `doc: undefined`. With `query`, a delete is sent only if the same stream saw an earlier matching put of that id.
- Delivery is at least once. After a restart an event can come again, so make the work safe to repeat.
- `ack(ev)` stores the position (`POST /{coll}/_ack`). The next `watch` with the same consumer name starts after it.
- `HistoryTruncatedError` means the WAL files for the position were deleted (`wal_retention`).

## Streams

A stream is an append-only log with consumer groups, with the behavior of Redis Streams (see [Streams](../../docs/dev/streams.md)). `T` is the type of the entry. The entry ID and time are in `Entry`, not in `T`.

```ts
const orders = db.stream<{ order: number; total: number }>('orders')

const id = await orders.append({ order: 42, total: 99.9 })            // "1731164400000-0"
const ids = await orders.appendMany([{ order: 43, total: 1 }, { order: 44, total: 2 }]) // one fsync
await orders.append(o, { trim: { maxLen: 10_000, approx: true } })

const entries = await orders.range('-', '+', 100) // XRANGE; entries[0].id, .time, .doc
const info = await orders.info()                  // length, first and last ID, groups
```

A worker in a group. `work` reads its own pending entries first, then new ones (waiting up to `blockMs`), acks the entries the handler did without an error, and claims the entries other workers left idle. An entry delivered `maxDeliveries` times goes to the dead-letter stream:

```ts
import { PoisonError, startNew } from '@thadeu/ndb'

const g = orders.group('billing')

await g.ensure(startNew) // or startAll

const controller = new AbortController()

await g.work('worker-1', async (e, signal) => {
  await charge(e.doc, signal) // resolves: ack; throws: stays pending
}, {
  count: 100,
  claimIdleMs: 60_000,
  maxDeliveries: 5,
  deadLetter: 'orders_dead',
  onError: (err) => console.error(err), // a PoisonError comes after its ack
  signal: controller.signal,
})
```

`work` resolves when the signal aborts; the abort also ends a blocked read. Delivery is at least once: make the work safe to repeat. Run many workers with different consumer names to share the work. A deleted entry is acked without the handler. Defaults: `count` 10, `blockMs` 5000, `claimIdleMs` 60000 (negative turns the claim off), `claimEveryMs` `claimIdleMs / 2` (at least 1000). A handler that throws after the abort leaves its entry pending: it is not reported and not buried. A failed read goes to `onError` and is tried again after a backoff (100 ms, doubled up to 5 s), also when the group or the stream does not exist yet: a worker that starts before its group waits for it. Only the abort ends `work`. An error that `onError` throws is ignored.

Delayed retry (ndb only). With `retry`, an entry whose handler fails is nacked: it comes back after the wait of its try, with no owner while it waits (a restarted worker does not take it early; any worker of the group takes it when it is due, within about `dueEveryMs`, default 1000). `retry` is `(deliveries) => ms` or an array of waits (the last one repeats); `tiers` builds the function, with steps `{ waitMs, upTo }`. An entry older than `maxAgeMs` (by its ID time) goes to the dead letter with `reason: 'max_age'` and is acked, with no retry. An entry that fails on its `maxDeliveries`-th delivery still goes to the dead letter (`reason: 'max_deliveries'`), not to a nack. The fsw-esl schedule (15 s, 1 min, 10 min, 1 h, 6 h, for 15 days):

```ts
import { tiers } from '@thadeu/ndb'

const minute = 60_000
const hour = 60 * minute

await g.work('worker-1', handler, {
  retry: tiers(
    { waitMs: 15_000, upTo: 1 },
    { waitMs: minute, upTo: 5 },
    { waitMs: 10 * minute, upTo: 11 },
    { waitMs: hour, upTo: 23 },
    { waitMs: 6 * hour },
  ),
  maxAgeMs: 15 * 24 * hour,
  deadLetter: 'orders_dead',
  signal: controller.signal,
})
```

The calls one by one, when you need your own loop:

| Redis | TypeScript |
|---|---|
| `XGROUP CREATE` | `g.create(startNew)` / `g.ensure(startNew)` |
| `XREADGROUP ... >` | `g.read('w1', { count: 100, blockMs: 5000, signal })` |
| `XREADGROUP ... 0` | `g.read('w1', { pending: true })` |
| `XACK` | `g.ack(...ids)` |
| `XPENDING` | `g.pending()` / `g.pendingList({ idleMs: 60_000 })`; ndb only: `waiting`, `retryAt`, `{ due: true }` |
| `XCLAIM` / `XAUTOCLAIM` | `g.claim('w2', 60_000, ids)` / `g.autoClaim('w2', 60_000, '0-0', 100)` |
| ndb only (no Redis command) | `g.nack('w1', ids, 60_000)` (back in 1 min) / `g.claimDue('w2', { cursor: '0-0', count: 100 })` |
| `XGROUP SETID` / `DESTROY` | `g.setId(id)` / `g.destroy()` |
| `XGROUP CREATECONSUMER` / `DELCONSUMER` | `g.createConsumer('w1')` / `g.deleteConsumer('w1')` |
| `XREAD` (no group) | `orders.read({ after: id, count: 100, blockMs: 5000, signal })`; `after: '$'` gives only later entries |
| `XTRIM` | `orders.trim({ maxLen: 1000 })` or `{ minId, approx, limit }` |
| `XDEL` | `orders.delete(id)` |
| `XINFO GROUPS` / `CONSUMERS` | `orders.groups()` / `g.consumers()` |

A blocked read has no request timeout: it waits as long as `blockMs`. Stream errors have their own classes: `StreamIdError` (`invalid_stream_id`, `stream_id_too_small`), `UnknownGroupError`, `GroupExistsError` and `NotAStreamError`. `db.stream(name)` with one argument is a stream; with a method and a path it is a raw request.

### Redis commands (ioredis and node-redis)

An app that uses Redis Streams can change to ndb and keep its calls. Two subpaths give the stream commands of the two Redis libraries on top of ndb streams, with the same arguments and the same answers. The app selects the backend in one place:

```ts
import { connect } from '@thadeu/ndb'
import { ioredis } from '@thadeu/ndb/ioredis'
import { Redis } from 'ioredis'

const streams = process.env.STREAMS_BACKEND === 'ndb' ? ioredis(connect()) : new Redis()

await streams.xadd('orders', 'MAXLEN', '~', 1000, '*', 'order_id', '42')
await streams.xgroup('CREATE', 'orders', 'billing', '$', 'MKSTREAM')

const got = await streams.xreadgroup('GROUP', 'billing', 'w1', 'COUNT', 10, 'BLOCK', 5000, 'STREAMS', 'orders', '>')
// [['orders', [['1731164400000-0', ['order_id', '42']]]]], or null

await streams.xack('orders', 'billing', '1731164400000-0')
```

The adapters have no dependency: the main entry does not load them, and they do not import `ioredis` or `redis`. Their types are structural. A type test in this package checks that each adapter is assignable to the stream commands of its library (`Pick<Redis, ...>` of ioredis 5, `Pick<RedisClientType, ...>` of node-redis 5), so the two sides of the `?:` above have the same methods.

#### `@thadeu/ndb/ioredis`

`ioredis(db)` gives the methods of ioredis 5: lowercase names, the arguments of the Redis command in the same order (option words in any case), an optional callback as the last argument, and the raw arrays of Redis as the answer.

| Method | Arguments | Answer |
|---|---|---|
| `xadd` | `key [NOMKSTREAM] [MAXLEN\|MINID [=\|~] n [LIMIT n]] id field value ...` | the ID; `null` with `NOMKSTREAM` and no stream |
| `xlen` | `key` | a number |
| `xrange` / `xrevrange` | `key start end [COUNT n]` (`(id` leaves the ID out) | `[[id, [field, value, ...]], ...]` |
| `xread` | `[COUNT n] [BLOCK ms] STREAMS key ... id ...` | `[[key, [[id, [field, value]]]]]` or `null` |
| `xdel` | `key id ...` | a number |
| `xtrim` | `key MAXLEN\|MINID [=\|~] n [LIMIT n]` | a number |
| `xgroup` | `CREATE key group id [MKSTREAM] [ENTRIESREAD n]`, `SETID key group id [ENTRIESREAD n]`, `DESTROY key group`, `CREATECONSUMER key group consumer`, `DELCONSUMER key group consumer` | `'OK'`, `'OK'`, `1`/`0`, `1`/`0`, the pending count |
| `xreadgroup` | `GROUP group consumer [COUNT n] [BLOCK ms] [NOACK] STREAMS key ... id ...` | as `xread`; with an ID that is not `>`, `[[key, []]]` when there is no entry, and `[id, null]` for a deleted entry |
| `xack` | `key group id ...` | a number |
| `xpending` | `key group` | `[count, lowestId, highestId, [[consumer, '2']]]`, or `[0, null, null, null]` |
| `xpending` | `key group [IDLE ms] start end count [consumer]` | `[[id, consumer, idleMs, deliveries], ...]` |
| `xclaim` | `key group consumer minIdle id ... [IDLE ms] [RETRYCOUNT n] [FORCE] [JUSTID] [LASTID id]` | entries as `xrange`; IDs with `JUSTID` |
| `xautoclaim` | `key group consumer minIdle start [COUNT n] [JUSTID]` | `[nextStart, entries or IDs, deletedIds]` |
| `xinfo` | `STREAM key`, `GROUPS key`, `CONSUMERS key group` | flat `[name, value, ...]` arrays, with the field names of Redis |

The return types are those of ioredis (`unknown[]` for `xreadgroup`, `xpending`, `xclaim` and `xautoclaim`; `unknown` for `xgroup` and `xinfo`). The subpath exports `EntryReply`, `StreamsReply` and `GroupStreamsReply` for a cast.

#### `@thadeu/ndb/node-redis`

`nodeRedis(db)` gives the methods of node-redis 5 (the `redis` package): camel case names, options as an object, and the answers that node-redis makes from the arrays.

```ts
import { nodeRedis } from '@thadeu/ndb/node-redis'
import { createClient } from 'redis'

const streams = process.env.STREAMS_BACKEND === 'ndb' ? nodeRedis(connect()) : await createClient().connect()

await streams.xAdd('orders', '*', { order_id: '42' }, { TRIM: { strategy: 'MAXLEN', strategyModifier: '~', threshold: 1000 } })
await streams.xGroupCreate('orders', 'billing', '$', { MKSTREAM: true })

const got = await streams.xReadGroup('billing', 'w1', { key: 'orders', id: '>' }, { COUNT: 10, BLOCK: 5000 })
// [{ name: 'orders', messages: [{ id: '1731164400000-0', message: { order_id: '42' } }] }], or null
```

| Method | Answer |
|---|---|
| `xAdd(key, id, message, { TRIM }?)` | the ID |
| `xAddNoMkStream(key, id, message, { TRIM }?)` | the ID, or `null` when there is no stream |
| `xLen(key)` | a number |
| `xRange(key, start, end, { COUNT }?)` / `xRevRange(key, end, start, { COUNT }?)` | `[{ id, message }]` |
| `xRead(streams, { COUNT, BLOCK }?)` | `[{ name, messages: [{ id, message }] }]` or `null` |
| `xDel(key, id \| ids)` | a number |
| `xTrim(key, 'MAXLEN' \| 'MINID', threshold, { strategyModifier, LIMIT }?)` | a number |
| `xGroupCreate(key, group, id, { MKSTREAM, ENTRIESREAD }?)` / `xGroupSetId(key, group, id, { ENTRIESREAD }?)` | `'OK'` |
| `xGroupDestroy(key, group)` / `xGroupCreateConsumer(key, group, consumer)` | `1` or `0` |
| `xGroupDelConsumer(key, group, consumer)` | the pending count |
| `xReadGroup(group, consumer, streams, { COUNT, BLOCK, NOACK }?)` | as `xRead` |
| `xAck(key, group, id \| ids)` | a number |
| `xPending(key, group)` | `{ pending, firstId, lastId, consumers: [{ name, deliveriesCounter }] \| null }` |
| `xPendingRange(key, group, start, end, count, { IDLE, consumer }?)` | `[{ id, consumer, millisecondsSinceLastDelivery, deliveriesCounter }]` |
| `xClaim(key, group, consumer, minIdle, id \| ids, { IDLE, RETRYCOUNT, FORCE, LASTID }?)` | `[{ id, message }]` |
| `xClaimJustId(...)` | IDs |
| `xAutoClaim(key, group, consumer, minIdle, start, { COUNT }?)` | `{ nextId, messages, deletedMessages }` |
| `xAutoClaimJustId(...)` | the same, with IDs in `messages` |
| `xInfoStream(key)` | `{ length, 'last-generated-id', 'entries-added', 'first-entry', ... }` |
| `xInfoGroups(key)` | `[{ name, consumers, pending, 'last-delivered-id', 'entries-read', lag }]` |
| `xInfoConsumers(key, group)` | `[{ name, pending, idle, inactive }]` |

The answers are those of the default client (RESP 2, no type mapping). A `TRIM` needs its `strategy`, as with a real Redis.

#### What is different from Redis

- **Values are text.** Redis keeps strings, so the adapters give every value as a string, and they store the values of `xadd` as strings. An entry that other ndb code appended with JSON types comes back as text: `42` is `'42'`, `true` is `'true'`, and `null`, an array or an object are JSON text.
- **Field order.** The daemon gives the fields of an entry in name order, not in the order of the `xadd`. A field name that starts with `_` is reserved, and a field name given two times is kept one time.
- **One stream for each request.** `xread` and `xreadgroup` with many streams send one request for each stream. With `BLOCK` and more than one stream they throw (`not_supported`). Before an `xreadgroup` of `>` on many streams, the adapters check the group of each stream, so a `NOGROUP` on one stream gives nothing from the others, as in Redis.
- **No `COUNT`, or `COUNT 0` or less.** Redis gives every entry. The adapters give at most 10 000 in one read (`xrange` has no limit). A `COUNT` over 10 000 is read as 10 000.
- **`BLOCK 0`** waits with no limit, as in Redis: the adapter repeats a 30 s read. A blocked `xread` on a stream that does not exist looks for it each 500 ms.
- **Not supported** (the call throws): `XREAD` with the ID `+`, `XREADGROUP ... CLAIM`, `XCLAIM ... TIME`, `XINFO STREAM ... FULL`, `XGROUP HELP` and `XINFO HELP`, the Redis 8.2 options `DELREF` and `ACKED`, and the idempotent `XADD` options. `LASTID` and `KEEPREF` are read and have no effect.
- **Not there**: `XSETID`, `XACKDEL`, `XDELEX`, the `Buffer` variants of ioredis (`xreadBuffer`, ...), pipelines and `MULTI`. The object has only the methods of the tables.
- **`xinfo`**: `radix-tree-keys` and `radix-tree-nodes` are `0`.
- **A range of `xpending`** does not take `(id`.
- **Errors in the arguments** are those of Redis: a negative `BLOCK`, `LIMIT` without `~`, an argument after the end of `xtrim`, and a number that is not decimal digits (`0x10`, `5000.0`). The option words go in any order where Redis takes them.
- **`(id` without a seq** in `xrange` is read as in Redis: `(1000` as a start leaves out only `1000-0`, and `(2000` as an end leaves out only `2000-<max seq>`.
- **Errors** are `ReplyError` (exported by the two subpaths), a subclass of `NdbError`. Its `message` is the text of Redis, so a check such as `err.message.startsWith('BUSYGROUP')` works on the two backends. `code` and `status` are those of ndb, and `cause` is the ndb error.

| Case | `message` starts with | `code` |
|---|---|---|
| The group exists | `BUSYGROUP` | `group_exists` |
| No such group, or no stream in a group command | `NOGROUP` | `unknown_group`, `unknown_collection` |
| `XGROUP` without `MKSTREAM`, and no stream | `ERR The XGROUP subcommand requires the key to exist` | `unknown_collection` |
| `XINFO` and no stream | `ERR no such key` | `unknown_collection` |
| An ID that is not greater than the last one | `ERR The ID specified in XADD is equal or smaller` | `stream_id_too_small` |
| A bad ID | `ERR Invalid stream ID` | `invalid_stream_id` |
| The name is a collection that is not a stream | `WRONGTYPE` | `not_a_stream` |
| A bad argument list | `ERR` | `invalid_request` |
| Something ndb does not have | `ERR ndb: ... is not supported` | `not_supported` |

An error that Redis does not have (`unauthorized`, `not_ready`, a network error) stays as it is. As in Redis, these give a value and not an error when the stream does not exist: `xlen` (0), `xrange` (`[]`), `xread` (`null`), `xdel`, `xtrim` and `xack` (0).

## Blobs

Large values live in the blob store. A document keeps only a reference. The same content always gives the same reference.

```ts
const blob = await ndb.putBlob(bytes, 'audio/wav') // POST /_blobs: { $blob, size, type }

await calls.update('c1', { $set: { recording: blob } })

const data = await ndb.getBlob(blob) // GET /_blobs/{ref}: a Uint8Array
```

## Admin

```ts
await calls.stats()              // rows, indexes, files, bytes (GET /calls/_stats)
await calls.compact()            // merge every segment now (POST /calls/_compact)
await calls.ensureIndex('caller') // built in the background (PUT /calls/_indexes/caller); the indexed fields
await calls.dropIndex('caller')

await ndb.collections() // one definition per collection (GET /_collections)
await ndb.stats()       // every collection, totals, version, uptime (GET /_stats)
await ndb.size()        // disk use per collection and part (GET /_size)
```

## Errors

Daemon errors are `NdbError` or a subclass. They have `code` (the daemon code) and `status` (the HTTP status; 0 for an error line inside a stream).

| Class | Codes |
|---|---|
| `UnauthorizedError` | `unauthorized` |
| `NotFoundError` | `not_found`, `unknown_collection` |
| `DuplicateIdError` | `duplicate_id` |
| `UniqueViolationError` | `unique_violation` |
| `InvalidQueryError` | `invalid_query`, `invalid_pipeline` |
| `CursorError` | `cursor_invalid`, `cursor_mismatch`, `cursor_expired` |
| `WatchLaggedError` | `watch_lagged` |
| `HistoryTruncatedError` | `history_truncated` |
| `TtlError` | `ttl_not_enabled`, `ttl_derived` |
| `InvalidUpdateError` | `unknown_operator`, `no_update`, `conflicting_update`, `invalid_field`, `unindexed_many`, `empty_filter`, `invalid_update` |
| `NdbError` | every other code: `not_ready`, `catalog_conflict`, `catalog_invalid`, `direct_collection`, `invalid_request`, `group_limit_exceeded`, `request_too_large`, `line_too_large`, `closed`, `invalid_position`, `confirm_required`, `unknown_route`, `pattern_read_only`, `http_status`, ... |

```ts
import { NdbError } from '@thadeu/ndb'

try {
  await calls.get('c1')
} catch (err) {
  if (err instanceof NdbError && err.code === 'not_ready') {
    // The daemon is still recovering, and readyTimeout ran out.
  }
}
```

`ErrorCode` is the union of the known codes.

## Testing

The tests of this package (vitest) build the daemon from the repository (`go build ./cmd/ndb`) in a global setup. They start it on a temp unix socket and a free TCP port, wait for `GET /_health` to return 200, and stop it at the end. To do the same by hand:

```sh
ndb -config /nonexistent.yml -data-dir "$(mktemp -d)" -unix /tmp/ndb-test.sock -tcp 127.0.0.1:7070
```

Add `-token-file <path>` to ask for a bearer token on TCP. Use a short socket path: macOS allows 104 bytes. Use a new collection name per test.

## License

MIT.
