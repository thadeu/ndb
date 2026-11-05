# Query language

Queries are JSON. The same body works from every SDK and from `curl`. The daemon owns all query meaning. The SDKs only build JSON.

## find

`POST /v1/c/{coll}/find`

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
| `where` | filter object. See below. No `where` matches every document. |
| `fields` | return only these paths (dot paths allowed). |
| `sort` | object `field: 1` or `-1`. The key order is the sort order. The document id is always the last sort key. With no `sort`, the order is insertion order. |
| `limit`, `skip` | non-negative integers. |
| `after` | cursor from the previous page. See [Pagination](#pagination). |
| `count` | add the total number of matches in header `X-Ndb-Total`. |
| `versions` | add `_v`, the document version, to each row. Send it back as `if_version`. |
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

## Operators

A field with a plain value means equality: `{"status": "answered"}`. Several fields in one object are ANDed.

| Operator | Meaning |
|---|---|
| `$eq`, `$ne` | equal, not equal. `$ne` also matches a missing field. |
| `$gt`, `$gte`, `$lt`, `$lte` | compare. Only values of the same type match: `{"$gt": 1}` never matches a string. The operand must be a scalar. |
| `$in`, `$nin` | in / not in an array. `$nin` also matches a missing field. |
| `$exists` | `true` or `false`. Missing is not `null`. |
| `$not` (in a field) | negate an operator object: `{"duration": {"$not": {"$gt": 10}}}`. |
| `$regex`, `$options` | string match, see [Regular expressions](#regular-expressions). |
| `$and`, `$or` | non-empty array of where objects. |
| `$not` (top level) | negate a where object: `{"$not": {"status": "failed"}}`. |

Rules:

- `{"a": null}` matches an explicit `null` only. Use `{"a": {"$exists": false}}` for a missing field.
- Arrays: a field matches when any element matches. Dot paths walk into arrays of objects (`items.sku`).
- Mixing operators and plain keys in one field object is an error: `{"a": {"$gt": 1, "b": 2}}`.
- Unknown operators are `invalid_query`: `$nor` and `$elemMatch` do not exist yet.
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

## aggregate

`POST /v1/c/{coll}/aggregate`

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

Shape:

- Exactly one `$group`.
- Before it: `$match` (all are ANDed) and one `$sort`. The `$sort` matters only for `$first` and `$last`.
- After it: `$match`, `$sort`, `$limit`, `$skip`, `$project`.
- `$group._id` is a field path (`"$status"`), a constant, `null`, a `$dateTrunc`, or an object of those (a composite key). Missing and `null` are the same key. Groups come out sorted by `_id`.
- Field paths start with `$`: `"$a.b"`. A path through an array gives an array value.

| Accumulator | Result |
|---|---|
| `$count` | number of documents. Its argument is `{}`. |
| `$sum` | sum of the numbers. Other values are ignored. No number gives `0`. |
| `$avg` | sum / count of the numbers. No number gives `null`. |
| `$min`, `$max` | lowest / highest value in the cross-type order. Nothing gives `null`. |
| `$first`, `$last` | value of the first / last document of the group. |

`$dateTrunc`: `{"$dateTrunc": {"date": "$ts", "unit": "hour", "timezone": "Europe/Rome"}}`. Units: `minute`, `hour`, `day`, `week` (Monday), `month`. The default timezone is UTC. The result is the bucket start as an RFC 3339 UTC string. A value that is not a time gives `null`.

`$project`: `{"$project": {"n": 1}}` keeps fields; `{"$project": {"n": 0}}` removes them. Do not mix. `_id` stays unless listed with `0`.

The group table is limited in memory. Over the limit the answer is `group_limit_exceeded` (HTTP 422): add a `$match` or use fewer group keys.

The response has the same headers as `find`.

## Pagination

Both modes work on `find` and `aggregate`.

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

## Errors

The body is `{"error":{"code":"...","message":"..."}}`.

| Code | HTTP | Meaning |
|---|---|---|
| `invalid_request` | 400 | Bad body, unknown field, empty `insert`. |
| `invalid_query` | 400 | Bad `where`, `sort`, operator or key. |
| `invalid_pipeline` | 400 | Bad aggregate pipeline. |
| `cursor_invalid` | 400 | The `after` token is broken. |
| `cursor_mismatch` | 400 | The token was made for another query. |
| `cursor_expired` | 400 | The snapshot of the cursor expired. |
| `group_limit_exceeded` | 422 | The group table is over its memory limit. |
| `not_found` | 404 | No document (or blob) with this id. |
| `unknown_collection` | 404 | The collection does not exist. |
| `version_conflict` | 409 | `if_version` does not match the current version. |
| `duplicate_id` | 409 | A document with this id exists. |
| `unique_violation` | 409 | A `unique` field value exists. |
| `catalog_conflict` | 409 | Two declarations of the collection disagree (`id`, `write`, `path`, `time`). |
| `direct_collection` | 409 | Write call on a direct-mode collection. |
| `catalog_invalid` | 400 | The collection definition is not valid. |
| `ttl_not_enabled` | 400 | `expire_in`, `expire` or `persist` on a collection without `expires_field`. |
| `ttl_derived` | 400 | `expire_in`, `expire` or `persist` on a collection whose expiry comes from `ttl` + `time`. |
| `invalid_doc`, `missing_id`, `invalid_id`, `immutable_id`, `invalid_batch`, `invalid_path`, `invalid_index` | 400 | A document or definition is not valid. |
| `request_too_large`, `line_too_large` | 413 | Body or line is over `max_line_size` (or the blob limit). |
| `invalid_blob_ref` | 400 | The blob reference is not `sha256:<64 hex>`. |
| `unauthorized` | 401 | TCP request without the right bearer token (`-token-file`). |
| `forbidden` | 403 | For example, backup over TCP. |
| `history_truncated` | 410 | A `watch` needs WAL files that `wal_retention` deleted. |
| `not_ready` | 503 | The daemon is still recovering. Retry. |
| `closed` | 503 | The daemon is shutting down. |
| `canceled` | 499 | The client left before the answer. |
| `internal` | 500 | A bug or disk error. The message stays in the daemon log. |

A `watch` stream reports `watch_lagged` and `invalid_position` as an error line in the stream. See [http-api.md](http-api.md).
