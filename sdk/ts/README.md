# @thadeu/ndb

<img src="https://raw.githubusercontent.com/thadeu/ndb/main/brand/png/lockup-on-light.png" width="220">

TypeScript client for [ndb](https://github.com/thadeu/ndb), an NDJSON query daemon. Typed models, a chainable query builder, aggregate and watch. The client builds JSON. The daemon decides what every query means.

Node 24 or newer. ESM and CJS. One runtime dependency (`undici`).

Docs: [client guide](https://github.com/thadeu/ndb/blob/main/docs/dev/clients.md#typescript-package-thadeundb), [query language](https://github.com/thadeu/ndb/blob/main/docs/dev/query.md), [HTTP API](https://github.com/thadeu/ndb/blob/main/docs/dev/http-api.md).

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

## Define a model

```ts
const calls = ndb.model('calls', {
  id: 'call_id',
  time: 'at',
  fields: {
    call_id: { type: 'string' },
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
- `id` is the id field. Without it the id is `_id`, made by the daemon.
- `time` is the collection time field. Used by zone maps and `$dateTrunc`.
- The model declares its collection on first use (`ensure_collection`). You can call `await calls.ready()` to do it at once.
- Types come from the schema. Rows have the schema fields and `_v`, the document version. A `date` field is a `Date` in rows. Input takes `Date` values and ISO strings in filters.
- The client has no `expires` or `ttl` option and no blob API. Use the HTTP API for those.

## Insert, find, update, delete

```ts
// Insert one or many. All documents go in one transaction.
const results = await calls.insert([
  { call_id: 'c1', status: 'answered', caller: 'a', duration: 30, at: new Date() },
  { call_id: 'c2', status: 'failed', caller: 'b', at: new Date() },
])
// results: [{ id: 'c1', v: 1 }, ...]

// Find by id. Returns null when missing.
const row = await calls.find('c1')

// Update with optimistic locking.
await calls.update('c1', { status: 'done' }, { ifVersion: row!._v })

// Remove a field.
await calls.update('c1', {}, { unset: ['duration'] })

// Delete. ifVersion is optional here too.
await calls.delete('c1', { ifVersion: 3 })
```

`update` returns `{ id, v }` with the new version. A stale `ifVersion` throws `VersionConflictError`:

```ts
import { VersionConflictError } from '@thadeu/ndb'

try {
  await calls.update('c1', { status: 'x' }, { ifVersion: row!._v })
} catch (err) {
  if (err instanceof VersionConflictError) {
    // Someone else wrote first. Read again and retry.
  }
}
```

An insert with a duplicate id throws `DuplicateIdError`. A duplicate `unique` value throws `UniqueViolationError`. An insert without the id when the schema names one fails with code `missing_id`.

## Query

A query is lazy and immutable. It is also thenable: `await` runs it.

```ts
const rows = await calls
  .where({ status: 'answered', duration: { $gte: 30 } })
  .where({ caller: { $in: ['a', 'b'] } })
  .not({ caller: 'zzz' })
  .or({ duration: { $lt: 5 } }, { 'meta.agent.id': 7 })
  .sort({ at: -1, call_id: 1 })
  .fields('call_id', 'status')
  .limit(50)
  .skip(100)
```

- Operators: `$eq`, `$ne`, `$gt`, `$gte`, `$lt`, `$lte`, `$in`, `$nin`, `$exists`. Several `where` calls are ANDed. Dot paths work on `json` fields.
- `$regex` (with `$options` `i`, `m`, `s`, `x`) uses Go RE2: no backreferences, no look-around. Pass a string, or a `RegExp` (`{ name: /^al/i }` or `{ name: { $regex: /^al/i } }`). Only the `i`, `m` and `s` flags are accepted; any other flag (`g`, `y`, `u`, ...) throws `InvalidQueryError` before the request. An anchored prefix such as `^abc` can use an index.
- The order of the `sort` keys is the order of the sort. The id is always the last key.
- `fields(...)` returns only these fields. The rows are `Partial`.
- `page(n, size)` sets `skip` and `limit` for page `n` (from 1).
- `first()` returns the first row, or `null`.
- The model has the same shortcuts: `calls.where(...)`, `calls.sort(...)`, `calls.all()`, `calls.count()`.

### Count

```ts
const answered = await calls.where({ status: 'answered' }).count()
const total = await calls.count()
```

### Cursor pagination

Offset pages get slower as `skip` grows. Use a cursor for deep pages.

```ts
const q = calls.where({ status: 'answered' }).sort({ call_id: 1 }).limit(100)
const page = await q
const token = q.nextCursor // '' on the last page

const next = await calls.where({ status: 'answered' }).sort({ call_id: 1 }).limit(100).after(token)
```

A token works only with the same `where` and `sort`. Another query shape throws `CursorError`.

To walk every row, use `iterate()`. It follows the cursor, with `limit` as the batch size:

```ts
for await (const row of calls.where({ status: 'answered' }).sort({ call_id: 1 }).limit(500).iterate()) {
  console.log(row.call_id)
}
```

### Explain

There is no explain method. Send `explain: true` in a raw request and read the header:

```ts
const res = await ndb.stream('POST', calls.path('find'), { where: { status: 'failed' }, explain: true, limit: 1 })

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

A watch streams the changes of a model to a named consumer. First it sends the events after the last ack. Then it sends new events as they happen.

```ts
import { HistoryTruncatedError, WatchLaggedError } from '@thadeu/ndb'

const controller = new AbortController()

try {
  for await (const ev of calls.watch('relay', { where: { status: 'answered' }, signal: controller.signal })) {
    // ev: { consumer, pos, op: 'put' | 'del', id, v, doc }
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

- Options: `from: 'now'` (a new consumer starts at the end), `limit` (stop after n events), `where` (same filters as a query), `signal` (an `AbortSignal` that stops the stream).
- A delete event has `doc: undefined`. With `where`, a delete is sent only if the same stream saw an earlier matching put of that id.
- Delivery is at least once. After a restart an event can come again, so make the work safe to repeat.
- `ack` stores the position. The next `watch` with the same consumer name starts after it.
- `HistoryTruncatedError` means the WAL files for the position were deleted (`wal_retention`).

## Errors

Daemon errors are `NdbError` or a subclass. They have `code` (the daemon code) and `status` (the HTTP status; 0 for an error line inside a stream).

| Class | Codes |
|---|---|
| `UnauthorizedError` | `unauthorized` |
| `NotFoundError` | `not_found`, `unknown_collection` |
| `VersionConflictError` | `version_conflict` |
| `DuplicateIdError` | `duplicate_id` |
| `UniqueViolationError` | `unique_violation` |
| `InvalidQueryError` | `invalid_query`, `invalid_pipeline` |
| `CursorError` | `cursor_invalid`, `cursor_mismatch`, `cursor_expired` |
| `WatchLaggedError` | `watch_lagged` |
| `HistoryTruncatedError` | `history_truncated` |
| `NdbError` | every other code: `not_ready`, `catalog_conflict`, `catalog_invalid`, `direct_collection`, `invalid_request`, `group_limit_exceeded`, `request_too_large`, `line_too_large`, `closed`, `invalid_position`, `http_status`, ... |

```ts
import { NdbError } from '@thadeu/ndb'

try {
  await calls.find('c1')
} catch (err) {
  if (err instanceof NdbError && err.code === 'not_ready') {
    // The daemon is still recovering, and readyTimeout ran out.
  }
}
```

`ErrorCode` is the union of the known codes.

## Testing

The tests of this package (vitest) build the daemon from the repository (`go build ./cmd/ndb`) in a global setup. They start it on a temp unix socket and a free TCP port, wait for `GET /health` to return 200, and stop it at the end. To do the same by hand:

```sh
ndb -config /nonexistent.yml -data-dir "$(mktemp -d)" -unix /tmp/ndb-test.sock -tcp 127.0.0.1:7070
```

Add `-token-file <path>` to ask for a bearer token on TCP. Use a short socket path: macOS allows 104 bytes. Use a new collection name per test.

## License

MIT.
