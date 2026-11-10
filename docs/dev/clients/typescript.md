# TypeScript

The package `@thadeu/ndb` (the name `ndb` is taken on npm). Typed collections, a chainable query builder, aggregate, watch, streams, blobs and admin.

The source and the same guide are in [`sdk/ts`](https://github.com/thadeu/ndb/tree/main/sdk/ts).

## Install

```sh
pnpm add @thadeu/ndb
# or
npm install @thadeu/ndb
bun add @thadeu/ndb
```

It runs on Node 24 or newer (with `undici`) and on Bun (with its own `fetch`), over a unix socket or TCP. You also need a running ndb daemon. See the repository for how to run one.

## Connect

```ts
import { connect } from '@thadeu/ndb'

// Unix socket.
const ndb = connect('unix:///run/ndb/ndb.sock') // or tcp://host:port, https://host

// TCP with a bearer token.
const remote = connect('tcp://10.0.0.5:7070', { token: 'secret' })

// Address and token from the environment: NDB_URL and NDB_TOKEN.
const fromEnv = connect()

// Retry for up to 5 s while the daemon starts or recovers.
const patient = connect('unix:///run/ndb/ndb.sock', { readyTimeout: 5000 })

await ndb.health()
await ndb.close()
```

- `connect` does not connect. The first call does.
- The address is `unix:///path`, `tcp://host:port`, or `https://host` (ndb behind a TLS proxy; `http://` works too). Without an address, `connect` reads `NDB_URL`. With no address and no `NDB_URL`, it throws a `TypeError`: there is no default address.
- A local daemon that you start with no flags and not as root listens on the unix socket `~/.ndb/ndb.sock` and on TCP `127.0.0.1:7070`. As root (or with `/var/lib/ndb`), it listens only on `/run/ndb/ndb.sock`. Give the full path: ``connect(`unix://${os.homedir()}/.ndb/ndb.sock`)`` or `connect('tcp://127.0.0.1:7070')`. See [Configuration](../configuration.md).
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
- `time` is the collection time field. Used by zone maps, `$dateTrunc` and date math (`now-7d`).
- `collection` sends nothing. The first call declares the collection (`PUT /calls`). You can call `await calls.ready()` to do it at once. A name with `*` (a [pattern](#collection-patterns)) never declares.
- Types come from the schema. Rows have the schema fields, `_id` and `_v`, the document version. A `date` field is a `Date` in rows. Input takes `Date` values and ISO strings in filters.
- `expires` is the `date` field that holds when a document expires. It turns on `expireIn`, `expire`, `persist` and `ttl` (below).

## Insert, get, update, delete

```ts
// One document (POST /calls/_doc): one result.
const one = await calls.insert({ _id: 'c1', status: 'answered', caller: 'a', duration: 30, at: new Date() })
// one: { _id: 'c1', _v: 1 }

// Many in one transaction, all or nothing (POST /calls/_bulk): an array.
const results = await calls.insert([
  { _id: 'c2', status: 'failed', caller: 'b', at: new Date() },
  { status: 'answered', caller: 'c', at: new Date() },
])

// Get by _id (GET /calls/_doc/c1). Returns null when missing.
const row = await calls.get('c1')

// Update by _id, in one atomic step. guard adds conditions on the same
// document: a _v guard is an optimistic lock.
const res = await calls.update('c1', { $set: { status: 'done' }, $unset: { duration: true } }, { guard: { _v: row!._v } })
// res: { matched, modified, doc } (doc is the new row with _v)

// Atomic update by query. A guarded decrement: exactly one of two racing calls wins.
await stock.updateBy({ _id: 'a', qty: { $gte: 1 } }, { $inc: { qty: -1 } })

// Every match (the query needs _id or an indexed field), or create it.
await calls.updateBy({ status: 'failed' }, { $set: { status: 'retry' } }, { many: true })
await calls.update('c9', { $inc: { duration: 1 } }, { upsert: true })

// Delete by _id or by query: { deleted }.
await calls.delete('c2')
await calls.deleteBy({ status: 'failed' }, { many: true })
```

- Operators: `$set`, `$setOnInsert` (like `$set`, but only when an upsert creates the document), `$unset`, `$inc`, `$push` (with `{ $each: [...] }`), `$pull`, `$min`, `$max`. Field names can be dot paths.
- Nothing matched is not an error: `matched` is 0 (for example when the `_v` guard is stale, so read again and retry). `modified` is 0 when the operators leave the document as it is.
- `update(id, ops, { guard, upsert, expireIn })`: `upsert` creates the document with that `_id`. `updateBy(query, ops, { many, upsert, expireIn })`: `many` needs `_id` or an indexed field in the query, and `upsert` creates the document from the equality fields of the query.
- A key of the operators without `$` throws `InvalidUpdateError` before anything is sent. The daemon contract errors (`unknown_operator`, `no_update`, `conflicting_update`, `invalid_field`, `unindexed_many`, `empty_filter`) are `InvalidUpdateError` too.

An insert with a duplicate `_id` throws `DuplicateIdError`.

`upsert(doc)` and `upsertAll(docs)` create by `_id`, or merge into the stored document when the `_id` exists, in one transaction. See [Upsert](../http/documents.md#upsert).
 A duplicate `unique` value throws `UniqueViolationError`. An empty or non-string `_id` fails with code `invalid_id`.

### Truncate and drop

```ts
await calls.truncate() // every document goes; the definition and indexes stay
await calls.drop()     // the collection, its definition and its watch consumers go
```

- Both take the same time for 10 or 10 million documents. Watch consumers get one event with `op: 'truncate'` (`_id` is `''`), then the new changes.
- After `drop()`, the next call on the collection declares the collection again. A collection declared in `ndb.yml` comes back, empty, when the daemon restarts.

### Rename, restore and PITR

```ts
const restored = db.collection('calls_restored', schema)

await calls.restore({ at: new Date(Date.now() - 3_600_000), to: 'calls_restored' })
await restored.rename('calls', { replace: true })

const win = await calls.pitr() // { collection, window, oldest, newest, snapshots, bytes, dropped }
```

- `rename(to, { replace })`: documents, indexes and watch positions move to `to`. Without `replace`, an existing `to` throws `CollectionExistsError`. With `replace`, the existing `to` goes aside first. The object keeps the old name: open the collection again under `to`.
- `restore({ at, to })`: makes the collection `to` hold this collection as it was at `at`. The source does not change. Out of the PITR window (or with `pitr: off`) it throws `PitrError`. An existing `to` throws `CollectionExistsError`.
- `pitr()`: the PITR window of the collection, and the disk it holds. See [Collections](../http/collections.md).

### Expiry (TTL like Redis)

```ts
const sessions = db.collection('sessions', {
  expires: 'exp',
  fields: { exp: { type: 'date', optional: true } },
})

await sessions.insert({ _id: 's1' }, { expireIn: '1h' }) // or 3600; all docs of the call
await sessions.update('s1', { $set: { user: 'u' } }, { expireIn: '30m' })

const { v, expiresAt } = await sessions.expire('s1', '30m') // EXPIRE
await sessions.expireAt('s1', new Date(Date.now() + 3_600_000))
await sessions.ttl('s1') // seconds left, -1 = never expires, null = missing or expired
await sessions.persist('s1') // PERSIST
```

- A duration is seconds or a string such as `"90m"`, `"1h30m"`, `"1d"`. `expireIn` wins over a value of the `expires` field in the same call.
- `expire` and `persist` throw `NotFoundError` for a missing or expired document. `TtlError` (`ttl_not_enabled`, `ttl_derived`) means the schema has no `expires` field, or the expiry comes from `ttl` + `time`.
- Disk space of expired documents returns after compaction, and fully only with `wal_retention`; see [Freeing disk](../configuration.md#freeing-disk).

## Query

### Queries built elsewhere

For a search form or a saved search, take the JSON as is. `rawQuery` takes an untyped query and chains like `query`; `search` takes a whole search body and returns one page.

```ts
const form = JSON.parse(input) as Record<string, unknown>
const rows = await calls.rawQuery(form).query({ dnis: '1000' }).sort({ ts: -1 }).all()

const page = await calls.search({ query: { status: 'answered' }, sort: { ts: -1 }, limit: 50, count: true })
page.rows   // Row[], decoded like query()
page.next   // cursor for { after }; '' on the last page
page.total  // with count: true
```

Rows decode like any other query. The daemon validates the JSON (`invalid_query`), but it takes any field: for a public form, keep an allowlist of fields and operators, cap `limit`, and ask for an indexed field or a time window, as you would before sending a form to Elasticsearch `_search`.

### Collection patterns

A collection named with a [pattern](../http/queries.md#collection-patterns) (`logs-2026-*`) reads every matched collection. It sends no `PUT`, and its writes reject with code `pattern_read_only`. Each row has `_coll`, the name of its collection.

```ts
const logs = db.collection('logs-2026-*', logSchema)

const rows = await logs.query({ lvl: 'error' }).sort({ at: -1 }).limit(50).all()
rows[0]._coll // 'logs-2026-11'
```

### Count

```ts
const answered = await calls.count({ status: 'answered' }) // POST /calls/_count
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

A token works only with the same `query` and `sort`. Another query shape throws `CursorError`.

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

## Lookup

`lookup` is a key of the HTTP search (`POST /{coll}/_search`): in the same call, the daemon reads the documents of other collections that each row points to, and puts them in the row. The HTTP API is the primitive and has every rule ([Lookup](../query.md#lookup)); this SDK only builds that JSON.

| Relation | The foreign key lives | Match | The row gets |
|---|---|---|---|
| belongs to | in **this** collection | `row.fk == other.pk` | an object, or null |
| has many | in the **other** collection | `other.fk == row.pk` | an array (100 unless a limit, at most 1000) |
| has one | in the **other** collection | `other.fk == row.pk` | an object, or null |

`pk` is `_id` unless you say otherwise. The field that is searched on the other side (its `pk` for a belongs to, its `fk` for a has many) must be `_id` or indexed, else the daemon answers `lookup_unindexed`.

### Basic use

```ts
import { belongsTo, hasMany, hasOne } from '@thadeu/ndb'

// belongs to: orders.customer_id -> customers._id
const rows = await orders
  .query({ status: 'paid' })
  .lookup<'customer', { name: string } | null>('customer', belongsTo('customers', 'customer_id'))
  .all()

rows[0].customer?.name // 'Ana' (null when there is no customer)

// has many: order_items.order_id -> orders._id
const withItems = await orders
  .query({})
  .lookup<'items', Item[]>('items', hasMany('order_items', 'order_id'))
  .all()

// has one: invoices.order_id -> orders._id
const withInvoice = await orders
  .query({})
  .lookup<'invoice', Invoice | null>('invoice', hasOne('invoices', 'order_id'))
  .all()
```

The type parameters are optional: the first is the name, the second the type of the field (`Record<string, unknown>` by default).

### Options

```ts
hasMany('order_items', 'order_id', {
  query: { qty: { $gt: 0 } },     // filter the related documents
  sort: { pos: 1 },               // their order
  limit: 50,                      // per row, 1 to 1000
  fields: ['sku', 'qty'],         // keep these fields
  lookup: {                       // nested, two levels at most
    product: belongsTo('products', 'sku', { pk: 'code' }),   // products.code instead of _id
  },
})
```

`belongsTo` takes `pk`, `fields` and `lookup`; `hasOne` takes everything but `limit`. Each one is the plain JSON object of the HTTP API (`{ has_many: 'order_items', fk: 'order_id', ... }`), so you can also write it by hand.

### Index

A `hasMany('order_items', 'order_id')` needs an index on `order_items.order_id`: `index: true` on that field in the schema of the collection, or `items.ensureIndex('order_id')`.

### Errors

`LookupError` (codes `lookup_invalid`, `lookup_unindexed`, `lookup_too_large`) and `NotFoundError` for an unknown collection.

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
    // ev: { consumer, pos, op: 'put' | 'del' | 'truncate', _id, v, doc }
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

- Options: `from: 'now'` (a new consumer starts at the end), `limit` (stop after n events), `query` (same filters as a search), `signal` (an `AbortSignal` that stops the stream).
- A delete event has `doc: undefined`. With `query`, a delete is sent only if the same stream saw an earlier matching put of that id.
- Delivery is at least once. After a restart an event can come again, so make the work safe to repeat.
- `ack` stores the position. The next `watch` with the same consumer name starts after it.
- `HistoryTruncatedError` means the WAL files for the position were deleted (`wal_retention`).

## Streams

A stream is an append-only log with consumer groups. It has the behavior of Redis Streams. See [Streams](../streams.md) for the model and [Stream routes](../http/streams.md) for the HTTP API.

`db.stream<T>(name)` gives a `Stream<T>`. `T` is the type of the entry. The entry ID and the time are in `Entry`, not in `T`.

```ts
import { connect } from '@thadeu/ndb'

const db = connect()
const orders = db.stream<{ order: number; total: number }>('orders')
```

- `db.stream(name)` sends nothing. The first append, or the first group create, makes the stream. `await orders.create()` makes it at once, or checks that the name is a stream.
- The `prefix` of `connect` goes before the stream name, as for a collection.
- `db.stream` has two forms. With **one** argument (a name), it gives a `Stream<T>`. With a **method and a path** (`db.stream('POST', path, body)`), it is a raw request: it returns a `Streamed` answer that you read as bytes or NDJSON rows (see [Explain](#explain)). The two forms are not related.
- An entry is `Entry<T>`: `{ id, time, doc, deleted }`. `id` is `"<ms>-<seq>"`. `time` is the ms part of the ID as a `Date`. `doc` is the entry without `_id` and `_ts`. `deleted` is `true` for a pending entry that a trim or a delete removed; then `doc` is `{}`.
- `idTime(id)` gives the `Date` of an ID. A bad ID gives an invalid `Date`.

### append

```ts
const id = await orders.append({ order: 42, total: 99.9 }) // "1731164400000-0"

await orders.append({ order: 43, total: 1 }, { trim: { maxLen: 10_000, approx: true } })
await orders.append({ order: 44, total: 2 }, { id: '1731164400500-*' })
await orders.append({ order: 45, total: 3 }, { noCreate: true })
```

Adds one entry and returns its ID (`XADD`).

- `id`: `"<ms>-<seq>"` or `"<ms>-*"`. Default `"*"`: the daemon makes the ID. An ID that is not greater than the last one throws `StreamIdError` (`stream_id_too_small`).
- `trim`: trims the stream after the append. It takes the [trim](#trim) options.
- `noCreate`: when the stream does not exist, throw `NotFoundError` (`unknown_collection`) and do not make it (`NOMKSTREAM`).

### appendMany

```ts
const ids = await orders.appendMany([
  { order: 46, total: 1 },
  { order: 47, total: 2 },
])
```

- Adds the entries in order, with one fsync for all. Returns their IDs.
- It takes the same options as `append`. `id` applies to the first entry.

### trim

```ts
await orders.trim({ maxLen: 1000 })                          // keep the newest 1000
await orders.trim({ minId: '1731160000000-0' })              // delete the IDs lower than this
await orders.trim({ maxLen: 1000, approx: true, limit: 5000 })
```

Deletes old entries and returns how many it deleted (`XTRIM`).

- Give `maxLen` or `minId`.
- `approx`: delete only whole blocks of 1 024 entries (`~` in Redis). It is faster.
- `limit`: with `approx`, delete at most this many entries.
- A trimmed entry that is still pending comes to a group read with `deleted: true`. See [Trim and retention](../streams.md#trim-and-retention).

### info

```ts
const info = await orders.info()
// { length, first_id, last_id, entries_added, max_deleted_id, groups }
```

- The state of the stream (`XINFO STREAM`, `XLEN`).

### range and revRange

```ts
const first = await orders.range('-', '+', 100)  // XRANGE, lowest ID first
const last = await orders.revRange('+', '-', 10) // XREVRANGE, highest ID first

first[0].id
first[0].time
first[0].doc.total
```

- `range(start = '-', end = '+', count?)`: up to `count` entries with an ID from `start` to `end`, both included, in ID order.
- `revRange(end = '+', start = '-', count?)`: the same, from `end` down to `start`. Note the order of the arguments: `end` comes first, as in Redis.
- `-` and `+` are the lowest and the highest ID. Without `count`, the daemon default applies.

### read without a group

```ts
let entries = await orders.read({ after: '0', count: 100 }) // XREAD, from the start

entries = await orders.read({ after: entries.at(-1)?.id, count: 100, blockMs: 5000, signal })

await orders.read({ after: '$', blockMs: 5000 }) // only the entries appended after this call starts
```

`orders.read(opts?)` gives the entries after an ID, in ID order, without a group (`XREAD`). It changes no group and no pending list, and nothing is acked. An empty answer is an empty array. The read of a group is [`group.read`](#read).

Fields of `StreamReadOptions`:

- `after`: give the entries with an ID greater than this one. Default `'0'`, the start of the stream. `'$'` is the last ID when the call starts: only later entries come.
- `count`: the most entries to give. Default 10, at most 10 000.
- `blockMs`: when there is no entry, wait up to this long (ms) for one.
- `fields`: give only these fields (and `_id`).
- `signal`: an `AbortSignal` that ends the read, also a blocked one.

To read on, give the ID of the last entry as `after`. Do not give `'$'` again: an entry appended between two calls would be lost.

### delete

```ts
const n = await orders.delete('1731164400000-0') // 1, or 0 when it is not there
```

- Removes one entry by ID (`XDEL`).

### groups

```ts
const list = await orders.groups()
// [{ name, consumers, pending, last_delivered_id, entries_read, lag }]
```

- The groups of the stream (`XINFO GROUPS`). `entries_read` and `lag` are `null` when they cannot be known.

### group

```ts
import { startAll, startNew } from '@thadeu/ndb'

const g = orders.group('billing') // sends nothing

await g.create(startNew)  // XGROUP CREATE ... MKSTREAM
await g.ensure(startAll)  // create, or do nothing when it exists
await g.setId('0')        // XGROUP SETID
await g.destroy()         // XGROUP DESTROY
```

`stream.group(name)` gives a `Group<T>`. Each entry goes to one consumer of the group. It stays pending until it is acked.

- `create(start = startNew)`: makes the group, and the stream when it does not exist. A group that exists throws `GroupExistsError`.
- `ensure(start = startNew)`: like `create`, but a group that exists is not an error. Use it at the start of a worker.
- `start` is `startNew` (`'$'`: only the entries appended after the create), `startAll` (`'0'`: every entry) or an entry ID.
- `setId(id)`: moves the last delivered ID of the group.
- `destroy()`: deletes the group and its pending entries.
- A call on a group that does not exist throws `UnknownGroupError`.

### consumers

```ts
const created = await g.createConsumer('w1') // true when it is new
const list = await g.consumers()             // [{ name, pending, idle_ms, inactive_ms }]
const held = await g.deleteConsumer('w1')    // how many pending entries it held
```

- `createConsumer(name)`: makes a consumer (`XGROUP CREATECONSUMER`). A read makes it too.
- `consumers()`: the consumers of the group (`XINFO CONSUMERS`).
- `deleteConsumer(name)`: deletes the consumer and its pending entries (`XGROUP DELCONSUMER`).

### read

```ts
const controller = new AbortController()

const entries = await g.read('w1', { count: 100, blockMs: 5000, signal: controller.signal })
const mine = await g.read('w1', { pending: true, after: '0' })
```

Gives entries to the consumer (`XREADGROUP`). By default it gives new entries, and they become pending for that consumer. An empty answer is an empty array.

- `count`: the most entries to give. Default 10, at most 10 000.
- `blockMs`: when there is no new entry, wait up to this long (ms) for one. A blocked read has no request timeout.
- `pending`: give the consumer's own pending entries (`XREADGROUP ... 0`), with an ID greater than `after`, not new ones.
- `noAck`: do not add the entries to the pending list (at most once).
- `fields`: give only these fields (and `_id`).
- `signal`: an `AbortSignal` that ends the read, also a blocked one.

### ack

```ts
const n = await g.ack(...entries.map((e) => e.id))
```

- Ends the work on the entries (`XACK`). Returns how many were pending. An ID that is not pending counts 0.
- With no ID, it sends nothing and returns 0.

### pending

```ts
const sum = await g.pending()
// { count, lowest, highest, consumers: [{ name, count }] }
```

- The pending list in short (`XPENDING`). `lowest` and `highest` are `null` when the list is empty.

### pendingList

```ts
const idle = await g.pendingList({ idleMs: 60_000, count: 100 })
// [{ id, consumer, idleMs, deliveries }]
```

- Pending entries, in ID order (`XPENDING` with a range).
- Options: `start` and `end` (default `'-'` and `'+'`), `count` (default 100), `consumer` (only its entries), `idleMs` (only the entries idle for at least this long).

### claim

```ts
const res = await g.claim('w2', 60_000, ['1731164400000-0'])
// { cursor: '', entries, deleted }
```

- `claim(consumer, minIdleMs, ids)`: moves the entries idle for at least `minIdleMs` to `consumer` (`XCLAIM`). Each one gets one more delivery.
- `entries` are the claimed entries. `deleted` are the pending IDs whose entry is no longer in the stream. They left the pending list.

### autoClaim

```ts
let cursor = '0-0'

do {
  const res = await g.autoClaim('w2', 60_000, cursor, 100)

  await work(res.entries)

  cursor = res.cursor
} while (cursor !== '0-0')
```

- `autoClaim(consumer, minIdleMs, cursor = '0-0', count?)`: scans the pending list from `cursor` and moves up to `count` idle entries to `consumer` (`XAUTOCLAIM`).
- `cursor` in the answer is where the next call starts. It is `'0-0'` when the scan is complete.

### work

```ts
import { PoisonError } from '@thadeu/ndb'

const g = orders.group('billing')

await g.ensure()

const controller = new AbortController()
process.on('SIGTERM', () => controller.abort())

await g.work('worker-1', async (entry, signal) => {
  await charge(entry.doc, signal) // resolves: ack; throws: stays pending
}, {
  count: 100,
  claimIdleMs: 60_000,
  maxDeliveries: 5,
  deadLetter: 'orders_dead',
  onError: (err) => {
    if (err instanceof PoisonError) console.warn('dead', err.id, err.deliveries)
    else console.error(err)
  },
  signal: controller.signal,
})
```

`group.work(consumer, handler, opts)` runs a worker of the group until the signal aborts. Then it resolves. The worker:

1. Reads its own pending entries first (the ones it took before a restart and did not ack), and does the work on them.
2. Reads new entries, and waits up to `blockMs` for them.
3. Acks each entry that the handler did without an error, in one call.
4. Every `claimEveryMs`, claims the entries of any consumer of the group that are idle for `claimIdleMs`, and tries them again.

Notes:

- The handler is `(entry, signal) => unknown`. When it resolves, the entry is acked. When it throws, the entry stays pending, and `onError` gets the error.
- A throw after the signal aborted is a stop, not a failure: the entry stays pending for the next start. `onError` does not get it, and the entry does not go to the dead letter.
- The group must exist. Call `ensure()` first. When it does not exist, `work` rejects with `UnknownGroupError`. Other errors of the first read (of the pending entries) go to `onError`, and the worker tries again, as for a read of new entries.
- Options and defaults:
  - `count`: the most entries per read. Default 10.
  - `blockMs`: how long a read waits for new entries. Default 5000.
  - `claimIdleMs`: default 60000. A negative value turns the claim off.
  - `claimEveryMs`: default `claimIdleMs / 2`, at least 1000.
  - `maxDeliveries`: default 0 (no limit).
  - `deadLetter`: the stream name for poison messages. Default none.
  - `onError`: gets the errors that do not stop the worker: a failed read (the worker then waits, from 100 ms up to 5 s), a handler error, a failed ack or claim, and a `PoisonError`. An error that `onError` throws is ignored: the worker and its acks go on.
  - `signal`: an `AbortSignal` that stops the worker. Without it, the worker runs forever.
- Stop: abort the signal. The abort also ends a blocked read. The acks of the work that is done are still sent.
- Poison message: an entry that fails on its `maxDeliveries`-th delivery. At that failure, when `deadLetter` is set, the worker appends a `DeadEntry` to that stream: `{ stream, group, id, deliveries, doc }`. Then it acks the entry, and `onError` gets a `PoisonError` (with `id` and `deliveries`). Without `deadLetter`, the entry is only acked and reported.
- An own pending entry at start with more than `maxDeliveries` deliveries was left by a process that stopped in the handler (a crash, a kill). The worker sends it to the dead letter without the handler, so a poison entry that kills the process does not run forever.
- A `deleted` entry (trimmed or deleted while pending) is acked without the handler.
- Delivery is at least once. Make the work safe to repeat.
- To share the work, run many workers with different consumer names in the same group.
- See [The worker in the SDKs](../streams.md#the-worker-in-the-sdks).

### Stream errors

| Class | Codes |
|---|---|
| `NotAStreamError` | `not_a_stream`: a stream call on a collection that is not a stream |
| `StreamIdError` | `invalid_stream_id`, `stream_id_too_small` |
| `UnknownGroupError` | `unknown_group` |
| `GroupExistsError` | `group_exists` (`create` only; `ensure` ignores it) |
| `PoisonError` | `poison`: not from the daemon. `work` gives it to `onError` |

All are `NdbError` subclasses. See the error table below and the [stream route errors](../http/streams.md#errors).

## Redis Streams commands

An app that uses Redis Streams can change to ndb and keep its calls. Two subpaths of the package give the stream commands of the two Redis libraries on ndb streams, with the same arguments and the same answers: `@thadeu/ndb/ioredis` and `@thadeu/ndb/node-redis`. The app picks its backend in one place, its factory. See [Move from Redis Streams](../streams.md#move-from-redis-streams) for the same change in Ruby and Go.

- The adapters have no dependency. The main entry does not load them, and they do not import `ioredis` or `redis`. Their types are structural.
- A type test of the package checks that each adapter is assignable to the stream commands of its library (`Pick<Redis, ...>` of ioredis 5, `Pick<RedisClientType, ...>` of node-redis 5). So the two sides of the `?:` in the factory have the same methods.
- The two libraries have different shapes, so there are two adapters. Use the one of the library that the app has.

### ioredis

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

`ioredis(db)` gives an `IoredisStreams`: the methods of ioredis 5. They have lowercase names, the arguments of the Redis command in the same order (option words in any case), an optional callback as the last argument, and the raw arrays of Redis as the answer.

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

- The return types are those of ioredis: `unknown[]` for `xreadgroup`, `xpending`, `xclaim` and `xautoclaim`; `unknown` for `xgroup` and `xinfo`. The subpath exports `EntryReply`, `PendingEntryReply`, `StreamsReply` and `GroupStreamsReply` for a cast.
- The option words go in any order where Redis takes them: `xadd key MAXLEN 10 NOMKSTREAM * f v`, `xautoclaim ... JUSTID COUNT 5`, `xgroup CREATE key g $ ENTRIESREAD 0 MKSTREAM`.
- There are no `Buffer` variants (`xreadBuffer`, ...).

### node-redis

```ts
import { connect } from '@thadeu/ndb'
import { nodeRedis } from '@thadeu/ndb/node-redis'
import { createClient } from 'redis'

const streams = process.env.STREAMS_BACKEND === 'ndb' ? nodeRedis(connect()) : await createClient().connect()

await streams.xAdd('orders', '*', { order_id: '42' }, { TRIM: { strategy: 'MAXLEN', strategyModifier: '~', threshold: 1000 } })
await streams.xGroupCreate('orders', 'billing', '$', { MKSTREAM: true })

const got = await streams.xReadGroup('billing', 'w1', { key: 'orders', id: '>' }, { COUNT: 10, BLOCK: 5000 })
// [{ name: 'orders', messages: [{ id: '1731164400000-0', message: { order_id: '42' } }] }], or null
```

`nodeRedis(db)` gives a `NodeRedisStreams`: the methods of node-redis 5 (the `redis` package). They have camel case names, options as an object, and the answers that node-redis makes from the arrays.

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

- The answers are those of the default client (RESP 2, no type mapping).
- A `TRIM` needs its `strategy`, as with a real Redis.

### ReplyError

Errors are `ReplyError`, exported by the two subpaths. It is a subclass of `NdbError`.

```ts
import { ReplyError } from '@thadeu/ndb/ioredis'

try {
  await streams.xgroup('CREATE', 'orders', 'billing', '$', 'MKSTREAM')
} catch (err) {
  const exists = err instanceof Error && err.message.startsWith('BUSYGROUP')

  if (!exists) throw err

  if (err instanceof ReplyError) console.warn(err.code) // "group_exists", on the ndb backend only
}
```

- `message` is the text of Redis, so a check such as `err.message.startsWith('BUSYGROUP')` works on the two backends.
- `code` and `status` are those of ndb, and `cause` is the ndb error.
- `err instanceof ReplyError` is true only on the ndb backend. The Redis libraries have their own error classes.

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

- An error that Redis does not have (`unauthorized`, `not_ready`, a network error) stays as it is.
- As in Redis, these give a value and not an error when the stream does not exist: `xlen` (0), `xrange` (`[]`), `xread` (`null`), `xdel`, `xtrim` and `xack` (0).

### Differences from Redis

- **Values are text.** Redis keeps strings, so the adapters give every value as a string, and they store the values of `xadd` as strings. An entry that other ndb code appended with JSON types comes back as text: `42` is `'42'`, `true` is `'true'`, and `null`, an array or an object are JSON text.
- **Field order.** The daemon gives the fields of an entry in name order, not in the order of the `xadd`. A field name that starts with `_` is reserved, and a field name given two times is kept one time.
- **Stream names.** A stream is an ndb collection, so its name follows the rules of a collection name. `orders:eu` is not a valid name.
- **One stream for each request.** `xread` and `xreadgroup` with many streams send one request for each stream. With `BLOCK` and more than one stream they throw (`not_supported`). Before an `xreadgroup` of `>` on many streams, the adapters check the group of each stream, so a `NOGROUP` on one stream gives nothing from the others, as in Redis.
- **No `COUNT`, or `COUNT 0` or less.** Redis gives every entry. The adapters give at most 10 000 in one read (`xrange` has no limit). A `COUNT` over 10 000 is read as 10 000.
- **`BLOCK 0`** waits with no limit, as in Redis: the adapter repeats a 30 s read. A blocked `xread` on a stream that does not exist looks for it each 500 ms.
- **Not supported** (the call throws): `XREAD` with the ID `+`, `XREADGROUP ... CLAIM`, `XCLAIM ... TIME`, `XINFO STREAM ... FULL`, `XGROUP HELP` and `XINFO HELP`, the Redis 8.2 options `DELREF` and `ACKED`, and the idempotent `XADD` options. `LASTID` and `KEEPREF` are read and have no effect.
- **Not there:** `XSETID`, `XACKDEL`, `XDELEX`, the `Buffer` variants of ioredis, pipelines and `MULTI`. The object has only the methods of the tables.
- **`xinfo`:** `radix-tree-keys` and `radix-tree-nodes` are `0`.
- **A range of `xpending`** does not take `(id`.
- **Errors in the arguments** are those of Redis: a negative `BLOCK` (`ERR timeout is negative`), `LIMIT` without `~`, an argument after the end of `xtrim`, and a number that is not decimal digits (`0x10`, `5000.0`).
- **`(id` without a seq** in `xrange` is read as in Redis: `(1000` as a start leaves out only `1000-0`, and `(2000` as an end leaves out only `2000-<max seq>`.

## Admin and blobs

```ts
await db.health()                 // GET /_health; throws while recovering
const defs = await db.collections() // GET /_collections
const stats = await db.stats()    // GET /_stats
const size = await db.size()      // GET /_size

await calls.stats()               // GET /calls/_stats
await calls.compact()             // POST /calls/_compact
await calls.ensureIndex('dnis')   // PUT /calls/_indexes/dnis => ['dnis', 'status']
await calls.dropIndex('dnis')

const blob = await db.putBlob(bytes, 'audio/wav') // { $blob: 'sha256:...', size, type }
await calls.update('c1', { $set: { meta: { recording: blob } } })
const data = await db.getBlob(blob)              // Uint8Array
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
| `CollectionExistsError` | `collection_exists` |
| `PitrError` | `out_of_window`, `pitr_off` |
| `LookupError` | `lookup_invalid`, `lookup_unindexed`, `lookup_too_large` |
| `NotAStreamError` | `not_a_stream` |
| `StreamIdError` | `invalid_stream_id`, `stream_id_too_small` |
| `UnknownGroupError` | `unknown_group` |
| `GroupExistsError` | `group_exists` |
| `NdbError` | every other code: `not_ready`, `catalog_conflict`, `catalog_invalid`, `direct_collection`, `invalid_request`, `group_limit_exceeded`, `request_too_large`, `line_too_large`, `closed`, `invalid_position`, `http_status`, ... |

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

`ErrorCode` is the union of every code of the [error table](../http/errors.md), so `err.code === 'invalid_path'` is type-checked. A test of the repository fails when the daemon gets a code that is not in it.

## Testing

ndb has no transactions, so a test cannot roll back. `clean(db)` truncates the collections that `db` used since the last clean (a few ms at any size; the definition and the indexes stay), like `Ndb::Testing` in Rails. It has no test framework in it, so vitest, jest, `node --test` and `bun test` all use it the same way:

```ts
import { clean, cleanAll, connect } from '@thadeu/ndb'

const db = connect(process.env.NDB_URL, { prefix: `test${process.env.VITEST_POOL_ID ?? 1}_` })

beforeAll(() => cleanAll(db))  // every collection with the prefix
afterEach(() => clean(db))     // the ones this test used
```

- `clean(db, { only, except })`: truncate these, or leave some out.
- `cleanAll(db)` truncates every engine collection with the prefix of `db`. Direct collections and partitions are left out.
- `prefix` (a `connect` option) goes before the name of every collection: each parallel worker gets collections of its own.
- A collection that does not exist is not an error.
- A clean runs only in a test run: `NODE_ENV=test` (jest, `bun test`), `VITEST`, `JEST_WORKER_ID` or `NODE_TEST_CONTEXT` (`node --test`). Anywhere else it throws `UnsafeCleanError`. `NDB_ALLOW_CLEAN=1` forces it.
- The SDK does not start a daemon: start it in CI as a service, with `make up`, or with Docker, and connect with `NDB_URL`.

### Tests of this package

The tests of this package (vitest) build the daemon from the repository (`go build ./cmd/ndb`) in a global setup. They start it on a temp unix socket and a free TCP port, wait for `GET /_health` to return 200, and stop it at the end. To do the same by hand:

```sh
ndb -config /nonexistent.yml -data-dir "$(mktemp -d)" -unix /tmp/ndb-test.sock -tcp 127.0.0.1:7070
```

Add `-token-file <path>` to ask for a bearer token on TCP. Use a short socket path: macOS allows 104 bytes. Use a new collection name per test.
