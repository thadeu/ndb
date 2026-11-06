# Aggregate

`POST /v1/c/{coll}/aggregate` groups documents and computes counts, sums, averages and more, inside the daemon. The rows come back as NDJSON, with the same headers and [pagination](query.md#pagination) as `find`.

::: code-group

```bash [curl]
curl --unix-socket /run/ndb/ndb.sock http://ndb/v1/c/calls/aggregate -d '{
  "pipeline": [
    { "$match": { "duration": { "$gte": 1 } } },
    { "$group": { "_id": "$status", "n": { "$count": {} }, "avg": { "$avg": "$duration" } } },
    { "$sort": { "n": -1 } }
  ],
  "limit": 100
}'
# {"_id":"answered","n":2,"avg":35.5}
# {"_id":"failed","n":1,"avg":3}
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
page = ndb.aggregate("calls",
                     pipeline: [
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

`limit`, `skip`, `after` and `count` page the result rows, like in [find](query.md#find).

## Pipeline shape

- Exactly one `$group`.
- Before it: `$match` (all are ANDed) and one `$sort`. The `$sort` matters only for `$first` and `$last`.
- After it: `$match`, `$sort`, `$limit`, `$skip`, `$project`.
- `$match` takes the [filters](query.md#filters) of `find`. A `$match` before `$group` uses indexes and zone maps like `find` does.

## Stages

| Stage | Where | Meaning |
|---|---|---|
| `$match` | before and after `$group` | keep the documents (or groups) that match a filter |
| `$sort` | before `$group` (once), after `$group` | order. Before the group it only decides `$first` and `$last`. |
| `$group` | once | build one row per key, see below |
| `$limit`, `$skip` | after `$group` | cut the group rows |
| `$project` | after `$group` | keep or remove fields of the group rows |

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

## Limits

- The group table is limited in memory. Over the limit the answer is `group_limit_exceeded` (HTTP 422): add a `$match` or use fewer group keys.
- An aggregate cursor runs the pipeline again for each page.
- A bad pipeline is `400 invalid_pipeline`. See [HTTP API](http-api.md#errors) for every code.
