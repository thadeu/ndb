# Query

Queries are JSON. The same body works from every SDK and from `curl`. The daemon owns all query meaning. The SDKs only build JSON.

This page has everything about reading and writing: `find`, the filter operators, the `/{_id}` routes, update and delete by filter with the write operators, and pagination. [Aggregate](aggregate.md) and [Watch](watch.md) have their own pages.

One rule covers every body: **a key that starts with `$` is an operator, any other key is a field.**

## find

`POST /v1/c/{coll}/find`

::: code-group

```bash [curl]
curl --unix-socket /run/ndb/ndb.sock http://ndb/v1/c/calls/find -d '{
  "where": { "status": "answered", "duration": { "$gte": 30 } },
  "sort": { "at": -1 },
  "fields": ["call_id", "status"],
  "limit": 50
}'
```

```go [Go]
rows, err := calls.
	Where(ndb.Eq("status", "answered"), ndb.Gte("duration", 30)).
	Sort(ndb.Desc("at")).
	Fields("call_id", "status").
	Limit(50).
	All(ctx)
```

```ruby [Ruby]
page = ndb.find("calls",
                where: { status: "answered", duration: { "$gte" => 30 } },
                sort: { at: -1 },
                fields: %w[call_id status],
                limit: 50)

page.rows
```

```ruby [Rails]
Call.where(status: "answered", duration: { "$gte" => 30 })
    .order(at: :desc)
    .limit(50)
    .pluck(:call_id, :status)
```

```ts [TypeScript]
const rows = await calls
  .where({ status: 'answered', duration: { $gte: 30 } })
  .sort({ at: -1 })
  .limit(50)
  .all()
```

:::

The full body:

```json
{
  "where": { "status": "answered", "duration": { "$gte": 30 } },
  "fields": ["_id", "status"],
  "sort": { "ts": -1 },
  "limit": 50,
  "skip": 0,
  "after": "<cursor>",
  "count": true,
  "versions": true,
  "snapshot": true,
  "explain": true
}
```

All keys are optional. An unknown key is `invalid_query`.

| Key | Meaning |
|---|---|
| `where` | filter object. See [Filters](#filters). No `where` matches every document. |
| `fields` | return only these paths (dot paths allowed). |
| `sort` | object `field: 1` or `-1`. The key order is the sort order. The document id is always the last sort key. With no `sort`, the order is insertion order. |
| `limit`, `skip` | non-negative integers. |
| `after` | cursor from the previous page. See [Pagination](#pagination). |
| `count` | add the total number of matches in header `X-Ndb-Total`. |
| `versions` | add `_v`, the document version, to each row. Use it as `_v` in the filter of an [update](#update-by-filter). |
| `snapshot` | keep the first snapshot for 60 s, so all pages see the same data. |
| `explain` | add header `X-Ndb-Explain` with the plan counters. |

The answer is NDJSON, one row per line. The page data is in response headers (they are sent before the first row):

| Header | Meaning |
|---|---|
| `X-Ndb-Next` | cursor of the next page. Absent on the last page. |
| `X-Ndb-Plan` | `id`, `index:<field>`, `scan` or `snapshot`. |
| `X-Ndb-Examined` | candidate rows whose `where` was evaluated. |
| `X-Ndb-Total` | only with `count: true`. |
| `X-Ndb-Explain` | only with `explain: true`. JSON with `segments_read`, `segments_skipped`, `row_groups_read`, `row_groups_skipped`, `rows_read`, `rows_matched`, `raw_fetched`, `cache_hits`. |

## Filters

A filter is the `where` of `find`, the `$match` of [aggregate](aggregate.md), the `where` of [watch](watch.md), and the non-`$` part of an [update](#update-by-filter) or [delete](#delete-by-filter) body. It is the same language everywhere.

A field with a plain value means equality: `{"status": "answered"}`. Several fields in one object are ANDed.

| Operator | Meaning |
|---|---|
| `$eq`, `$ne` | equal, not equal. `$ne` also matches a missing field. |
| `$gt`, `$gte`, `$lt`, `$lte` | compare. Only values of the same type match: `{"$gt": 1}` never matches a string. The operand must be a scalar. |
| `$in`, `$nin` | in / not in an array. `$nin` also matches a missing field. |
| `$exists` | `true` or `false`. Missing is not `null`. |
| `$not` (in a field) | negate an operator object: `{"duration": {"$not": {"$gt": 10}}}`. |
| `$regex`, `$options` | string match, see [Regular expressions](#regular-expressions). |
| `$and`, `$or`, `$nor` | non-empty array of filter objects. `$nor` matches when none of them does. |
| `$not` (top level) | negate a filter object: `{"$not": {"status": "failed"}}`. |

```json
{ "status": { "$in": ["answered", "failed"] } }
{ "duration": { "$gte": 30, "$lte": 60 } }
{ "$or": [{ "status": "failed" }, { "duration": { "$gt": 50 } }] }
{ "$nor": [{ "status": "failed" }, { "caller": { "$exists": false } }] }
{ "agent.id": 7 }
```

Rules:

- `{"a": null}` matches an explicit `null` only. Use `{"a": {"$exists": false}}` for a missing field.
- Arrays: a field matches when any element matches. Dot paths walk into arrays of objects (`items.sku`).
- Mixing operators and plain keys in one field object is an error: `{"a": {"$gt": 1, "b": 2}}`.
- Unknown operators are `invalid_query`: `$elemMatch` does not exist yet.
- Time: for the collection `time` field, RFC 3339 strings are compared as instants, so offsets do not matter.
- Cross-type order (sort, cursors, indexes): `missing < null < number < string < time < object < array < bool`.

### Regular expressions

```json
{ "where": { "title": { "$regex": "^alpha", "$options": "i" } } }
```

- `$regex` is a string pattern, at most 1 KiB. `$options` is optional and needs `$regex`. Flags: `i` (ignore case), `m` (`^` and `$` match at each line), `s` (`.` matches a newline), `x` (extended, see below). Any other flag is `invalid_query`.
- Syntax is Go **RE2**, not PCRE: there are no backreferences (`\1`) and no look-around (`(?=...)`, `(?<!...)`). Matching is linear in the text, so a pattern cannot cause catastrophic backtracking. An invalid pattern or flag is `invalid_query` with the RE2 message.
- `x` is done by ndb, not by RE2: white space and `# ...` comments outside a character class are removed before compiling. Escape them (`\ `, `\#`) or put them in a class (`[ ]`) to keep them.
- Only strings match. A number, `null`, a boolean, an object or a missing field never matches. On an array field the row matches when any string element matches. `$regex` works in dot paths, in `$not` (field and top level), `$and` and `$or`, and next to other operators of the same field. `{"a": {"$not": {"$regex": "x"}}}` also matches a missing field, like the other negations.
- A time field is matched as its RFC 3339 text.
- The pattern is compiled once per query, not once per row.
- Planner: a pattern anchored with `^`, with a literal start (`^abc`, `^abc.*`, `^ab*`), and without `i` or `m`, is turned into the range `[abc, abd)` on that field. A ready index on the field answers it (`X-Ndb-Plan: index:<field>`), and the zone maps of the segments skip row groups outside the range (`row_groups_skipped`). The regex is still run on every candidate. Without such a prefix (no `^`, a group or alternation first, `i`, `m`, or only `$not`/`$or`) the regex is a filter on a scan. `explain: true` shows which one ran.

## By _id

Every document has `_id` (see [Document id](configuration.md#document-id)). These routes reach one document by it, without a filter body:

::: code-group

```bash [curl]
curl --unix-socket /run/ndb/ndb.sock http://ndb/v1/c/calls/0192f3a4-7c1e-7b2a-9f3d-1a2b3c4d5e6f
curl --unix-socket /run/ndb/ndb.sock http://ndb/v1/c/stock/sku-a/update -d '{"qty":{"$gte":1},"$inc":{"qty":-1}}'
curl --unix-socket /run/ndb/ndb.sock -X DELETE http://ndb/v1/c/calls/0192f3a4-7c1e-7b2a-9f3d-1a2b3c4d5e6f
```

```go [Go]
call, err := calls.Get(ctx, id)
res, err := stock.UpdateID(ctx, "sku-a", ndb.Ops{"$inc": {"qty": -1}}, ndb.Guard(ndb.Gte("qty", 1)))
deleted, err := calls.DeleteID(ctx, id)
```

```ruby [Ruby]
ndb.get("calls", id)
ndb.update("stock", "sku-a", { "$inc" => { qty: -1 } })   # a String is the _id
ndb.delete("calls", id)
```

```ruby [Rails]
call = Call.find(id)   # call.id is call._id
call.destroy
```

```ts [TypeScript]
const call = await calls.get(id)
await stock.update('sku-a', { $inc: { qty: -1 } })   // a string is the _id
await calls.delete(id)
```

```bash [CLI]
ndb get calls 0192f3a4-7c1e-7b2a-9f3d-1a2b3c4d5e6f
ndb update stock sku-a '{"$inc":{"qty":-1}}'
ndb delete calls 0192f3a4-7c1e-7b2a-9f3d-1a2b3c4d5e6f
```

:::

- `GET /v1/c/{coll}/{_id}` answers the document with `_v`, or `404 not_found`.
- `POST /v1/c/{coll}/{_id}/update` takes write operators and optional guard filters in the body, like [update by filter](#update-by-filter) with `{"_id": ...}` added. `upsert` and `expire_in` work; `many` does not.
- `DELETE /v1/c/{coll}/{_id}` answers `{"deleted":0}` or `{"deleted":1}`.
- A fixed route wins: a document whose `_id` is `stats` or `find` is reached with `find`, not with `GET /{_id}`.

## Update by filter

`POST /v1/c/{coll}/update` takes **one JSON object that mixes a filter and write operators**. The daemon matches the filter and writes the result inside the single writer of the collection, against the newest state. Two updates never interleave, so a guarded decrement or an `$inc` needs no retry.

::: code-group

```bash [curl]
# take one unit of stock, only if there is one
curl --unix-socket /run/ndb/ndb.sock http://ndb/v1/c/stock/update \
  -d '{"sku":"a","qty":{"$gte":1},"$inc":{"qty":-1}}'
# {"matched":1,"modified":1,"doc":{"sku":"a","qty":4,"_v":7}}
```

```go [Go]
res, err := stock.Update(ctx,
	ndb.And(ndb.Eq("sku", "a"), ndb.Gte("qty", 1)),
	ndb.Ops{"$inc": {"qty": -1}},
)
// res.Matched, res.Modified, res.Doc
```

```ruby [Ruby]
ndb.update("stock", { sku: "a", qty: { "$gte" => 1 } }, { "$inc" => { qty: -1 } })
# => {"matched" => 1, "modified" => 1, "doc" => {...}}
```

```ruby [Rails]
Stock.where(sku: "a", qty: { "$gte" => 1 }).update("$inc" => { qty: -1 })

stock.decrement!(:qty)   # atomic $inc on the daemon, refreshes the attribute
```

```ts [TypeScript]
const res = await stock.update({ sku: 'a', qty: { $gte: 1 } }, { $inc: { qty: -1 } })
// { matched, modified, doc }
```

```bash [CLI]
ndb update stock '{"sku":"a","qty":{"$gte":1},"$inc":{"qty":-1}}'
```

:::

The SDKs take the filter and the operators as two arguments and send them as one body. A key of the operators without `$` is an error in the SDK, before anything is sent.

### Body keys

| Key | Meaning |
|---|---|
| `$set`, `$setOnInsert`, `$unset`, `$inc`, `$push`, `$pull`, `$min`, `$max` | write operators, each an object of fields (dot paths allowed) |
| `$and`, `$or`, `$nor` | filter, see [Filters](#filters) |
| any key without `$` (the id field, `qty`, `_v`, `meta.k`) | filter |
| any other `$...` key | `400 unknown_operator` |

Options are in the query string: `many=true`, `upsert=true`, `expire_in=<duration>`.

### Write operators

| Operator | Meaning |
|---|---|
| `{"$set": {"f": v}}` | set the field |
| `{"$setOnInsert": {"f": v}}` | set the field only when an [upsert](#upsert) creates the document. On a match it does nothing, so a racing writer is never overwritten. |
| `{"$unset": {"f": true}}` | remove the field (`true` or `""`) |
| `{"$inc": {"f": n}}` | add a number. A negative number subtracts. A missing field is 0. A field that is not a number is `invalid_field`. Integers stay exact. |
| `{"$push": {"f": v}}` | append one value. `{"$push": {"f": {"$each": [a, b]}}}` appends many. A missing field becomes a new array. A field that is not an array is `invalid_field`. |
| `{"$pull": {"f": v}}` | remove every element equal to `v`. A condition removes the elements that match it: `{"$gte": 5}`, or `{"status": "done"}` for arrays of objects. |
| `{"$min": {"f": v}}`, `{"$max": {"f": v}}` | set when the field is missing or `v` is lower (higher). Numbers, or strings in lexical order. A different type than the field is `invalid_field`. |

Several operators can go in one body, on different fields:

```json
{
  "_id": "order-1",
  "$push": { "events": { "at": "2026-11-06T12:00:00Z", "kind": "paid" } },
  "$inc": { "attempts": 1 },
  "$max": { "last_seen": "2026-11-06T12:00:00Z" },
  "$set": { "status": "paid" }
}
```

### Arrays

`$push` and `$pull` change an array inside the document in one atomic write. Two concurrent pushes both land.

```bash
# append one event
curl ... /v1/c/orders/update -d '{"_id":"o1","$push":{"events":{"kind":"paid"}}}'

# append many
curl ... /v1/c/orders/update -d '{"_id":"o1","$push":{"tags":{"$each":["vip","eu"]}}}'

# remove a value, or the elements that match a condition
curl ... /v1/c/orders/update -d '{"_id":"o1","$pull":{"tags":"eu"}}'
curl ... /v1/c/orders/update -d '{"_id":"o1","$pull":{"events":{"kind":"draft"}}}'
curl ... /v1/c/orders/update -d '{"_id":"o1","$pull":{"scores":{"$lt":10}}}'
```

### Answers

Always HTTP 200 when the body is valid:

- One document: `{"matched":1,"modified":1,"doc":{...}}`. `doc` is the new document with `_v`.
- Nothing matched: `{"matched":0,"modified":0}`. That is not an error. For a guarded decrement it means "sold out".
- With `many=true`: `{"matched":N,"modified":N}`, with no `doc`.
- `modified` is 0 when the operators leave the document as it is (for example `$max` with a lower value). No new version is written then.

### Which documents

- Without `many`, a filter with the id field uses the id lookup. Without the id, the first matching document is used.
- With `many=true`, the filter must be served by the id or a ready secondary index. Otherwise the call is `400 unindexed_many`, so a big update never blocks the writer on a full scan.
- A filter must not be empty (`400 empty_filter`). Expired documents are never matched.
- A `many` write has no size limit. ndb lists the matches one time, then writes them in WAL records of up to half of `max_line_size`. Other writes to the collection run between two records. Each record is atomic, but the write as a whole is not: after a crash, the records already written stay, and you can send the same call again. Each document is written at most one time, so an `$inc` never runs two times on the same document.

```bash
curl ... '/v1/c/calls/update?many=true' -d '{"status":"failed","$set":{"status":"retry"}}'
# {"matched":3,"modified":3}
```

### Optimistic concurrency

`_v` in the filter is the version of the document. Read with `versions: true`, then write with that `_v`. `matched: 0` means somebody else wrote first.

```json
{ "_id": "c1", "_v": 7, "$set": { "note": "checked" } }
```

### Upsert

With `upsert=true` and no match, a document is created from the plain equality fields of the filter (dot paths too). Then the operators run on it: `$inc` starts from 0, `$push` creates the array, `$min` and `$max` set the value. Without the id field in the filter, an id is generated like `insert` does.

The answer is `matched: 0`, `modified: 1` and the `doc`. If the id exists but the rest of the filter does not match, the call is `409 duplicate_id`.

Create once, never overwrite: the filter finds the document, `$setOnInsert` fills it only when it is new. Two racing calls make one document.

```bash
curl ... '/v1/c/users/update?upsert=true' -d '{"email":"a@b.c","$setOnInsert":{"name":"Ana","plan":"free"}}'
```

A counter that expires, as a rate limit:

```bash
curl ... '/v1/c/hits/update?upsert=true&expire_in=60s' -d '{"_id":"ip:1.2.3.4","$inc":{"n":1}}'
```

### Contract errors

HTTP 400:

| Code | Cause |
|---|---|
| `unknown_operator` | a top-level `$` key that is neither a write operator nor `$and`, `$or`, `$nor` |
| `no_update` | no write operator |
| `conflicting_update` | the same field, or a field and its parent (`a` and `a.b`), in two operators |
| `invalid_field` | a field name that starts with `$`, or a value of the wrong type |
| `unindexed_many` | `many=true` without the id or an indexed field in the filter |
| `empty_filter` | no filter |
| `immutable_id` | an operator on the id field. `_v` cannot be written either. |

The WAL line of each changed document is the resulting document with the next version, so replay and `watch` need no operators. In direct mode, `update` and `delete` are `409 direct_collection`.

## Delete by filter

`POST /v1/c/{coll}/delete` takes a filter as the body and answers `{"deleted":N}`. Nothing matched is `{"deleted":0}`. `many=true` follows the same index and size rules as update. A write operator in a delete body is `unknown_operator`.

When the index alone answers the filter (one indexed field, with `$eq`, `$in` or a range, and no `null`), ndb deletes the matches without reading them. This is the fast path for sealed data. Example: on 1M documents, a delete of 868k sealed documents by an indexed `status` takes about 3 s. A filter that also has a field with no index still works, but ndb reads each candidate to test it.

::: code-group

```bash [curl]
curl --unix-socket /run/ndb/ndb.sock http://ndb/v1/c/calls/delete -d '{"call_id":"c2"}'
curl --unix-socket /run/ndb/ndb.sock 'http://ndb/v1/c/calls/delete?many=true' -d '{"status":"expired"}'
```

```go [Go]
deleted, err := calls.Delete(ctx, ndb.Eq("call_id", "c2"))
deleted, err = calls.Delete(ctx, ndb.Eq("status", "expired"), ndb.Many())
```

```ruby [Ruby]
ndb.delete("calls", { call_id: "c2" })                  # => {"deleted" => 1}
ndb.delete("calls", { status: "expired" }, many: true)
```

```ruby [Rails]
call.destroy
Call.where(status: "expired").delete   # returns how many it deleted
```

```ts [TypeScript]
await calls.delete({ call_id: 'c2' })
await calls.delete({ status: 'expired' }, { many: true })
```

```bash [CLI]
ndb delete calls '{"status":"expired"}' --many
```

:::

## Pagination

Both modes work on `find` and [aggregate](aggregate.md).

**Offset**: `skip` + `limit`. Simple, good for page numbers. The cost grows with `skip`, and rows can move between pages when data changes.

```json
{ "where": { "status": "answered" }, "sort": { "ts": -1 }, "skip": 100, "limit": 50 }
```

**Cursor**: `limit`, then pass the `X-Ndb-Next` header back as `after`.

```json
{ "where": { "status": "answered" }, "sort": { "ts": -1 }, "limit": 50, "after": "eyJz..." }
```

- The token is opaque. Do not parse or build it.
- It is bound to the `where` and `sort` (or to the pipeline). A token used with another query is `cursor_mismatch`. A broken token is `cursor_invalid`.
- Rows inserted during the walk do not move pages already read.
- With `snapshot: true`, the cursor keeps one snapshot for 60 s. After that the error is `cursor_expired`.
- An aggregate cursor runs the pipeline again for each page.

Error codes are listed in [HTTP API](http-api.md#errors).
