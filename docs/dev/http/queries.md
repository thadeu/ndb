# Query routes

Search, count, aggregate, and update or delete by query. The filter is always the body key `query`, with the operators of [Filters](../query.md#filters): `$eq`, `$in`, `$gte`, `$regex`, `$elemMatch`, date math (`now-7d/d`) and the others. The rules that apply to every route (answer formats, headers, errors) are on [HTTP API](../http.md).

In the examples, `calls` is a collection handle (see [Collections](collections.md)).

## `POST /{coll}/_search`

Finds documents. No `query` matches every document.

| Body key | Type | Default | Meaning |
|---|---|---|---|
| `query` | object | `{}` | the filter, see [Filters](../query.md#filters) |
| `sort` | object | insertion order | `{"field": 1 or -1}`, in key order; `_id` is the last tie-break |
| `fields` | string[] | all | return only these paths (dot paths allowed); `_id` too is left out unless you list it |
| `limit` | int | none | rows of one page |
| `skip` | int | 0 | rows to skip (offset pages) |
| `after` | string | none | the `X-Ndb-Next` of the previous page (cursor pages) |
| `count` | bool | `false` | add `X-Ndb-Total`, the number of matches |
| `versions` | bool | `false` | add `_v` to each row |
| `snapshot` | bool | `false` | keep the first snapshot for 60 s so all pages see the same data |
| `explain` | bool | `false` | add `X-Ndb-Explain`, the plan counters |
| `lookup` | object | none | documents of other collections for each row, see [Lookup](../query.md#lookup) |

An unknown key is `invalid_query`. `?decrypt=1` in the URL gives the plain value of the [encrypted fields](../encryption.md#who-reads-the-plain-value) to a caller with the right; it works on `_search`, `_count`, `_aggregate` and the `doc` of the writes.

**Answer:** the rows, a JSON array (or NDJSON with `Accept: application/x-ndjson`), and the page headers `X-Ndb-Next`, `X-Ndb-Total`, `X-Ndb-Plan`, `X-Ndb-Examined`, `X-Ndb-Took-Ms`, `X-Ndb-Explain`. See [Answers](../http.md#answers).

**Errors:** `invalid_query`, `cursor_invalid`, `cursor_mismatch`, `cursor_expired`, `404 unknown_collection`; with a lookup, `lookup_invalid`, `lookup_unindexed`, `422 lookup_too_large`, `403 forbidden` (a scoped token that may not read the other collection).

::: code-group

```bash [curl]
curl --unix-socket /run/ndb/ndb.sock -i http://ndb/calls/_search -d '{
  "query": { "status": "answered", "ts": { "$gte": "now-7d/d" } },
  "sort": { "ts": -1 },
  "fields": ["call_id", "status"],
  "limit": 50,
  "count": true
}'
# X-Ndb-Total: 812
# X-Ndb-Next: eyJz...
# [{"call_id":"c-1","status":"answered"}, ...]
```

```go [Go]
rows, err := calls.
	Query(ndb.Eq("status", "answered"), ndb.Gte("ts", "now-7d/d")).
	Sort(ndb.Desc("ts")).
	Fields("call_id", "status").
	Limit(50).
	All(ctx)
```

```ruby [Ruby]
calls.query(status: "answered", ts: { "$gte" => "now-7d/d" })
     .sort(ts: -1).fields(:call_id, :status).limit(50).all
```

```ts [TypeScript]
const rows = await calls
  .query({ status: 'answered', ts: { $gte: 'now-7d/d' } })
  .sort({ ts: -1 })
  .fields('call_id', 'status')
  .limit(50)
  .all()
```

:::

**A body built elsewhere.** A search form or a saved search can send its JSON as is. The SDKs take the whole body (`Search`, `search`) or only the query (`RawQuery`, `rawQuery`, `raw_query`):

::: code-group

```go [Go]
page, err := calls.Search(ctx, body) // page.Rows, page.Next, page.Total

q, err := ndb.RawQueryJSON(formJSON)
rows, err := calls.Query(q, ndb.Eq("dnis", "1000")).All(ctx)
```

```ruby [Ruby]
page = calls.search(JSON.parse(body)) # page.rows, page.next, page.total

calls.raw_query(JSON.parse(form)).query(dnis: "1000").all
```

```ts [TypeScript]
const page = await calls.search(body) // rows, next, total

const rows = await calls.rawQuery(JSON.parse(form)).query({ dnis: '1000' }).all()
```

:::

The daemon validates the JSON, but it takes any field. For a public form, keep an allowlist of fields and operators, cap `limit`, and ask for an indexed field or a time window.

## `POST /{coll}/_count`

How many documents match.

| Body key | Type | Default | Meaning |
|---|---|---|---|
| `query` | object | `{}` | the filter |

The body may be empty. **Answer:** `{"count":812}`. **Errors:** `invalid_query`, `404 unknown_collection`.

::: code-group

```bash [curl]
curl --unix-socket /run/ndb/ndb.sock http://ndb/calls/_count -d '{"query":{"status":"failed"}}'
# {"count":17}
```

```go [Go]
n, err := calls.Count(ctx, ndb.Eq("status", "failed"))
```

```ruby [Ruby]
calls.count(status: "failed")
```

```ts [TypeScript]
const n = await calls.count({ status: 'failed' })
```

:::

## `POST /{coll}/_aggregate`

Runs a pipeline: `$match`, `$unwind`, `$group`, `$sort`, `$limit`, `$skip`, `$project`, with accumulators and time buckets. The stages are on [Aggregate](../aggregate.md).

| Body key | Type | Default | Meaning |
|---|---|---|---|
| `pipeline` | object[] | required | the stages |
| `limit`, `skip`, `after` | | | pages over the output rows |
| `count` | bool | `false` | add `X-Ndb-Total`, the number of output rows |
| `explain` | bool | `false` | add `X-Ndb-Explain` |

**Answer:** the output rows, with the same formats and headers as `_search`. **Errors:** `invalid_pipeline`, `422 group_limit_exceeded`, the cursor errors.

::: code-group

```bash [curl]
curl --unix-socket /run/ndb/ndb.sock http://ndb/calls/_aggregate -d '{"pipeline":[
  {"$match": {"ts": {"$gte": "now-1d"}}},
  {"$group": {"_id": "$status", "n": {"$count": {}}, "avg": {"$avg": "$duration"}}}
]}'
# [{"_id":"answered","n":2,"avg":35.5},{"_id":"failed","n":1,"avg":3}]
```

```go [Go]
res, err := calls.Aggregate(ctx, ndb.Pipeline{
	{"$match": map[string]any{"ts": map[string]any{"$gte": "now-1d"}}},
	{"$group": map[string]any{"_id": "$status", "n": map[string]any{"$count": map[string]any{}}}},
})
```

```ruby [Ruby]
calls.aggregate([{ "$group" => { "_id" => "$status", "n" => { "$count" => {} } } }]).rows
```

```ts [TypeScript]
const res = await calls.aggregate([{ $group: { _id: '$status', n: { $count: {} } } }])
```

:::

## `POST /{coll}/_update_by_query`

The atomic update by query: the daemon matches and writes in one step against the newest state, so two updates never interleave. A guarded decrement or an `$inc` is safe without a retry.

| Body key | Type | Default | Meaning |
|---|---|---|---|
| `query` | object | required | the filter; it must not be empty |
| `update` | object | required | write operators: `$set`, `$setOnInsert`, `$unset`, `$inc`, `$push`, `$pull`, `$min`, `$max` |
| `many` | bool | `false` | update every match; without it, the first match |
| `upsert` | bool | `false` | when nothing matches, create the document from the equality fields of `query` plus the operators |
| `expire_in` | duration | none | set `expires_field` to now + `expire_in` on every written document |

**Answer:** `{"matched","modified","doc"}`. `doc` is the new document with its `_v`, for a single update that matched or upserted; it is absent with `many`. Nothing matched is `{"matched":0,"modified":0}`, not an error.

**Rules:** `many` needs `_id` or an indexed field in `query` (`unindexed_many`). An empty `query` is `empty_filter`. A write operator inside `query`, or a filter inside `update`, is `unknown_operator`. A `many` update is atomic per WAL record, not as a whole. See [Update by query](../query.md#update-by-query).

**Errors:** `empty_filter`, `unindexed_many`, `no_update`, `unknown_operator`, `conflicting_update`, `invalid_field`, `immutable_id`, `invalid_query`.

::: code-group

```bash [curl]
curl --unix-socket /run/ndb/ndb.sock http://ndb/stock/_update_by_query -d '{
  "query": { "sku": "a", "qty": { "$gte": 1 } },
  "update": { "$inc": { "qty": -1 } }
}'
# {"matched":1,"modified":1,"doc":{"_id":"...","sku":"a","qty":4,"_v":7}}
```

```go [Go]
res, err := stock.UpdateBy(ctx, ndb.And(ndb.Eq("sku", "a"), ndb.Gte("qty", 1)), ndb.Ops{"$inc": {"qty": -1}})
```

```ruby [Ruby]
stock.update_by({ sku: "a", qty: { "$gte" => 1 } }, { "$inc" => { qty: -1 } })
```

```ts [TypeScript]
await stock.updateBy({ sku: 'a', qty: { $gte: 1 } }, { $inc: { qty: -1 } })
```

:::

## `POST /{coll}/_delete_by_query`

Removes the first document that matches, or every match with `many`.

| Body key | Type | Default | Meaning |
|---|---|---|---|
| `query` | object | required | the filter; it must not be empty. No `query` (the body `{}`) is the same empty filter: `400 empty_filter` |
| `many` | bool | `false` | delete every match |

**Answer:** `{"deleted":n}`. **Rules and errors:** as for `_update_by_query`. With `many`, when the index alone answers the query (one indexed field with `$eq`, `$in` or a range), ndb deletes the matches without reading them.

::: code-group

```bash [curl]
curl --unix-socket /run/ndb/ndb.sock http://ndb/calls/_delete_by_query \
  -d '{"query":{"ts":{"$lt":"now-90d"}},"many":true}'
# {"deleted":1204}
```

```go [Go]
n, err := calls.DeleteBy(ctx, ndb.Lt("ts", "now-90d"), ndb.Many())
```

```ruby [Ruby]
calls.delete_by({ ts: { "$lt" => "now-90d" } }, many: true)
```

```ts [TypeScript]
await calls.deleteBy({ ts: { $lt: 'now-90d' } }, { many: true })
```

:::

To empty a whole collection, use [truncate](collections.md#post-coll-truncate): it costs the same for any size.

## Collection patterns

A path with a name prefix and one `*` at the end reads every collection whose name starts with the prefix. It is made for collections split by time: write to `logs-2026-11`, read a year with `logs-2026-*` and everything with `logs-*`, and [drop](collections.md#delete-coll) a period when it ends.

A path can also list collections, split by commas, and mix them with patterns: `logs-2026-11,logs-2026-10` reads two months, `logs-2026-1*,logs-2025-12` a quarter. You choose the collections a read touches; ndb does not choose them from the query. The name of a partitioned collection in a list reads its live partitions.

```bash
curl --unix-socket /run/ndb/ndb.sock http://ndb/logs-2026-11,logs-2026-10/_count -d '{"query":{"lvl":"error"}}'
```

```bash
curl --unix-socket /run/ndb/ndb.sock http://ndb/logs-2026-*/_search -d '{
  "query": { "lvl": "error" },
  "sort": { "at": -1 },
  "limit": 50
}'
# [{"_coll":"logs-2026-11","_id":"...","lvl":"error"},{"_coll":"logs-2026-10",...}]
```

| Route | On a pattern |
|---|---|
| `_search` | one result over every matched collection, in the `sort` order |
| `_count` | the sum over the collections |
| `_aggregate` | the pipeline runs as if the collections were one |
| `GET /{pattern}` | `{"collections":[...]}`: the matched names |
| every other route | `400 pattern_read_only` |

- **Pattern:** a prefix and one `*` at the end. `*` alone, a `*` in the middle or an upper-case prefix is `invalid_pattern`.
- **List:** items split by commas, each a pattern, a collection name or a partitioned collection. A name that does not exist is `404 unknown_collection` (you asked for it); an empty item is `invalid_pattern`. A collection matched by two items is read once. A scoped token needs read access to every item.
- **Plain prefix:** `logs-*` also matches `logs-archive`. Name the partitions so their prefix is unique.
- **`_coll`:** each row starts with `_coll`, the name of its collection. A `_coll` field stored in the document is replaced.
- **Order:** the `sort` keys, then `_coll`, then `_id`. With no `sort`: collections by name, then insertion order.
- **Plans:** each collection uses its own indexes and zone maps, so a time filter on `logs-*` skips old row groups. `X-Ndb-Plan` is `pattern`.
- **Pagination:** `skip`, `limit` and `after` work over the whole result. A cursor is bound to its pattern. `snapshot: true` is `invalid_query` on a pattern.
- **Set of collections:** taken when the call starts. A pattern with no match answers no rows (`[]`, `X-Ndb-Total: 0`), not `unknown_collection`. At most 256 collections, else `pattern_too_wide`.
- **Definitions:** keep the same `time` field in every partition.

::: code-group

```go [Go]
type Log struct {
	ID   string    `ndb:"_id"`
	At   time.Time `ndb:"at,time"`
	Lvl  string    `ndb:"lvl"`
	Coll string    `ndb:"_coll"` // filled on reads, never sent
}

logs, err := ndb.Collection[Log](db, "logs-2026-*") // a pattern never declares
rows, err := logs.Query(ndb.Eq("lvl", "error")).Sort(ndb.Desc("at")).Limit(50).All(ctx)
```

```ruby [Ruby]
logs = db.collection("logs-2026-*")
logs.query(lvl: "error").sort(at: -1).limit(50).all # each row has "_coll"
```

```ts [TypeScript]
const logs = db.collection('logs-2026-*', schema)
const rows = await logs.query({ lvl: 'error' }).sort({ at: -1 }).limit(50).all() // rows[0]._coll
```

:::
