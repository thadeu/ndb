# Collection routes

Declare, read, empty and remove a collection, and manage its indexes. `{coll}` is the collection name (`^[a-z0-9][a-z0-9_-]{0,63}$`). The rules that apply to every route are on [HTTP API](../http.md).

A [collection pattern](queries.md#collection-patterns) (`logs-*`) works only on `GET /{coll}`, `_search`, `_count` and `_aggregate`. Every other route is `400 pattern_read_only`, also the reads `GET /{coll}/_stats`, `_pitr` and `_quarantine`.

In the examples, `calls` is a collection handle: `ndb.Collection[Call](db, "calls")` in Go, `db.collection("calls", definition)` in Ruby, `db.collection('calls', schema)` in TypeScript. The SDKs send `PUT /{coll}` themselves on the first call of a handle.

## `PUT /{coll}`

Creates the collection, or merges the definition into it (`ensure_collection`). It is idempotent: send it at every start of an app.

| Body key | Type | Default | Meaning |
|---|---|---|---|
| `time` | string | none | the time field: RFC 3339 compare as instants, date math, zone maps, `$dateTrunc`, `ttl` |
| `indexes` | string[] | `[]` | fields with a secondary index (dot paths allowed). An entry with commas is a [compound index](../query.md#indexes) on 2 to 4 fields: `"account_id,created_at"` |
| `unique` | string[] | `[]` | fields whose value must be unique (`409 unique_violation`). An entry with commas is a compound key (`"account_id,email"`): the tuple of values must be unique. A field added by a later `PUT` is enforced at once: when the data already holds a value twice, the `PUT` is `409 unique_violation` (the message gives the field, the value and two ids) and the definition does not change. Else the collection opens again with the new field, so its open `_watch` streams end and their consumers connect again from their position. |
| `encrypted` | string[] | `[]` | top-level fields that the daemon encrypts before the WAL (AES-256-GCM). A read with `?decrypt=1` by a caller with the `decrypt` right gets the plain value; any other read gets `"ndb:e1:k1:..."`. Engine mode only; not `_id`, the `time` field, `expires_field`, an indexed field, or a stream. A field added later is encrypted in the old documents by a background job (`"encrypting"` in the answer). See [Encrypted fields](../security.md#encrypted-fields) |
| `blind` | object or string[] | `{}` | an encrypted field and its normalization (`exact`, `lower`, `upper`, `numeric`): `{"cpf":"numeric"}`, or a list of `exact` fields; the field is also encrypted. The daemon also keeps a hash of the value, so `$eq`, `$in`, `$ne`, `$nin`, `indexes` and `unique` work on the field. See [Blind fields](../security.md#blind-fields) |
| `write` | string | `engine` | `engine` (ndb writes) or `direct` (apps append NDJSON files) |
| `path` | string | none | the files of a `direct` collection (a glob) |
| `durability` | string | `strict` | `strict` (sync the WAL before the ack) or `relaxed` |
| `retention` | duration | none | hide and later drop rows whose `time` is older |
| `wal_retention` | duration | none | delete sealed WAL files older than this |
| `ttl` | duration | none | documents expire this long after `time` |
| `expires_field` | string | none | the field that holds the expiry time of each document |
| `max_line_size` | bytes | 1 MiB | the largest request body (`413 request_too_large`) and document: in engine mode, a write that makes a stored document larger (an update) is `413 line_too_large`; the WAL line has room for its record around the document. In direct mode, a longer line goes to quarantine |
| `blob_threshold` | bytes | none | a string field longer than this goes to the blob store |
| `partition_by` | string | none | `day`, `week`, `month` or `year`: a [partitioned collection](#partitioned-collections); needs `time` |
| `pitr` | string | daemon `pitr` (`1d`) | engine mode: the [PITR](../pitr.md) window, `off` or a duration up to `7d` |
| `stream` | bool | `false` | make a [stream](streams.md) (engine mode, no `unique`, `partition_by`, `ttl` or `expires_field`); set only at creation |

**Merge rules:** `indexes` and `unique` are united; a key that is set (`write`, `path`, `time`, `ttl`, `expires_field`) cannot change: a different value is `409 catalog_conflict`, and a key that is not set yet can be added; `partition_by` is set only at creation (`409 catalog_conflict` on a collection that exists); `partition_of` is set by ndb on each partition and is not a key of `PUT` (`400 invalid_request`); the key `id` is `400 catalog_invalid` (the key is always `_id`); `"stream": true` on a collection that is not a stream is `400 not_a_stream`; `ndb.yml` wins for `durability`, `retention`, `wal_retention` and `pitr`. See [Collection keys](../configuration.md#collection-keys).

**Answer:** `{"definition":{...},"indexes":[{"field","state","done","total"}]}`: the final definition and the state of each index (`building` or `ready`).

**Errors:** `catalog_conflict`, `catalog_invalid`, `invalid_request`. A pattern is `pattern_read_only`.

::: code-group

```bash [curl]
curl --unix-socket /run/ndb/ndb.sock -X PUT http://ndb/calls \
  -d '{"time":"ts","indexes":["status"],"unique":["call_id"]}'
```

```go [Go]
type Call struct {
	ID     string    `ndb:"_id"`
	CallID string    `ndb:"call_id,unique"`
	Status string    `ndb:"status,index"`
	TS     time.Time `ndb:"ts,time"`
}

calls, err := ndb.Collection[Call](db, "calls") // declared on its first call
```

```ruby [Ruby]
calls = db.collection("calls", time: "ts", indexes: ["status"], unique: ["call_id"])
```

```ts [TypeScript]
const calls = db.collection('calls', {
  time: 'ts',
  fields: { call_id: { type: 'string', unique: true }, status: { type: 'string', index: true }, ts: { type: 'date' } },
})
```

:::

An unknown collection is also created by the first `POST /{coll}/_doc` or `_bulk`, with the default definition, unless `allow_implicit: false`.

## `GET /{coll}`

The definition and the state of each index, like the answer of `PUT`. While the daemon encrypts the old documents of a field added to `encrypted` or `blind`, the answer also has `"encrypting":[{"field","state","done","total"}]` (see [A field added later](../encryption.md#a-field-added-later)).

With a [collection pattern](queries.md#collection-patterns) (`GET /logs-2026-*`) it lists the names it matches: `{"collections":["logs-2026-10","logs-2026-11"]}`.

**Errors:** `404 unknown_collection`; `invalid_pattern`, `pattern_too_wide` for a pattern.

```bash
curl --unix-socket /run/ndb/ndb.sock http://ndb/calls
curl --unix-socket /run/ndb/ndb.sock http://ndb/logs-2026-*
```

## `DELETE /{coll}`

Drops the collection: its documents, its definition and the positions of its watch consumers. It takes a few ms for any size: ndb swaps the collection dir and deletes the old one in the background.

**Query string:** `confirm={coll}`, the collection name, on purpose.

**Answer:** `{"collection":"calls","dropped":true}`.

**Errors:** `400 confirm_required` (missing or another name), `404 unknown_collection`, `409 direct_collection`.

::: code-group

```bash [curl]
curl --unix-socket /run/ndb/ndb.sock -X DELETE 'http://ndb/calls-2026-09?confirm=calls-2026-09'
```

```go [Go]
err := calls.Drop(ctx)
```

```ruby [Ruby]
calls.drop
```

```ts [TypeScript]
await calls.drop()
```

:::

Calls in flight finish first; new calls wait for the swap. A collection declared in `ndb.yml` comes back, empty, at the next start. See [Truncate and drop](../operations.md#truncate-and-drop).

## `POST /{coll}/_truncate`

Removes every document and keeps the definition and indexes. Same cost and rules as `DELETE /{coll}`. Watch consumers get one `truncate` event, then the new changes.

**Query string:** `confirm={coll}`. **Answer:** `{"collection":"calls","truncated":true}`. **Errors:** as for `DELETE /{coll}`.

::: code-group

```bash [curl]
curl --unix-socket /run/ndb/ndb.sock -X POST 'http://ndb/calls/_truncate?confirm=calls'
```

```go [Go]
err := calls.Truncate(ctx)
```

```ruby [Ruby]
calls.truncate
```

```ts [TypeScript]
await calls.truncate()
```

:::

## `POST /{coll}/_rename`

Gives the collection a new name: its documents, indexes and watch positions move with it. A partitioned collection renames its partitions. With `replace`, an existing `to` goes to the trash first, where it stays restorable for its [PITR window](../pitr.md).

**Body:** `{"to":"calls","replace":true}`. **Answer:** `{"collection":"calls","renamed_from":"calls_restored","replaced":true}`.

**Errors:** `400 catalog_invalid` (bad name, a partition, the same name, or a `to` that is a partition name `{logical}-{period}` of a partitioned collection), `404 unknown_collection`, `409 collection_exists` (without `replace`; and for a partitioned collection, when a collection holds a partition name `{to}-{period}` and is not a partition that the `replace` sends to the trash). Every check runs before any change. A scoped token needs `admin` on both names.

::: code-group

```bash [curl]
curl --unix-socket /run/ndb/ndb.sock -X POST http://ndb/calls_restored/_rename -d '{"to":"calls","replace":true}'
```

```go [Go]
err := restored.Rename(ctx, "calls", true)
```

```ruby [Ruby]
client.rename("calls_restored", "calls", replace: true)
```

```ts [TypeScript]
await restored.rename('calls', { replace: true })
```

:::

## `POST /{coll}/_restore`

Makes the collection `to` hold `coll` as it was at `at`, from its [PITR window](../pitr.md) (1 day by default). The source is not touched. A dropped, truncated or replaced collection can be restored by its name while it is in the window.

**Body:** `{"at":"2026-11-12T14:04:00Z","to":"calls_restored"}`. **Answer:** `{"collection":"calls_restored","restored":"calls","at":"…","duration_ms":840}`.

**Errors:** `400 out_of_window` (the message gives the start of the window), `400 pitr_off`, `400 catalog_invalid` (bad `to`, a `to` equal to `coll`: restore into a new name, then rename it with `replace`, or a `to` that is a partition name of a partitioned collection), `404 unknown_collection`, `409 collection_exists` (`to` exists, or for a partitioned collection a collection holds a name `{to}-{period}`). A scoped token needs `admin` on `coll` and `to`.

::: code-group

```bash [curl]
curl --unix-socket /run/ndb/ndb.sock -X POST http://ndb/calls/_restore -d '{"at":"2026-11-12T14:04:00Z","to":"calls_restored"}'
```

```go [Go]
err := calls.Restore(ctx, at, "calls_restored")
```

```ruby [Ruby]
client.restore("calls", at: at, to: "calls_restored")
```

```ts [TypeScript]
await calls.restore({ at, to: 'calls_restored' })
```

:::

## `GET /{coll}/_pitr`

The PITR window of the collection: `{"collection":"calls","window":"1d","oldest":"…","newest":"…","snapshots":2,"bytes":18874368}`. `oldest` is the first instant a restore reaches; `bytes` is the disk the window holds beyond the live files. `dropped: true` for a collection that is only in the trash. A collection with `pitr: off` has `"window":"off"`.

## `PUT /{coll}/_indexes/{field}`

Adds a secondary index on `field` (a dot path) and builds it in the background. Writes keep it up to date during the build; reads use it when it is `ready`. A `field` with commas adds a [compound index](../query.md#indexes): `PUT /orders/_indexes/account_id,created_at`. `DELETE` takes the same name.

**Answer:** `{"collection":"calls","indexes":["status","dnis"]}`. **Errors:** `404 unknown_collection`. An empty `field` has no route (`404 unknown_route`); an empty field in the `indexes` of `PUT /{coll}` is `400 catalog_invalid`. A compound name that is not 2 to 4 different fields, or that holds `_id`, is `400 invalid_index` here and `400 catalog_invalid` in `PUT /{coll}`.

::: code-group

```bash [curl]
curl --unix-socket /run/ndb/ndb.sock -X PUT http://ndb/calls/_indexes/dnis
```

```go [Go]
fields, err := calls.EnsureIndex(ctx, "dnis")
```

```ruby [Ruby]
calls.ensure_index("dnis")
```

```ts [TypeScript]
await calls.ensureIndex('dnis')
```

:::

## `DELETE /{coll}/_indexes/{field}`

Removes the index; reads stop using it at once. **Answer:** `{"collection","indexes"}`.

::: code-group

```bash [curl]
curl --unix-socket /run/ndb/ndb.sock -X DELETE http://ndb/calls/_indexes/dnis
```

```go [Go]
fields, err := calls.DropIndex(ctx, "dnis")
```

```ruby [Ruby]
calls.drop_index("dnis")
```

```ts [TypeScript]
await calls.dropIndex('dnis')
```

:::

## `GET /{coll}/_stats`

One element of [GET /_stats](system.md#get-stats): `name`, `write`, `rows`, `quarantined`, `indexes` (each `building` or `ready`, with `done` and `total`), `encrypting` (the same shape, only while a field added to `encrypted` is encrypted in the old documents), `segments`, `wal_files`, `bytes`, `total` and `asleep`.

::: code-group

```bash [curl]
curl --unix-socket /run/ndb/ndb.sock http://ndb/calls/_stats
```

```go [Go]
stats, err := calls.Stats(ctx)
```

```ruby [Ruby]
calls.stats
```

```ts [TypeScript]
const stats = await calls.stats()
```

:::

## `POST /{coll}/_compact`

Seals the memtable, then merges every segment into one, and waits for the end. The sealer also compacts in the background; this route is for maintenance.

**Answer:** `{"collection","before":{"segments","bytes"},"after":{"segments","bytes"},"duration_ms"}`. **Errors:** `404 unknown_collection`. A direct collection is compacted like an engine one.

::: code-group

```bash [curl]
curl --unix-socket /run/ndb/ndb.sock -X POST http://ndb/calls/_compact
```

```go [Go]
report, err := calls.Compact(ctx)
```

```ruby [Ruby]
calls.compact
```

```ts [TypeScript]
const report = await calls.compact()
```

:::

## `GET /{coll}/_quarantine`

The bad lines of a direct collection: `?limit=N` (default 100, at most 1000), oldest first.

**Answer:** `{"collection","total","rows":[{"ts","file","offset","error","line","truncated"}]}`. `total` is how many distinct lines the quarantine holds. `line` is cut to 2 KiB. An engine collection has no quarantine: `rows` is `[]`.

**Errors:** `400 invalid_request` for a `limit` that is not a positive integer.

```bash
curl --unix-socket /run/ndb/ndb.sock 'http://ndb/logs/_quarantine?limit=20'
```

`ndb quarantine <coll>` (CLI) prints the same.

## Partitioned collections

With `partition_by`, the collection is **logical**: it has a definition and no data. Its documents live in partitions, one per period of the `time` field, named `{coll}-{period}`:

| `partition_by` | Partition of 2026-11-07 |
|---|---|
| `day` | `calls-2026-11-07` |
| `week` | `calls-2026-w45` (ISO week) |
| `month` | `calls-2026-11` |
| `year` | `calls-2026` |

```bash
curl --unix-socket /run/ndb/ndb.sock -X PUT http://ndb/calls \
  -d '{"time":"ts","indexes":["status"],"partition_by":"month","retention":"180d"}'
# {"definition":{...},"partitions":[]}
```

You call the logical name, and ndb picks the partitions:

| Route on `/{coll}` | What happens |
|---|---|
| `_doc`, `_bulk` (and upsert) | each document goes to the partition of its time field, created at first use with the definition of the logical collection. A document without the time field (an RFC 3339 string) is `400 invalid_doc`. The `_id` is unique over all live partitions: an `_id` that another partition holds is `409 duplicate_id`; with `upsert`, the document merges into that partition, also when its time field is of another period. |
| `_search`, `_count`, `_aggregate` | read every live partition, like a [collection pattern](queries.md#collection-patterns); each row has `_coll` |
| `_doc/{id}`, `_update/{id}`, `_expire/{id}`, `_persist/{id}`, `_ttl/{id}`, `_lock/{id}`, `_unlock/{id}` | go to the partition that holds the id (newest first). An upsert of a new id goes to the partition of the time field of its operators, or of now. |
| `_update_by_query`, `_delete_by_query` | run on every live partition, newest first; without `many`, stop at the first partition that matched |
| `PUT /{coll}`, `_indexes/{field}` | change the logical definition and every partition: all keys (`indexes`, `unique`, `ttl`, `expires_field`, `durability`, `max_line_size`, `blob_threshold`, `pitr`, ...) |
| `GET /{coll}` | the definition and the partitions; `_stats`: rows and stats of each partition |
| `DELETE /{coll}?confirm=`, `_truncate?confirm=` | drop every partition (a drop also removes the definition) |
| `_watch`, `_ack`, `_compact`, `_quarantine` | `400 not_supported`: call one partition |

- **Retention:** with `retention`, a partition whose period **ended** more than `retention` ago leaves every read at once, and a background sweep (every minute) drops it, which costs a few ms for any size. A month partition with `"retention": "180d"` goes about six months after the month ends.
- **Partitions are collections:** you can read, compact or watch one by its name (`/calls-2026-11/_search`), or a period range with a pattern (`/calls-2026-*`).
- **`unique` is per partition**: two partitions can hold the same value (the `_id` is the exception, see above).
- **`_bulk` is all or nothing:** it writes one transaction per partition, but it first checks every document of every partition (time field, ids, unique values, the document itself) and writes nothing when one fails. It makes no partition before the checks pass. One narrow case stays: a write that fails after the checks (a disk error, or another call that takes the same `_id` or unique value between the check and the write) keeps the partitions written before it.
- **Partition names belong to the partitions:** an insert into a free partition name (`POST /calls-2026-12/_doc`) makes that partition, with the definition of the logical collection. `PUT` of a partition name that does not exist yet is `409 catalog_conflict` (declare the logical name), and so is a new logical collection when a collection already holds one of its partition names. A write to the logical name for a period whose name a plain collection holds (made by an older version) is `409 catalog_conflict`: rename or drop that collection.
- `partition_by` cannot change after the collection exists (`409 catalog_conflict`), and needs engine mode.
