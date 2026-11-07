# Collection routes

Declare, read, empty and remove a collection, and manage its indexes. `{coll}` is the collection name (`^[a-z0-9][a-z0-9_-]{0,63}$`). The rules that apply to every route are on [HTTP API](../http.md).

In the examples, `calls` is a collection handle: `ndb.Collection[Call](db, "calls")` in Go, `db.collection("calls", definition)` in Ruby, `db.collection('calls', schema)` in TypeScript. The SDKs send `PUT /{coll}` themselves on the first call of a handle.

## `PUT /{coll}`

Creates the collection, or merges the definition into it (`ensure_collection`). It is idempotent: send it at every start of an app.

| Body key | Type | Default | Meaning |
|---|---|---|---|
| `time` | string | none | the time field: RFC 3339 compare as instants, date math, zone maps, `$dateTrunc`, `ttl` |
| `indexes` | string[] | `[]` | fields with a secondary index (dot paths allowed) |
| `unique` | string[] | `[]` | fields whose value must be unique (`409 unique_violation`) |
| `write` | string | `engine` | `engine` (ndb writes) or `direct` (apps append NDJSON files) |
| `path` | string | none | the files of a `direct` collection (a glob) |
| `durability` | string | `strict` | `strict` (sync the WAL before the ack) or `relaxed` |
| `retention` | duration | none | hide and later drop rows whose `time` is older |
| `wal_retention` | duration | none | delete sealed WAL files older than this |
| `ttl` | duration | none | documents expire this long after `time` |
| `expires_field` | string | none | the field that holds the expiry time of each document |
| `max_line_size` | bytes | 1 MiB | the largest document and request body |
| `blob_threshold` | bytes | none | a string field longer than this goes to the blob store |

**Merge rules:** `indexes` and `unique` are united; a different `write`, `path` or `time` is `409 catalog_conflict`; the key `id` is `400 catalog_invalid` (the key is always `_id`); `ndb.yml` wins for `durability`, `retention` and `wal_retention`. See [Collection keys](../configuration.md#collection-keys).

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

The definition and the state of each index, like the answer of `PUT`.

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

## `PUT /{coll}/_indexes/{field}`

Adds a secondary index on `field` (a dot path) and builds it in the background. Writes keep it up to date during the build; reads use it when it is `ready`.

**Answer:** `{"collection":"calls","indexes":["status","dnis"]}`. **Errors:** `404 unknown_collection`, `invalid_index`.

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

One element of [GET /_stats](system.md#get-stats): `name`, `write`, `rows`, `quarantined`, `indexes` (each `building` or `ready`, with `done` and `total`), `segments`, `wal_files`, `bytes` and `total`.

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

**Answer:** `{"collection","before":{"segments","bytes"},"after":{"segments","bytes"},"duration_ms"}`. **Errors:** `400 not_supported` for a direct collection.

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
