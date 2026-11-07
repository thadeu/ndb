# TypeScript

The package `@thadeu/ndb` (the name `ndb` is taken on npm). Typed collections, a chainable query builder, aggregate, watch, blobs and admin.

The source and the same guide are in [`sdk/ts`](https://github.com/thadeu/ndb/tree/main/sdk/ts).

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

await ndb.health()
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

An insert with a duplicate `_id` throws `DuplicateIdError`. A duplicate `unique` value throws `UniqueViolationError`. An empty or non-string `_id` fails with code `invalid_id`.

### Truncate and drop

```ts
await calls.truncate() // every document goes; the definition and indexes stay
await calls.drop()     // the collection, its definition and its watch consumers go
```

- Both take the same time for 10 or 10 million documents. Watch consumers get one event with `op: 'truncate'` (`_id` is `''`), then the new changes.
- After `drop()`, the next call on the collection declares the collection again. A collection declared in `ndb.yml` comes back, empty, when the daemon restarts.

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

A query is lazy and immutable. It is also thenable: `await` runs it.

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

- Operators: `$eq`, `$ne`, `$gt`, `$gte`, `$lt`, `$lte`, `$in`, `$nin`, `$exists`, `$elemMatch`, and date math on the time field (`{ at: { $gte: 'now-7d/d' } }`). Several `query` calls are ANDed. Dot paths work on `json` fields.
- `$regex` (with `$options` `i`, `m`, `s`, `x`) uses Go RE2: no backreferences, no look-around. Pass a string, or a `RegExp` (`{ name: /^al/i }` or `{ name: { $regex: /^al/i } }`). Only the `i`, `m` and `s` flags are accepted; any other flag (`g`, `y`, `u`, ...) throws `InvalidQueryError` before the request. An anchored prefix such as `^abc` can use an index.
- The order of the `sort` keys is the order of the sort. The id is always the last key.
- `fields(...)` returns only these fields. The rows are `Partial`.
- `page(n, size)` sets `skip` and `limit` for page `n` (from 1).
- `first()` returns the first row, or `null`.
- The collection has the same shortcuts: `calls.query(...)`, `calls.sort(...)`, `calls.all()`, `calls.count(...)`, `calls.each()`.

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

`ErrorCode` is the union of the known codes.

## Testing

The tests of this package (vitest) build the daemon from the repository (`go build ./cmd/ndb`) in a global setup. They start it on a temp unix socket and a free TCP port, wait for `GET /_health` to return 200, and stop it at the end. To do the same by hand:

```sh
ndb -config /nonexistent.yml -data-dir "$(mktemp -d)" -unix /tmp/ndb-test.sock -tcp 127.0.0.1:7070
```

Add `-token-file <path>` to ask for a bearer token on TCP. Use a short socket path: macOS allows 104 bytes. Use a new collection name per test.
