# Filters: date math and `$elemMatch` (backlog)

> Decision (2026-11-07): after collection patterns (0.4.x). Keep the current
> filter language; do not add an Elasticsearch-like DSL. No full-text search
> (no use case).
> Built (2026-11-07, unreleased): `internal/query/datemath.go`, `elemNode` in
> `internal/query/match.go`, docs/dev/query.md#date-math and #array-elements.
> Changes from the draft:
> - `$elemMatch` must be the only operator of its field.
> - A query with `$elemMatch` decodes segment rows (`Query.NeedsDoc`); the
>   column values cannot keep the element structure.
> - An aggregate does not pin `now` in its cursor (it runs again per page).

## Why

- The filter context of Elasticsearch is the reference people know. ndb
  already covers most of it with its own operators:

  | ES | ndb |
  |---|---|
  | `term` / `terms` | `$eq` / `$in` |
  | `range` | `$gt` `$gte` `$lt` `$lte` |
  | `exists` | `$exists` |
  | `bool` `must` / `should` / `must_not` | `$and` / `$or` / `$nor` |
  | `ids` | `_id: {"$in": [...]}` |
  | `prefix` | `$regex: "^abc"` (uses the index, `query/regex.go`) |
  | `regexp` | `$regex` |

- A second DSL would be a second parser, second docs and second SDK builders
  for the same power. The two gaps that matter are capabilities, not syntax:
  1. Date math in ranges: `now-7d`, `now/d`. The most common log filter.
  2. `nested`: many conditions on the same element of an array. Today
     `{"items.sku": "a", "items.qty": {"$gt": 2}}` can match `sku` in one
     element and `qty` in another.

## 1. Date math

### Draft contract

On the collection `time` field (and the TTL field), a string operand that
starts with `now` is date math:

```json
{ "at": { "$gte": "now-7d/d" } }
{ "at": { "$gte": "now-1h", "$lt": "now" } }
{ "at": { "$gte": "now/M" } }
```

```
now ( (+|-) N unit )* ( /unit )?
unit: s m h d w M y      (m = minute, M = month)
/unit rounds down to the start of the unit, in UTC
```

- Works in every operator that takes a time: `$eq`, `$ne`, `$gt`, `$gte`,
  `$lt`, `$lte`, `$in`, `$nin`. So it works in `find`, `count`, `aggregate`
  `$match`, `watch`, update and delete by filter.
- On a field that is not the time field, `"now-7d"` stays a plain string (no
  magic on user data).
- A broken expression (`now-7x`, `now--1d`) is `400 invalid_query`.
- `now` is read one time per call: every `now` in one query is the same
  instant.
- Rounding in UTC only, in this step. Later, maybe: `"$tz": "America/Sao_Paulo"`
  at the top of the body.
- SDKs: plain strings, nothing to build. Go helper `ndb.Now().Add(-7*day).Day()`
  is optional.

### Draft design

- Parse in `query.FromJSON` when `isTime` is true (`compileOp` in
  `query/parse.go`). The result is a `Time` value, so index ranges, zone maps
  and the planner work unchanged.
- `Options.Now func() time.Time` for tests (default `time.Now`).
- Cursors: the `after` token binds the `where` text, and `now` moves between
  pages. The token keeps the `now` of the first page; the next pages use it.
  So the pages of one walk see the same window.
- `watch` with `where`: `now` is read when the stream opens and stays fixed.
  The docs say so (a sliding window on watch is not a goal).

### Tests first

- `now-7d/d` with a fixed clock gives the expected instant; `/M`, `/w` (ISO
  week, Monday), `/y`.
- `now-1h` on the time field uses the index (`X-Ndb-Plan: index:at`) and skips
  row groups by zone map.
- `"now-1h"` on a non-time field matches only the literal string.
- Cursor pages over 1 s apart keep the same window.
- Broken expressions → `invalid_query`.

## 2. `$elemMatch`

### Draft contract

```json
{ "items": { "$elemMatch": { "sku": "a", "qty": { "$gt": 2 } } } }
{ "tags":  { "$elemMatch": { "$in": ["x", "y"] } } }
```

- On a field: matches when one element of the array matches the whole inner
  filter.
- Inner filter on objects: field keys and `$and`/`$or`/`$nor`/`$not`, the same
  language. Inner filter on scalars: an operator object (`$gt`, `$in`, ...).
- A field that is not an array does not match.
- Allowed in `find`, `aggregate` `$match`, `watch`, update and delete filters.
- No `$elemMatch` in `fields` (projection), no positional `$` update. Not in
  this step.
- Update: `parse_test.go` lists `$elemMatch` as an unknown operator today;
  that test changes.

### Draft design

- New field op `elemMatch` that holds a compiled sub-query; `match.go` walks
  the array and runs the sub-query on each element with the element as root.
- Index: no new index. The planner may use an index on `items.sku` for the
  candidates (an element match implies a field match), then checks
  `$elemMatch` on the row. `query.IndexOnly` is false for it.

### Tests first

- `[{sku:a,qty:1},{sku:b,qty:5}]` with `{sku:a, qty>2}`: no match with
  `$elemMatch`, match with the dot paths (current behavior, kept).
- Scalar arrays: `{"$elemMatch": {"$gte": 10, "$lt": 20}}` needs one element in
  range, not one >= 10 and another < 20.
- Nested: `$elemMatch` inside `$elemMatch`.
- With an index on `items.sku`: plan is `index:items.sku`, result is right.
- Non-array field → no match; non-object operand → `invalid_query`.

## Not now

- `minimum_should_match`: `{"$or": [...], "$min": 2}`. Easy, no use case yet.
- `wildcard` (`a*b?c`): `$regex` covers it.
- Full-text (`match`, analyzers, BM25, `_score`): no use case. For "grep in
  logs", a trigram index that speeds up `$regex` is the cheaper path.
