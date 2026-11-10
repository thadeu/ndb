# Stream routes

The wire contract of [streams](../streams.md): an append-only log with
consumer groups, with the behavior of Redis Streams and ndb names. Every
route is under one prefix, `/{s}/_streams/...`, where `{s}` is the stream
(a collection made with `"stream": true`).

Bodies and answers are JSON. Errors have the body of every route (see
[Errors](errors.md)).

## Entry IDs

An entry ID is `<ms>-<seq>`: two unsigned 64-bit integers, as text, for
example `1731160000000-0`.

- The server makes the ID from its clock. `ms` is the Unix time in
  milliseconds; `seq` counts the entries of the same millisecond, from 0.
- IDs always grow. A clock that goes back keeps the last `ms` and adds 1 to
  `seq`.
- The ID is the `_id` of the entry. On a stream, `_id` sorts and compares as
  an ID (`ms`, then `seq`), not as text: `1-9` comes before `1-10`. This is
  true in `sort`, in range filters (`$gt`, `$gte`, `$lt`, `$lte`) and in
  cursors.
- Special values in requests:

| Value | Meaning |
|---|---|
| `*` | append: the server makes the ID |
| `<ms>-*` | append: this `ms`, the server makes `seq` |
| `$` | the last ID of the stream (group start, `setid`) |
| `0` or `0-0` | before the first entry |
| `-`, `+` | the lowest and the highest ID (ranges) |

An ID without `-seq` (`1731160000000`) means `<ms>-0` as a start and
`<ms>-<max>` as an end, as in Redis.

## Entries

An entry is a JSON object with any fields (nested values are allowed, unlike
Redis), plus:

- `_id`: the entry ID.
- `_ts`: the `ms` of the ID as an RFC 3339 time. It is the `time` field of
  the stream, so `retention`, the zone maps and time filters work.

A field name that starts with `_` is reserved, as in every collection.

## Stream

### `PUT /{s}` with `"stream": true`

Makes a stream (or checks that it is one). The first append makes it too.
The definition takes the keys of every collection (`indexes`,
`retention`, `durability`, ...) except `time` (it is `_ts`) and `unique`.

A collection that exists and is not a stream: `400 not_a_stream`. A stream
cannot become a normal collection.

### `POST /{s}/_streams/append`

Redis: `XADD`.

```json
{ "doc": { "order": 42, "total": 99.9 }, "id": "*", "trim": { "max_len": 10000, "approx": true } }
```

```json
{ "_id": "1731160000000-0" }
```

- `doc` is the entry. `docs` (an array) appends many in order and answers
  `{ "ids": [...] }`; one fsync covers them all.
- `id`: `*` (default), `<ms>-*` or `<ms>-<seq>`. An ID that is not greater
  than the last one: `400 stream_id_too_small`. `0-0`: `400
  invalid_stream_id`.
- `trim`: optional, as in `trim` below, run after the append.
- `no_create: true`: do not make the stream (`NOMKSTREAM`); a stream that
  does not exist is `404 unknown_collection`.

### `POST /{s}/_streams/trim`

Redis: `XTRIM`.

```json
{ "max_len": 10000, "approx": true }
```

```json
{ "deleted": 1520 }
```

- `max_len`: keep the newest N entries. `min_id`: delete the entries with an
  ID lower than this one. One of the two.
- `approx: true` deletes only whole blocks of 1 024 entries (`~` in
  Redis): the stream keeps up to 1 023 more entries than asked, never fewer.
  A stream trimmed on every append then deletes once per block, not once per
  append. Default `false` (exact).
- `limit`: with `approx`, delete at most this many entries.
- A trim does not change the groups: a pending entry that the trim deleted
  stays in the PEL, and `claim` gives its ID in `deleted`.

### `GET /{s}/_streams`

Redis: `XINFO STREAM`, `XLEN`.

```json
{
  "length": 128000,
  "first_id": "1731160000000-0",
  "last_id": "1731163600000-4",
  "entries_added": 130000,
  "max_deleted_id": "1731160300000-0",
  "groups": 2
}
```

- `entries_added` counts every entry ever appended, deleted ones too.
- `max_deleted_id` is the highest ID deleted one by one (`DELETE
  /{s}/_doc/{id}`, `XDEL`). A trim does not change it, as in Redis.

### Read, count and delete entries

These are the routes of every collection, on the stream:

| Redis | ndb |
|---|---|
| `XRANGE s a b COUNT n` | `POST /{s}/_search {"query":{"_id":{"$gte":"a","$lte":"b"}},"sort":{"_id":1},"limit":n}` |
| `XREVRANGE s b a COUNT n` | the same with `"sort":{"_id":-1}` |
| `XLEN s` | `POST /{s}/_count` (or `length` above) |
| `XDEL s id` | `DELETE /{s}/_doc/{id}` |
| `XREAD STREAMS s id` | `_search` with `{"_id":{"$gt":"id"}}` and `sort` on `_id` |

## Groups

A group reads the whole stream. Each entry goes to one consumer of the group
and stays in the group's **PEL** (pending entries list) until it is acked.

A group has: `last_delivered_id`, `entries_read` (for the lag), and the PEL:
for each pending entry, its consumer, the time of its last delivery and its
delivery count. All of it is on disk before the answer (`durability` of the
stream; `strict` by default).

### `PUT /{s}/_streams/groups/{g}`

Redis: `XGROUP CREATE`.

```json
{ "start": "$" }
```

```json
{ "group": "workers", "last_delivered_id": "1731163600000-4" }
```

- `start`: `$` (only new entries, default), `0` (every entry) or an ID.
- `entries_read`: optional, for the lag (`ENTRIESREAD`).
- The stream is made if it does not exist (`MKSTREAM` is the default);
  `no_create: true` turns it off.
- A group that exists: `409 group_exists`.

### `POST /{s}/_streams/groups/{g}/setid`

Redis: `XGROUP SETID`.

```json
{ "id": "0", "entries_read": 0 }
```

### `DELETE /{s}/_streams/groups/{g}`

Redis: `XGROUP DESTROY`.

Deletes the group and its PEL. Answers `{ "deleted": true }`.

### `PUT /{s}/_streams/groups/{g}/consumers/{c}`

Redis: `XGROUP CREATECONSUMER`.

Answers `{ "created": true }`, or `false` if it exists. A read makes the
consumer too.

### `DELETE /{s}/_streams/groups/{g}/consumers/{c}`

Redis: `XGROUP DELCONSUMER`.

Deletes the consumer and its pending entries. Answers `{ "pending": 3 }`,
the number of entries it held.

### `GET /{s}/_streams/groups`

Redis: `XINFO GROUPS`.

```json
[{ "name": "workers", "consumers": 3, "pending": 12, "last_delivered_id": "1731163600000-4", "entries_read": 129988, "lag": 12 }]
```

`lag` is `null` when it cannot be known (after a trim or a delete in the
part the group did not read, as in Redis).

### `GET /{s}/_streams/groups/{g}/consumers`

Redis: `XINFO CONSUMERS`.

```json
[{ "name": "w1", "pending": 4, "idle_ms": 1200, "inactive_ms": 1100 }]
```

`idle_ms` is the time since the last read or claim of the consumer;
`inactive_ms` since the last read that gave entries.

### `POST /{s}/_streams/groups/{g}/read`

Redis: `XREADGROUP`.

```json
{ "consumer": "w1", "count": 100, "block_ms": 5000 }
```

```json
[{ "_id": "1731163600000-5", "_ts": "2026-11-09T15:00:00Z", "order": 43 }]
```

- New entries (`>` in Redis, the default): the entries after
  `last_delivered_id`, in ID order. Each one goes into the PEL of this
  consumer with a delivery count of 1, and `last_delivered_id` moves.
- `pending: true` (`0` in Redis): the entries of this consumer's PEL, with
  an ID greater than `after` (default `0`). Each one counts as a new
  delivery, as in Redis: delivery count + 1 and a new delivery time. An entry deleted from the stream comes as `{"_id": "...",
  "_deleted": true}`.
- `count`: the most entries to give (default 10, at most 10 000).
- `block_ms`: with no new entry, wait up to this long for one (`BLOCK`). `0`
  answers at once (the default). An empty answer is `[]`. Not with
  `pending`.
- `no_ack: true`: do not add the entries to the PEL (`NOACK`): at most
  once.
- `fields`: give only these fields (and `_id`).
- The answer is the full entries, as JSON or as NDJSON with `Accept:
  application/x-ndjson`, like `_search`.
- The consumer is made on its first read.

### `POST /{s}/_streams/groups/{g}/ack`

Redis: `XACK`.

```json
{ "ids": ["1731163600000-5", "1731163600000-6"] }
```

```json
{ "acked": 2 }
```

An ID that is not pending in the group counts 0; it is not an error.

### `POST /{s}/_streams/groups/{g}/pending`

Redis: `XPENDING`.

Summary (an empty body):

```json
{ "count": 12, "lowest": "1731163600000-1", "highest": "1731163600000-12", "consumers": [{ "name": "w1", "count": 4 }] }
```

List (with `count`):

```json
{ "start": "-", "end": "+", "count": 100, "consumer": "w1", "idle_ms": 60000 }
```

```json
[{ "_id": "1731163600000-1", "consumer": "w1", "idle_ms": 61000, "deliveries": 2 }]
```

`consumer` and `idle_ms` are optional filters.

### `POST /{s}/_streams/groups/{g}/claim`

Redis: `XCLAIM`, `XAUTOCLAIM`.

By IDs (`XCLAIM`):

```json
{ "consumer": "w2", "min_idle_ms": 60000, "ids": ["1731163600000-1"] }
```

With a scan (`XAUTOCLAIM`), instead of `ids`:

```json
{ "consumer": "w2", "min_idle_ms": 60000, "cursor": "0-0", "count": 100 }
```

```json
{ "cursor": "1731163600000-9", "entries": [{ "_id": "1731163600000-1", "order": 40 }], "deleted": [] }
```

- Only the entries idle for at least `min_idle_ms` move to `consumer`. Each
  one gets a delivery count + 1 and a new delivery time.
- `cursor` is `0-0` when the scan is complete (only with a scan).
- `deleted`: pending IDs whose entry is no longer in the stream; they leave
  the PEL.
- `just_ids: true` (`JUSTID`): `entries` are IDs only, and the delivery
  count does not change.
- By IDs only: `idle_ms`, `retry_count` and `force` as in `XCLAIM`.

## Errors

| Code | HTTP | Meaning |
|---|---|---|
| `not_a_stream` | 400 | A `_streams` route, or `"stream": true`, on a collection that is not a stream. |
| `invalid_stream_id` | 400 | An ID that is not `<ms>-<seq>`, a special value where it is not allowed, or `0-0` in an append. |
| `stream_id_too_small` | 400 | An append with an ID that is not greater than the last one. |
| `unknown_group` | 404 | The group does not exist. |
| `group_exists` | 409 | A group create with a name that exists. |

## Access

`append`, `trim`, every group change, `read` (it moves the PEL), `ack` and
`claim` are writes. `GET /{s}/_streams`, the `GET` of groups and consumers,
and `pending` are reads.
