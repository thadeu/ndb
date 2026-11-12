# Query

Queries are JSON. The same body works from every SDK and from `curl`. The daemon owns all query meaning. The SDKs only build JSON.

This page is the language: search, the filter operators, update and delete by query with the write operators, and pagination. The routes, with every body key, answer and error, are on the [HTTP API](http.md) pages. [Aggregate](aggregate.md) and [Watch](watch.md) have their own pages.

Two words, as in Elasticsearch: the **query** is what matches (the filter), and a **search** is the whole request (query, sort, limit, cursor). One rule covers every query: **a key that starts with `$` is an operator, any other key is a field.**

## Search

`POST /{coll}/_search` ([route](http/queries.md#post-coll-search))

::: code-group

```bash [curl]
curl --unix-socket /run/ndb/ndb.sock http://ndb/calls/_search -d '{
  "query": { "status": "answered", "duration": { "$gte": 30 } },
  "sort": { "at": -1 },
  "fields": ["call_id", "status"],
  "limit": 50
}'
```

```go [Go]
rows, err := calls.
	Query(ndb.Eq("status", "answered"), ndb.Gte("duration", 30)).
	Sort(ndb.Desc("at")).
	Fields("call_id", "status").
	Limit(50).
	All(ctx)
```

```ruby [Ruby]
calls.query(status: "answered", duration: { "$gte" => 30 })
     .sort(at: -1)
     .fields(:call_id, :status)
     .limit(50)
     .all
```

```ruby [Rails]
Call.where(status: "answered", duration: { "$gte" => 30 })
    .order(at: :desc)
    .limit(50)
    .pluck(:call_id, :status)
```

```ts [TypeScript]
const rows = await calls
  .query({ status: 'answered', duration: { $gte: 30 } })
  .sort({ at: -1 })
  .limit(50)
  .all()
```

```bash [CLI]
ndb query calls '{"status":"answered","duration":{"$gte":30}}' -sort '{"at":-1}' -limit 50
```

:::

The full body:

```json
{
  "query": { "status": "answered", "duration": { "$gte": 30 } },
  "fields": ["_id", "status"],
  "sort": { "ts": -1 },
  "limit": 50,
  "skip": 0,
  "after": "<cursor>",
  "count": true,
  "versions": true,
  "snapshot": true,
  "explain": true,
  "per": { "field": "customer_id", "limit": 5 },
  "lookup": { "customer": { "belongs_to": "customers", "fk": "customer_id" } }
}
```

All keys are optional. An unknown key is `invalid_query`.

| Key | Meaning |
|---|---|
| `query` | filter object. See [Filters](#filters). No `query` matches every document. |
| `fields` | return only these paths (dot paths allowed). |
| `sort` | object `field: 1` or `-1`. The key order is the sort order. The document id is always the last sort key, in the direction of the last key of `sort`. With no `sort`, the order is insertion order. |
| `limit`, `skip` | non-negative integers. |
| `after` | cursor from the previous page. See [Pagination](#pagination). |
| `count` | add the total number of matches in header `X-Ndb-Total`. `POST /{coll}/_count` answers only the number. |
| `versions` | add `_v`, the document version, to each row. Use it as a guard in an [update](#update-by-query). |
| `snapshot` | keep the first snapshot for 60 s, so all pages see the same data. |
| `explain` | add header `X-Ndb-Explain` with the plan counters. |
| `lookup` | add the documents of other collections that each row points to. See [Lookup](#lookup). |
| `per` | `{"field": "customer_id", "limit": 5}`: the first `limit` rows (in `sort` order) of each value of `field`, instead of a limit over all rows: the last 5 orders of each customer in one search. A row whose field is an array counts for each element. It does not go with `limit`, `skip`, `after` or `snapshot`. A `has_many` lookup uses it to read the rows of a whole page in one search. |

The answer is the rows: a JSON array, or NDJSON with `Accept: application/x-ndjson`. The page data is in headers:

| Header | Meaning |
|---|---|
| `X-Ndb-Next` | cursor of the next page. Absent on the last page. |
| `X-Ndb-Plan` | `id`, `index:<field>`, `scan`, `snapshot` or `pattern`. |
| `X-Ndb-Examined` | candidate rows whose `query` was evaluated. |
| `X-Ndb-Total` | only with `count: true`. |
| `X-Ndb-Took-Ms` | time spent by the daemon. |
| `X-Ndb-Explain` | only with `explain: true`. JSON with `segments_read`, `segments_skipped`, `row_groups_read`, `row_groups_skipped`, `rows_read`, `rows_matched`, `raw_fetched`, `cache_hits`. |

## Lookup

This is the primitive: the SDKs build this JSON ([Go](clients/go.md#lookup), [Ruby](clients/ruby.md#lookup), [Rails](clients/rails.md#lookup), [TypeScript](clients/typescript.md#lookup)).

`lookup` reads, in the same call, the documents of other collections that the rows point to: the `includes` of Rails, the `$lookup` of MongoDB. The daemon runs it, so every SDK gets the same answer.

::: code-group

```bash [curl]
curl --unix-socket /run/ndb/ndb.sock http://ndb/orders/_search -d '{
  "query": { "status": "paid" },
  "sort": { "created_at": -1 },
  "limit": 20,
  "lookup": {
    "customer": { "belongs_to": "customers", "fk": "customer_id", "fields": ["name"] },
    "items":    { "has_many": "order_items", "fk": "order_id", "sort": { "pos": 1 }, "limit": 50,
                  "lookup": { "product": { "belongs_to": "products", "fk": "sku", "pk": "code" } } },
    "invoice":  { "has_one": "invoices", "fk": "order_id" }
  }
}'
```

```go [Go]
rows, err := orders.Query(ndb.Eq("status", "paid")).
	Sort(ndb.Desc("created_at")).
	Limit(20).
	Lookup("customer", ndb.BelongsTo("customers", "customer_id").Fields("name")).
	Lookup("items", ndb.HasMany("order_items", "order_id").Sort(ndb.Asc("pos")).Limit(50).
		Lookup("product", ndb.BelongsTo("products", "sku").PK("code"))).
	Lookup("invoice", ndb.HasOne("invoices", "order_id")).
	All(ctx)
```

```ruby [Ruby]
orders.query(status: "paid").sort(created_at: -1).limit(20).lookup(
  customer: { belongs_to: "customers", fk: "customer_id", fields: ["name"] },
  items: { has_many: "order_items", fk: "order_id", sort: { pos: 1 }, limit: 50,
           lookup: { product: { belongs_to: "products", fk: "sku", pk: "code" } } },
  invoice: { has_one: "invoices", fk: "order_id" }
).all

# Rails: Order.where(status: "paid").order(created_at: :desc).includes(:customer, :invoice, items: :product)
```

```ts [TypeScript]
const rows = await orders
  .query({ status: 'paid' })
  .sort({ created_at: -1 })
  .limit(20)
  .lookup('customer', belongsTo('customers', 'customer_id', { fields: ['name'] }))
  .lookup('items', hasMany('order_items', 'order_id', { sort: { pos: 1 }, limit: 50,
    lookup: { product: belongsTo('products', 'sku', { pk: 'code' }) } }))
  .lookup('invoice', hasOne('invoices', 'order_id'))
  .all()
```

:::

```json
{ "_id": "o1", "customer_id": "c1", "status": "paid",
  "customer": { "name": "Ana" },
  "items": [ { "_id": "i2", "order_id": "o1", "sku": "B", "pos": 1, "product": { "_id": "p2", "code": "B", "name": "Teclado" } } ],
  "invoice": null }
```

| Relation | The foreign key is | Match | The row gets |
|---|---|---|---|
| `belongs_to: "customers"` | here | `row.fk == customers.pk` | an object, or `null` |
| `has_many: "order_items"` | there | `order_items.fk == row.pk` | an array (100 documents unless `limit`, at most 1000) |
| `has_one: "invoices"` | there | `invoices.fk == row.pk` | an object, or `null` |

| Key | Meaning |
|---|---|
| `foreign_key`, `fk` | the field of the foreign key. Required. |
| `primary_key`, `pk` | the field it points to. Default `_id`. |
| `query`, `sort` | filter and order the documents of a `has_many` or `has_one` (the keys of a search). On a `belongs_to`, `query`, `sort` and `limit` are `400 lookup_invalid`. |
| `limit` | cut the documents of a `has_many` (1 to 1000). On a `has_one` (always one document) it is `400 lookup_invalid`. |
| `fields` | keep these fields of the related documents. |
| `lookup` | the lookups of the related documents. Two levels at most. |

- **The other side must be indexed:** `_id`, a field in its `indexes`, or the first field of a compound index. Else `400 lookup_unindexed` with the `_indexes` route to call. (The Rails associations create that index.)
- **Cost:** a `belongs_to` is one query for every key of the page (`$in`). A `has_many` or `has_one` is one query for each row, with its own `limit`, four at a time. Keep pages small and `limit` set.
- **Limits:** one answer attaches 10 000 documents at most, else `422 lookup_too_large`.
- **A foreign key that is an array** (`"tags": ["A","B"]`) matches each element; a `belongs_to` then gives an array.
- **`fields`** of the search: a foreign key it leaves out is read for the lookup and removed from the row.
- **The name** of a lookup replaces a field of that name in the row.
- **Scope:** a [scoped token](configuration.md#scoped-tokens) must read every collection of the lookup, else `403 forbidden`.
- **Consistency:** each collection reads its own snapshot. A write between two reads can show on one side only (as MongoDB without a transaction).
- A pattern (`logs-*`) or a partitioned collection can be the source of a lookup. Only a partitioned collection can be the other side: a pattern there is `400 lookup_invalid` (the other side is one collection name). `_count` takes no lookup.
- An `_aggregate` takes [`$lookup`](aggregate.md#lookup) at the end of the pipeline.

## Filters

A filter is the `query` of a search, a count, a [watch](watch.md), an [update](#update-by-query) or a [delete](#delete-by-query), and the `$match` of [aggregate](aggregate.md). It is the same language everywhere.

A field with a plain value means equality: `{"status": "answered"}`. Several fields in one object are ANDed.

| Operator | Meaning |
|---|---|
| `$eq`, `$ne` | equal, not equal. `$ne` also matches a missing field. |
| `$gt`, `$gte`, `$lt`, `$lte` | compare. Only values of the same type match: `{"$gt": 1}` never matches a string. The operand must be a scalar. |
| `$in`, `$nin` | in / not in an array. `$nin` also matches a missing field. |
| `$exists` | `true` or `false`. Missing is not `null`. |
| `$not` (in a field) | negate an operator object: `{"duration": {"$not": {"$gt": 10}}}`. |
| `$regex`, `$options` | string match, see [Regular expressions](#regular-expressions). |
| `$elemMatch` | one element of an array matches the whole inner filter, see [Array elements](#array-elements). |
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
- Unknown operators are `invalid_query`.
- Time: for the collection `time` field, RFC 3339 strings are compared as instants, so offsets do not matter. A string that starts with `now` is [date math](#date-math).
- Cross-type order (sort, cursors, indexes): `missing < null < number < string < time < object < array < bool`. A time is an RFC 3339 string of any field (see [Times](#times)).

### Indexes

An index makes a filter read only the documents that can match, not the whole collection. Declare one in `indexes` of the collection ([Collections](http/collections.md)), or add it later with `PUT /{coll}/_indexes/{field}`. The `X-Ndb-Plan` header of an answer names the index that was used (`index:status`), or `scan`.

An index of **one field** (`"status"`) serves `$eq`, `$in` and the range operators (`$gt`, `$gte`, `$lt`, `$lte`) on that field.

A **compound index** has 2 to 4 fields, written with commas: `"account_id,created_at"`. The order of the fields matters. It serves a filter that has:

1. equality (or `$in`) on its first fields, from the first one;
2. then, at most, a range on the next field.

| Filter | With `"account_id,created_at"` |
|---|---|
| `account_id = x` and `created_at >= t` | uses it: both fields |
| `account_id = x` and `created_at = t` | uses it: both fields |
| `account_id = x` | uses it: the first field (an index of `account_id` alone is not needed) |
| `account_id in [x, y]` and `created_at < t` | uses it |
| `created_at >= t` | does not use it: the first field is not in the filter |

- A condition on a field that the index does not hold is tested on each document the index gives, so the answer is always the same as with no index.
- An array field gives one entry per element, as in an index of one field. A range of two sides on an array field matches when one element passes each side (`[1, 3]` matches `> 1` and `<= 2`), with or without an index.
- The index does not order the rows: a `sort` still reads the documents that the filter keeps. So put in the index the fields that cut the most.
- A compound **unique** key (`"unique": ["account_id,email"]`) allows one live document per tuple of values: an email is unique inside an account. A document where one of the fields is missing or `null` is not checked.

On 300 000 documents of 300 accounts, `account_id = x and amount >= 100 and amount < 200` reads 1003 documents in 36 ms with an index of `account_id`, and 5 documents in 1 ms with `"account_id,amount"`.

An [encrypted field](encryption.md) accepts only `$exists`. A **blind** field also accepts `$eq`, `$in`, `$ne` and `$nin`, and an index or a unique key on it: the daemon compares the hash of the value (`<field>_bidx`, the plan shows `index:cpf_bidx`). A range, `$regex` or a sort on an encrypted field is `invalid_query`.

### Regular expressions

```json
{ "query": { "title": { "$regex": "^alpha", "$options": "i" } } }
```

- `$regex` is a string pattern, at most 1 KiB. `$options` is optional and needs `$regex`. Flags: `i` (ignore case), `m` (`^` and `$` match at each line), `s` (`.` matches a newline), `x` (extended, see below). Any other flag is `invalid_query`.
- Syntax is Go **RE2**, not PCRE: there are no backreferences (`\1`) and no look-around (`(?=...)`, `(?<!...)`). Matching is linear in the text, so a pattern cannot cause catastrophic backtracking. An invalid pattern or flag is `invalid_query` with the RE2 message.
- `x` is done by ndb, not by RE2: white space and `# ...` comments outside a character class are removed before compiling. Escape them (`\ `, `\#`) or put them in a class (`[ ]`) to keep them.
- Only strings match. A number, `null`, a boolean, an object or a missing field never matches. On an array field the row matches when any string element matches. `$regex` works in dot paths, in `$not` (field and top level), `$and` and `$or`, and next to other operators of the same field. `{"a": {"$not": {"$regex": "x"}}}` also matches a missing field, like the other negations.
- A time field is matched as its RFC 3339 text.
- The pattern is compiled once per query, not once per row.
- Planner: a pattern anchored with `^`, with a literal start (`^abc`, `^abc.*`, `^ab*`), and without `i` or `m`, is turned into the range `[abc, abd)` on that field. A ready index on the field answers it (`X-Ndb-Plan: index:<field>`), and the zone maps of the segments skip row groups outside the range (`row_groups_skipped`). The regex is still run on every candidate. Without such a prefix (no `^`, a group or alternation first, `i`, `m`, or only `$not`/`$or`) the regex is a filter on a scan. `explain: true` shows which one ran.

### Date math

On the collection `time` field (and the `expires_field`), a string that starts with `now` is an instant relative to the time of the request:

```json
{ "at": { "$gte": "now-7d/d" } }
{ "at": { "$gte": "now-1h", "$lt": "now" } }
{ "at": { "$gte": "now/M" } }
```

```
now ( +N<unit> | -N<unit> )* ( /<unit> )?
units: s m h d w M y      (m = minute, M = month)
```

| Expression | Meaning (request at Thu 2026-11-05 14:30:15 UTC) |
|---|---|
| `now-1h` | 2026-11-05 13:30:15 |
| `now-7d/d` | 2026-10-29 00:00:00 |
| `now/w` | 2026-11-02 00:00:00 (weeks start on Monday) |
| `now/M` | 2026-11-01 00:00:00 |
| `now-1M/M` | 2026-10-01 00:00:00 |

- `/<unit>` rounds down to the start of the unit, in UTC.
- It works in `$eq`, `$ne`, `$gt`, `$gte`, `$lt`, `$lte`, `$in`, `$nin` and plain equality, so in every `query` (search, count, update, delete, watch) and in the `$match` of an aggregate. The result is a time value, so indexes and zone maps work as with an RFC 3339 string.
- `now` is read once per request: every `now` of one query is the same instant. A search cursor keeps the `now` of its first page, so the pages of one walk see the same window. An aggregate reads `now` on each request. A `watch` reads it when the stream opens.
- On any other field, `"now-1h"` is a plain string.
- A broken expression (`now-7x`, `now--1d`, `now/`) is `invalid_query`.

### Times

A string in RFC 3339 with a time (`2026-11-12T15:00:05Z`) is an instant on **every** field, not only on the collection `time` field. So two times compare in the right order whatever their width or offset: `"...:05Z"`, `"...:05.1Z"`, `"...:05.100Z"` and `"...12:00:05.100-03:00"` are instants, and the last three are equal.

```json
{ "next_at": { "$lte": "2026-11-12T15:00:05.300Z" } }
```

- It works in every filter, sort, cursor, index, `$min` and `$max`, and in `$match` and `$group` of an aggregate. An aggregate gives a time back in UTC (`2026-11-12T15:00:05.1Z`).
- A string must have the date, `T`, the time with seconds, and an offset or `Z`. `"2026-11-12"`, `"15:00"` and other text stay strings. In the cross-type order a time comes after a string.
- `_id` is always text: an id that looks like a time is found by its exact text.
- `$regex` reads the text as it is stored.
- Date math (`now-1h`) is only for the `time` field and the `expires_field`. On another field, use an RFC 3339 string.
- The SDKs write a time in UTC with milliseconds, always the same width (`2026-11-12T15:00:05.000Z`): Ruby and Rails with `iso8601(3)`, TypeScript with `Date#toISOString`, Go with `ndb.TimeLayout` (a `time.Time` field, a `time.Time` in a filter, or `ndb.Time`). The stored text stays as it was sent.
- A data dir of an older version has the index keys of a time string as text. At the first start of this version, each index builds its keys again in the background (the old key files are removed), as for a new index: queries scan until it is `ready`.

### Array elements

Conditions on dot paths into an array match each on its own element: `{"items.sku": "a", "items.qty": {"$gt": 2}}` matches `[{"sku":"a","qty":1},{"sku":"b","qty":5}]`. `$elemMatch` asks for one element that matches all of them:

```json
{ "items": { "$elemMatch": { "sku": "a", "qty": { "$gt": 2 } } } }
{ "scores": { "$elemMatch": { "$gte": 10, "$lt": 20 } } }
```

- Field keys (and `$and`, `$or`, `$nor`, `$not`) filter an array of objects. Operators only filter an array of scalars: the element itself.
- A field that is not an array, or is missing, does not match. `$elemMatch` can nest, and works in `$not`, `$or` and dot paths (`"order.items"`).
- `$elemMatch` must be the only operator of its field. Put other conditions on the same field in `$and`.
- Plan: `$elemMatch` itself uses no index. Add a condition that an index can answer (`"items.sku": "a"`) and the index gives the candidates; `$elemMatch` checks each. A row read from a segment is decoded for it.
- `$elemMatch` in `fields` and positional updates (`items.$.qty`) are not supported.

## By _id

Every document has `_id` (see [Document id](configuration.md#document-id)). These routes reach one document by it; the id comes after the action, so any id works. See [Documents](http/documents.md).

::: code-group

```bash [curl]
curl --unix-socket /run/ndb/ndb.sock http://ndb/calls/_doc/0192f3a4-7c1e-7b2a-9f3d-1a2b3c4d5e6f
curl --unix-socket /run/ndb/ndb.sock http://ndb/stock/_update/sku-a -d '{"query":{"qty":{"$gte":1}},"update":{"$inc":{"qty":-1}}}'
curl --unix-socket /run/ndb/ndb.sock -X DELETE http://ndb/calls/_doc/0192f3a4-7c1e-7b2a-9f3d-1a2b3c4d5e6f
```

```go [Go]
call, err := calls.Get(ctx, id)
res, err := stock.Update(ctx, "sku-a", ndb.Ops{"$inc": {"qty": -1}}, ndb.Guard(ndb.Gte("qty", 1)))
deleted, err := calls.Delete(ctx, id)
```

```ruby [Ruby]
calls.get(id)
stock.update("sku-a", { "$inc" => { qty: -1 } }, guard: { qty: { "$gte" => 1 } })
calls.delete(id)
```

```ruby [Rails]
call = Call.find(id)   # call.id is call._id
call.destroy
```

```ts [TypeScript]
const call = await calls.get(id)
await stock.update('sku-a', { $inc: { qty: -1 } }, { guard: { qty: { $gte: 1 } } })
await calls.delete(id)
```

```bash [CLI]
ndb get calls 0192f3a4-7c1e-7b2a-9f3d-1a2b3c4d5e6f
ndb update stock sku-a '{"$inc":{"qty":-1}}'
ndb delete calls 0192f3a4-7c1e-7b2a-9f3d-1a2b3c4d5e6f
```

:::

- `GET /{coll}/_doc/{id}` answers the document with `_v`, or `404 not_found`.
- `POST /{coll}/_update/{id}` takes `update` (the write operators) and an optional `query` of guards on the same document, like [update by query](#update-by-query) with `{"_id": ...}` added. `upsert` and `expire_in` work; `many` does not.
- `DELETE /{coll}/_doc/{id}` answers `{"deleted":0}` or `{"deleted":1}`.

## Update by query

`POST /{coll}/_update_by_query` ([route](http/queries.md#post-coll-update-by-query)) takes a `query` and an `update` with the write operators. The daemon matches the query and writes the result inside the single writer of the collection, against the newest state. Two updates never interleave, so a guarded decrement or an `$inc` needs no retry.

::: code-group

```bash [curl]
# take one unit of stock, only if there is one
curl --unix-socket /run/ndb/ndb.sock http://ndb/stock/_update_by_query \
  -d '{"query":{"sku":"a","qty":{"$gte":1}},"update":{"$inc":{"qty":-1}}}'
# {"matched":1,"modified":1,"doc":{"sku":"a","qty":4,"_v":7}}
```

```go [Go]
res, err := stock.UpdateBy(ctx,
	ndb.And(ndb.Eq("sku", "a"), ndb.Gte("qty", 1)),
	ndb.Ops{"$inc": {"qty": -1}},
)
// res.Matched, res.Modified, res.Doc
```

```ruby [Ruby]
stock.update_by({ sku: "a", qty: { "$gte" => 1 } }, { "$inc" => { qty: -1 } })
# => {"matched" => 1, "modified" => 1, "doc" => {...}}
```

```ruby [Rails]
Stock.where(sku: "a", qty: { "$gte" => 1 }).update("$inc" => { qty: -1 })

stock.decrement!(:qty)   # atomic $inc on the daemon, refreshes the attribute
```

```ts [TypeScript]
const res = await stock.updateBy({ sku: 'a', qty: { $gte: 1 } }, { $inc: { qty: -1 } })
// { matched, modified, doc }
```

```bash [CLI]
ndb update stock '{"sku":"a","qty":{"$gte":1}}' '{"$inc":{"qty":-1}}'
```

:::

A key of the operators without `$` is an error in the SDK, before anything is sent.

### Body keys

| Key | Meaning |
|---|---|
| `query` | the filter, see [Filters](#filters). It must not be empty. |
| `update` | the write operators: `$set`, `$setOnInsert`, `$unset`, `$inc`, `$push`, `$pull`, `$min`, `$max`, each an object of fields (dot paths allowed) |
| `many` | update every match, see [Which documents](#which-documents) |
| `upsert` | create the document when nothing matches, see [Upsert](#upsert) |
| `expire_in` | set `expires_field` to now + this duration |

A write operator in `query`, or another key in `update`, is `400 unknown_operator`.

### Write operators

| Operator | Meaning |
|---|---|
| `{"$set": {"f": v}}` | set the field |
| `{"$setOnInsert": {"f": v}}` | set the field only when an [upsert](#upsert) creates the document. On a match it does nothing, so a racing writer is never overwritten. |
| `{"$unset": {"f": true}}` | remove the field (`true` or `""`) |
| `{"$inc": {"f": n}}` | add a number. A negative number subtracts. A missing field is 0. A field that is not a number is `invalid_field`. Integers stay exact, also above 2^53: a sum of two integers out of the int64 range is `invalid_field` (integer overflow), and the document does not change. A fraction gives a float. |
| `{"$push": {"f": v}}` | append one value. `{"$push": {"f": {"$each": [a, b]}}}` appends many. A missing field becomes a new array. A field that is not an array is `invalid_field`. |
| `{"$pull": {"f": v}}` | remove every element equal to `v`. A condition removes the elements that match it: `{"$gte": 5}`, or `{"status": "done"}` for arrays of objects. |
| `{"$min": {"f": v}}`, `{"$max": {"f": v}}` | set when the field is missing or `v` is lower (higher). Numbers, times (an RFC 3339 string, see [Times](#times)), or strings in lexical order. A different type than the field is `invalid_field`. Numbers compare exactly, also integers above 2^53. |

Several operators can go in one `update`, on different fields:

```json
{
  "query": { "_id": "order-1" },
  "update": {
    "$push": { "events": { "at": "2026-11-06T12:00:00Z", "kind": "paid" } },
    "$inc": { "attempts": 1 },
    "$max": { "last_seen": "2026-11-06T12:00:00Z" },
    "$set": { "status": "paid" }
  }
}
```

### Arrays

`$push` and `$pull` change an array inside the document in one atomic write. Two concurrent pushes both land.

```bash
# append one event
curl ... /orders/_update/o1 -d '{"update":{"$push":{"events":{"kind":"paid"}}}}'

# append many
curl ... /orders/_update/o1 -d '{"update":{"$push":{"tags":{"$each":["vip","eu"]}}}}'

# remove a value, or the elements that match a condition
curl ... /orders/_update/o1 -d '{"update":{"$pull":{"tags":"eu"}}}'
curl ... /orders/_update/o1 -d '{"update":{"$pull":{"events":{"kind":"draft"}}}}'
curl ... /orders/_update/o1 -d '{"update":{"$pull":{"scores":{"$lt":10}}}}'
```

### Answers

Always HTTP 200 when the body is valid:

- One document: `{"matched":1,"modified":1,"doc":{...}}`. `doc` is the new document with `_v`.
- Nothing matched: `{"matched":0,"modified":0}`. That is not an error. For a guarded decrement it means "sold out".
- With `many`: `{"matched":N,"modified":N}`, with no `doc`.
- `modified` is 0 when the operators leave the document as it is (for example `$max` with a lower value). No new version is written then.

### Which documents

- Without `many`, a query with the id field uses the id lookup. Without the id, the first matching document is used.
- With `"many": true`, the query must be served by the id or a ready secondary index. Otherwise the call is `400 unindexed_many`, so a big update never blocks the writer on a full scan.
- A query must not be empty (`400 empty_filter`). Expired documents are never matched.
- A `many` write has no size limit. ndb lists the matches one time, then writes them in WAL records of up to half of `max_line_size`. Other writes to the collection run between two records. Each record is atomic, but the write as a whole is not: after a crash, the records already written stay, and you can send the same call again. Each document is written at most one time, so an `$inc` never runs two times on the same document.

```bash
curl ... /calls/_update_by_query -d '{"query":{"status":"failed"},"update":{"$set":{"status":"retry"}},"many":true}'
# {"matched":3,"modified":3}
```

### Optimistic locking

ndb uses optimistic locking by default; the overview of optimistic locking, pessimistic locking and transactions is on [Concurrency](concurrency.md).

`_v` in the query is the version of the document. Read with `versions: true`, then write with that `_v`. `matched: 0` means somebody else wrote first.

```json
{ "query": { "_v": 7 }, "update": { "$set": { "note": "checked" } } }
```

That is the body of `POST /{coll}/_update/c1`; the SDKs call it a guard (`Guard(Eq("_v", 7))`, `guard: { _v: 7 }`).

#### with_retry: read, change, write, retry

Every SDK has `with_retry` (`WithRetry` in Go, `withRetry` in TypeScript). It does the guard for you:

1. It reads the document and its `_v`.
2. It calls your function with the document. Change the fields there.
3. It writes the fields that changed (`$set`, and `$unset` for a removed field), with the guard `_v`.
4. When another write came first (`matched: 0`), it reads the document again and calls your function again. After 5 tries (`retries`) it fails with `stale_object`.

An error in your function stops it, and nothing is written. When your function changes nothing, nothing is written. Nothing waits: there is no lock on the daemon, so a client that dies holds nothing. Use it when the new value depends on a decision in your code. When one operator does the job, use a [conditional update](#update-by-query): it is one call, with no retry.

Your function can run more than one time. When it has an effect outside ndb (a refund, an email), use a [lock](#pessimistic-locking-one-holder-at-a-time).

**1. Debit a balance from two requests at the same time.** The second request reads the new balance and debits again, so both debits land.

```ruby
# Rails
Wallet.find("w1").with_retry do |w|
  raise InsufficientFunds if w.balance_cents < 500

  w.balance_cents -= 500
end
```

```go
// Go
w, err := wallets.WithRetry(ctx, "w1", func(w *Wallet) error {
	if w.Balance < 500 {
		return ErrInsufficientFunds
	}

	w.Balance -= 500

	return nil
})
```

The same debit without a decision in code is one conditional update, with no retry:

```bash
curl ... /wallets/_update/w1 -d '{"query":{"balance_cents":{"$gte":500}},"update":{"$inc":{"balance_cents":-500}}}'
# {"matched":0,...} when the balance is too low
```

**2. The payment webhook arrives twice.** The second call sees `paid` and writes nothing. Without the retry, it gets `stale_object`, answers 500, and the gateway sends the webhook again.

```ruby
# Rails
Payment.find(event.payment_id).with_retry do |p|
  next if p.status == "paid"

  p.status = "paid"
  p.paid_at = Time.current
end
```

```ts
// TypeScript
await payments.withRetry(event.paymentId, (p) => {
  if (p.status === 'paid') return

  p.status = 'paid'
  p.paid_at = new Date()
})
```

**3. A setting with a rule (the seat limit of a plan).** Two invites at the same time cannot pass the limit.

```go
// Go
_, err := accounts.WithRetry(ctx, "acc_1", func(a *Account) error {
	if a.Seats >= a.MaxSeats {
		return ErrSeatLimit
	}

	a.Seats++

	return nil
})
```

```ruby
# Ruby (plain client): documents are hashes
accounts.with_retry("acc_1") do |a|
  raise SeatLimit if a["seats"] >= a["max_seats"]

  a["seats"] += 1
end
```

### Pessimistic locking: one holder at a time

A lock gives one caller the document, like `SELECT ... FOR UPDATE`. Until the lock is released, no other caller takes it and no other write changes the document. The code between the lock and the release runs one time. Use it when that code has an effect outside ndb that must not happen twice:

```ruby
# Rails. A second request waits at lock.find, then sees "refunded".
Payment.lock.find("p1") do |p|
  next if p.status == "refunded"

  Gateway.refund(p.charge_id)
  p.update!(status: "refunded")
end
```

```go
// Go
_, err := payments.Lock(ctx, "p1", func(p *Payment) error {
	if p.Status == "refunded" {
		return nil
	}

	if err := gateway.Refund(p.ChargeID); err != nil {
		return err
	}

	p.Status = "refunded"

	return nil
})
```

```ts
// TypeScript
await payments.lock('p1', async (p) => {
  if (p.status === 'refunded') return

  await gateway.refund(p.charge_id)
  p.status = 'refunded'
})
```

```ruby
# Ruby (plain client)
payments.lock("p1") do |p|
  next if p["status"] == "refunded"

  Gateway.refund(p["charge_id"])
  p["status"] = "refunded"
end
```

How it works ([`POST /{coll}/_lock/{id}`](http/documents.md#post-coll-lock-id)):

- The daemon gives a token and the document in one step. The SDK writes with the token and releases the lock at the end, also when your code fails. In Go, TypeScript and plain Ruby the fields that your function changed are written for you; an error writes nothing.
- Another lock of the same document waits (`wait`, default 10 s), then fails with `locked`.
- A write of the document from any other place waits for the release, then runs. Reads do not wait.
- The lock has a `ttl` (default 30 s, up to 5 minutes). A process that dies holds the lock only until then. Set `ttl` over the longest time your code can take: after it, the write is refused with `lock_expired`, but the effect outside ndb is done. For that case, also give the outside call an idempotency key when it takes one.
- ndb has no transaction: a lock is one document. Do not take two locks in a different order in two places (they can wait for each other until `wait` ends).

| | `with_retry` | `lock` |
|---|---|---|
| Your code runs | one or more times | one time |
| Other writers | never wait | wait for the release |
| Calls to the daemon | read + write (again on a conflict) | lock + write + unlock |
| Use it when | the code only changes the document | the code has an effect outside ndb |

### Upsert

With `"upsert": true` and no match, a document is created from the plain equality fields of the query (dot paths too). Then the operators run on it: `$inc` starts from 0, `$push` creates the array, `$min` and `$max` set the value. Without the id field in the query, an id is generated like an insert does.

The answer is `matched: 0`, `modified: 1` and the `doc`. If the id exists but the rest of the query does not match, the call is `409 duplicate_id`.

Create once, never overwrite: the query finds the document, `$setOnInsert` fills it only when it is new. Two racing calls make one document.

```bash
curl ... /users/_update_by_query -d '{"query":{"email":"a@b.c"},"update":{"$setOnInsert":{"name":"Ana","plan":"free"}},"upsert":true}'
```

A counter that expires, as a rate limit. An upsert does not create the collection (`404 unknown_collection`), so declare it first, with an `expires_field` for `expire_in`:

```bash
curl -X PUT ... /hits -d '{"expires_field":"expires_at"}'
curl ... /hits/_update/ip:1.2.3.4 -d '{"update":{"$inc":{"n":1}},"upsert":true,"expire_in":"60s"}'
```

### Contract errors

HTTP 400:

| Code | Cause |
|---|---|
| `unknown_operator` | a key of `update` that is not a write operator, a write operator in `query`, or a top-level `$` key of `query` that is not `$and`, `$or`, `$nor` or `$not`. An unknown operator on a field (`{"n":{"$bogus":1}}`) is `invalid_query`, as in a search. |
| `no_update` | no write operator |
| `conflicting_update` | the same field, or a field and its parent (`a` and `a.b`), in two operators |
| `invalid_field` | a field name that starts with `$`, or a value of the wrong type |
| `unindexed_many` | `many` without the id or an indexed field in the query |
| `empty_filter` | no query |
| `immutable_id` | an operator on the id field. `_v` cannot be written either. |

The WAL line of each changed document is the resulting document with the next version, so replay and `watch` need no operators. In direct mode, updates and deletes are `409 direct_collection`.

## Delete by query

`POST /{coll}/_delete_by_query` ([route](http/queries.md#post-coll-delete-by-query)) takes `{"query": {...}, "many": false}` and answers `{"deleted":N}`. Nothing matched is `{"deleted":0}`. `many` follows the same index and size rules as update. To remove every document of a collection, use [truncate](operations.md#truncate-and-drop): an empty query is `empty_filter`.

When the index alone answers the query (one indexed field, with `$eq`, `$in` or a range, and no `null`), ndb deletes the matches without reading them. This is the fast path for sealed data. Example: on 1M documents, a delete of 868k sealed documents by an indexed `status` takes about 3 s. A query that also has a field with no index still works, but ndb reads each candidate to test it.

::: code-group

```bash [curl]
curl --unix-socket /run/ndb/ndb.sock http://ndb/calls/_delete_by_query -d '{"query":{"call_id":"c2"}}'
curl --unix-socket /run/ndb/ndb.sock http://ndb/calls/_delete_by_query -d '{"query":{"status":"expired"},"many":true}'
```

```go [Go]
deleted, err := calls.DeleteBy(ctx, ndb.Eq("call_id", "c2"))
deleted, err = calls.DeleteBy(ctx, ndb.Eq("status", "expired"), ndb.Many())
```

```ruby [Ruby]
calls.delete_by(call_id: "c2")                         # => {"deleted" => 1}
calls.delete_by({ status: "expired" }, many: true)
```

```ruby [Rails]
call.destroy
Call.where(status: "expired").delete_all   # returns how many it deleted
```

```ts [TypeScript]
await calls.deleteBy({ call_id: 'c2' })
await calls.deleteBy({ status: 'expired' }, { many: true })
```

```bash [CLI]
ndb delete calls '{"status":"expired"}' --many
```

:::

## Collection patterns

A collection name with one `*` at the end (`logs-2026-*`), or a list split by commas (`logs-2026-11,logs-2026-10`, which can mix patterns), reads every collection it names, for collections split by time: `_search`, `_count` and `_aggregate` work over all of them, and each row has `_coll`, the name of its collection. The rules are on [Collection patterns](http/queries.md#collection-patterns).

## Pagination

Both modes work on `_search` and [aggregate](aggregate.md).

**Offset**: `skip` + `limit`. Simple, good for page numbers. The cost grows with `skip`, and rows can move between pages when data changes.

```json
{ "query": { "status": "answered" }, "sort": { "ts": -1 }, "skip": 100, "limit": 50 }
```

**Cursor**: `limit`, then pass the `X-Ndb-Next` header back as `after`.

```json
{ "query": { "status": "answered" }, "sort": { "ts": -1 }, "limit": 50, "after": "eyJz..." }
```

- The token is opaque. Do not parse or build it.
- It is bound to the `query` and `sort` (or to the pipeline). A token used with another query is `cursor_mismatch`. A broken token is `cursor_invalid`.
- Rows inserted during the walk do not move pages already read.
- With `snapshot: true`, the cursor keeps one snapshot for 60 s. After that the error is `cursor_expired`.
- An aggregate cursor runs the pipeline again for each page.

Error codes are listed in [Errors](http/errors.md).
