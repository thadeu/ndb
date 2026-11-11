# Aggregate

`POST /{coll}/_aggregate` ([route](http/queries.md#post-coll-aggregate)) groups documents and computes counts, sums, averages and more, inside the daemon. The rows come back like the rows of a search (a JSON array, or NDJSON with `Accept`), with the same headers and [pagination](query.md#pagination).

::: code-group

```bash [curl]
curl --unix-socket /run/ndb/ndb.sock http://ndb/calls/_aggregate -d '{
  "pipeline": [
    { "$match": { "duration": { "$gte": 1 } } },
    { "$group": { "_id": "$status", "n": { "$count": {} }, "avg": { "$avg": "$duration" } } },
    { "$sort": { "n": -1 } }
  ],
  "limit": 100
}'
# [{"_id":"answered","n":2,"avg":35.5},{"_id":"failed","n":1,"avg":3}]
```

```go [Go]
res, err := calls.Aggregate(ctx, ndb.Pipeline{
	{"$match": map[string]any{"duration": map[string]any{"$gte": 1}}},
	{"$group": map[string]any{
		"_id": "$status",
		"n":   map[string]any{"$count": map[string]any{}},
		"avg": map[string]any{"$avg": "$duration"},
	}},
	ndb.SortStage(ndb.Desc("n")),
}, ndb.AggLimit(100))

// res.Rows, res.Next, res.Total (with ndb.AggCount())
```

```ruby [Ruby]
page = calls.aggregate([
                         { "$match" => { "duration" => { "$gte" => 1 } } },
                         { "$group" => {
                           "_id" => "$status",
                           "n" => { "$count" => {} },
                           "avg" => { "$avg" => "$duration" }
                         } },
                         { "$sort" => { "n" => -1 } }
                       ],
                       limit: 100)

page.rows   # => [{"_id" => "answered", "n" => 2, "avg" => 35.5}, ...]
```

```ruby [Rails]
Call.where(duration: 1..).group(:status).count       # => {"answered" => 2, "failed" => 1}
Call.where(duration: 1..).group(:status).average(:duration)

# a full pipeline goes through the client
Ndb::Rails.client.aggregate(Call.collection_name, pipeline: [...])
```

```ts [TypeScript]
const res = await calls.aggregate<{ _id: string; n: number; avg: number }>(
  [
    { $match: { duration: { $gte: 1 } } },
    { $group: { _id: '$status', n: { $count: {} }, avg: { $avg: '$duration' } } },
    { $sort: { n: -1 } },
  ],
  { limit: 100 },
)

// res.rows, res.next, res.total (with count: true)
```

:::

## Body

```json
{
  "pipeline": [
    { "$match": { "ts": { "$gte": "2026-11-01T00:00:00Z" } } },
    { "$group": {
        "_id": { "status": "$status", "hour": { "$dateTrunc": { "date": "$ts", "unit": "hour" } } },
        "n": { "$count": {} },
        "avg_duration": { "$avg": "$duration" }
    } },
    { "$sort": { "_id.hour": 1 } },
    { "$limit": 1000 }
  ],
  "limit": 100, "skip": 0, "after": "<cursor>", "count": true
}
```

`limit`, `skip`, `after` and `count` page the result rows, like in a [search](query.md#search).

## Pipeline shape

- Exactly one `$group`.
- Before it: `$match` (all are ANDed), one `$sort`, and one `$unwind`. The `$sort` matters only for `$first` and `$last`. A `$match` after the `$unwind` filters its rows.
- After it: `$match`, `$sort`, `$limit`, `$skip`, `$project`.
- `$match` takes the [filters](query.md#filters) of `find`. A `$match` before `$group` uses indexes and zone maps like `find` does.

## Stages

| Stage | Where | Meaning |
|---|---|---|
| `$match` | before and after `$group` | keep the documents (or groups) that match a filter |
| `$having` | after `$group` | the same as `$match` there, named as in SQL: keep the groups that match (`{"calls": {"$gt": 10}}`). Before `$group` it is `invalid_pipeline`: use `$match`. |
| `$unwind` | before `$group` (once) | one row per element of an array, see [`$unwind`](#unwind) |
| `$sort` | before `$group` (once), after `$group` | order. Before the group it only decides `$first` and `$last`. |
| `$group` | once | build one row per key, see below |
| `$limit`, `$skip` | after `$group` | cut the group rows |
| `$project` | after `$group` | keep or remove fields of the group rows |
| `$lookup` | at the end | add documents of another collection to each output row, see [Lookup](#lookup) |

## `$unwind`

`$unwind` makes one row per element of an array, so `$group` can group by the elements. In each row, the path of the array holds that element.

```json
{"pipeline": [
  {"$match": {"status": "paid"}},
  {"$unwind": "$items"},
  {"$group": {"_id": "$items.sku", "qty": {"$sum": "$items.qty"}, "orders": {"$distinct": "$_id"}}},
  {"$sort": {"qty": -1}},
  {"$limit": 10}
]}
```

With the orders `{"_id": "o1", "items": [{"sku": "a", "qty": 2}, {"sku": "b", "qty": 1}]}` and `{"_id": "o2", "items": [{"sku": "a", "qty": 5}]}`, the rows are `{"_id": "a", "qty": 7, "orders": 2}` and `{"_id": "b", "qty": 1, "orders": 1}`.

The rules are those of MongoDB:

| The value at the path | Rows |
|---|---|
| an array with elements | one per element |
| an empty array, `null`, or no field | none (one, with `preserveNullAndEmptyArrays`) |
| not an array | one, with the value |

The long form has two options:

```json
{"$unwind": {"path": "$items", "preserveNullAndEmptyArrays": true, "includeArrayIndex": "i"}}
```

- `preserveNullAndEmptyArrays` keeps a document with no element as one row.
- `includeArrayIndex` names a top-level field that gets the place of the element, from 0 (`null` when the value is not an array).

Notes:

- A `$match` before `$unwind` uses indexes and zone maps. A `$match` after it is tested on each row (`{"items.qty": {"$gte": 2}}` keeps the elements with that quantity), with no index. So put the filters of the document first.
- `$count` counts rows (elements). To count documents, use `{"$distinct": "$_id"}`.
- `$first` and `$last` follow the order of the documents, then of the elements.
- The path goes through objects only (`$order.items`), not through another array.
- One `$unwind` per pipeline, before `$group`. A pipeline with `$unwind` needs a `$group`. Else `400 invalid_pipeline`.
- The array is read from the document, not from a column. An `$unwind` on many documents is slower than a group on a plain field: filter first.

## Group key

`$group._id` is:

- a field path: `"$status"`. Paths start with `$`: `"$a.b"`. A path through an array gives an array value.
- a constant, or `null` for one group with every document.
- a `$dateTrunc`, to bucket time.
- an object of those, for a composite key: `{"status": "$status", "hour": {"$dateTrunc": ...}}`.

Missing and `null` are the same key. Groups come out sorted by `_id`.

## Accumulators

| Accumulator | Result |
|---|---|
| `$count` | number of documents. Its argument is `{}`. |
| `$sum` | sum of the numbers. Other values are ignored. No number gives `0`. |
| `$avg` | sum / count of the numbers. No number gives `null`. |
| `$min`, `$max` | lowest / highest value in the cross-type order. Nothing gives `null`. |
| `$first`, `$last` | value of the first / last document of the group, in the order of the `$sort` before the group. |
| `$distinct` | number of distinct values (`COUNT(DISTINCT)`, the `cardinality` of Elasticsearch), exact. Missing and `null` are not counted. |
| `$addToSet` | the distinct values, as an array sorted in the cross-type order. Missing and `null` are left out. |

The distinct values of `$distinct` and `$addToSet` count against `group_limit`: a group over it is `group_limit_exceeded`, not a wrong count.

How many times each caller called, and the callers that called more than 10 times:

```json
{"pipeline": [
  {"$match": {"type": "finishcall"}},
  {"$group": {"_id": "$ani", "calls": {"$count": {}}}},
  {"$having": {"calls": {"$gt": 10}}},
  {"$sort": {"calls": -1}}
]}
```

How many distinct callers each customer had, and which hangup causes:

```json
{"pipeline": [
  {"$match": {"type": "finishcall"}},
  {"$group": {"_id": "$customer", "callers": {"$distinct": "$ani"}, "causes": {"$addToSet": "$hangup_cause"}}},
  {"$sort": {"callers": -1}},
  {"$limit": 10}
]}
```

## Time buckets

```json
{ "$dateTrunc": { "date": "$ts", "unit": "hour", "timezone": "Europe/Rome" } }
```

- Units: `minute`, `hour`, `day`, `week` (Monday), `month`.
- The default timezone is UTC.
- The result is the bucket start as an RFC 3339 UTC string.
- A value that is not a time gives `null`.

## Project

`{"$project": {"n": 1}}` keeps fields; `{"$project": {"n": 0}}` removes them. Do not mix. `_id` stays unless listed with `0`.

## Lookup

`$lookup` stages go at the end of the pipeline: they add, to each output row, documents of another collection. They take the MongoDB names, or one relation of the [lookup of a search](query.md#lookup):

```json
{
  "pipeline": [
    { "$group": { "_id": "$customer_id", "total": { "$sum": "$total" } } },
    { "$sort": { "total": -1 } },
    { "$limit": 10 },
    { "$lookup": { "from": "customers", "localField": "_id", "foreignField": "_id", "as": "customer" } }
  ]
}
```

```json
{ "_id": "c1", "total": 125, "customer": [ { "_id": "c1", "name": "Ana" } ] }
```

- MongoDB names: `from`, `localField`, `foreignField`, `as`. As in MongoDB, `as` is always an array. `pipeline` and `let` are not supported.
- One relation of a search: `{ "$lookup": { "customer": { "belongs_to": "customers", "fk": "_id" } } }` gives an object or `null`.
- A `$lookup` before another stage is `400 lookup_invalid`. The rules of a search lookup apply: an indexed other side, 10 000 documents at most.

## Limits

- The group table is limited in memory. Over the limit the answer is `group_limit_exceeded` (HTTP 422): add a `$match` or use fewer group keys.
- An aggregate cursor runs the pipeline again for each page.
- A bad pipeline is `400 invalid_pipeline`. See [Errors](http/errors.md) for every code.
