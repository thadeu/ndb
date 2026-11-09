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
| `XREAD STREAMS s id` | `_search` with `{"_id":{"$gt":"id"}}` |
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

## Backup and PITR

- A [backup](operations.md) keeps the groups: their position and their
  pending entries, as they were when the backup ran. A pending entry that
  points to an entry appended after the backup's WAL mark comes back as a
  deleted entry (`claim` drops it).
- A PITR restore gives the entries as they were at the instant, not the
  groups: the PEL has no history of its changes. Make the groups again on
  the restored stream, with `start` at the last ID each one processed.

Not supported: a read of many streams in one call (`XREAD` / `XREADGROUP`
with many keys); `XSETID`; the Redis 8.2 options `KEEPREF`, `DELREF`,
`ACKED`, `XACKDEL` and `XDELEX` (a trim keeps the PEL references, as up to
Redis 8.0).
