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

`work` resolves when the signal aborts; the abort also ends a blocked read. Delivery is at least once: make the work safe to repeat. Run many workers with different consumer names to share the work. A deleted entry is acked without the handler. Defaults: `count` 10, `blockMs` 5000, `claimIdleMs` 60000 (negative turns the claim off), `claimEveryMs` `claimIdleMs / 2` (at least 1000).

The calls one by one, when you need your own loop:

| Redis | TypeScript |
|---|---|
| `XGROUP CREATE` | `g.create(startNew)` / `g.ensure(startNew)` |
| `XREADGROUP ... >` | `g.read('w1', { count: 100, blockMs: 5000, signal })` |
| `XREADGROUP ... 0` | `g.read('w1', { pending: true })` |
| `XACK` | `g.ack(...ids)` |
| `XPENDING` | `g.pending()` / `g.pendingList({ idleMs: 60_000 })` |
| `XCLAIM` / `XAUTOCLAIM` | `g.claim('w2', 60_000, ids)` / `g.autoClaim('w2', 60_000, '0-0', 100)` |
| `XGROUP SETID` / `DESTROY` | `g.setId(id)` / `g.destroy()` |
| `XGROUP CREATECONSUMER` / `DELCONSUMER` | `g.createConsumer('w1')` / `g.deleteConsumer('w1')` |
| `XTRIM` | `orders.trim({ maxLen: 1000 })` or `{ minId, approx, limit }` |
| `XDEL` | `orders.delete(id)` |
| `XINFO GROUPS` / `CONSUMERS` | `orders.groups()` / `g.consumers()` |

A blocked read has no request timeout: it waits as long as `blockMs`. Stream errors have their own classes: `StreamIdError` (`invalid_stream_id`, `stream_id_too_small`), `UnknownGroupError`, `GroupExistsError` and `NotAStreamError`. `db.stream(name)` with one argument is a stream; with a method and a path it is a raw request.

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
