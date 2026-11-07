# Collection patterns: `logs-2026-*` (backlog)

> Decision (2026-11-07): next, as 0.4.0. Read only. No named aliases.
> Built (2026-11-07, unreleased): `internal/collection/pattern.go`,
> `internal/engine/pattern.go`, docs/dev/query.md#collection-patterns.
> Changes from the draft:
> - `snapshot: true` on a pattern is `invalid_query` (no snapshot over many
>   collections yet).
> - The cursor is one position (last row + its collection), not one per
>   collection: the order is total, so each collection derives its start.
> - `aggregate` merges one group table per collection (not a merged stream).
> - `explain` sums the counters; no entry per collection yet.
> - No `ndb ls <pattern>`; `ndb query 'logs-*'` works as is.

## Why

- Time-split collections (`logs-2026-10`, `logs-2026-11`) are emptied or
  dropped whole since 0.3.3. Reading across them still needs one query per
  collection and a merge in the app.
- A query on the hot partition stays a query on one collection
  (`logs-2026-11`). A report over a year or over all the data needs every
  partition at once: `logs-2026-*`, `logs-*`.
- Named aliases (catalog entry, `ndb.yml`, write target) are more than this
  case needs. A name prefix covers it with no new state.

## Draft contract

```
POST /v1/c/logs-2026-*/find        every collection whose name starts with "logs-2026-"
POST /v1/c/logs-*/aggregate        every collection whose name starts with "logs-"
GET  /v1/c/logs-*                  the list of matched collections
```

- A pattern is a name prefix plus one `*` at the end. `*` anywhere else is
  `400 invalid_pattern`. `*` alone (empty prefix) is also `invalid_pattern`:
  too wide to be on purpose.
- No ambiguity with real names: `*` is not allowed in a collection name
  (`^[a-z0-9][a-z0-9_-]{0,63}$`).
- The match is a plain prefix: `logs-*` also matches `logs-archive`. Naming is
  the user's job; the docs say so.
- The set of collections is taken once, at the start of the call. A collection
  created during the call is not read; one dropped during the call ends its
  part of the stream with no error.
- No match: an empty answer (`find`, `count`, `aggregate`), not
  `unknown_collection`. A report over an empty period is not an error.
- Each row gets `_coll`, the name of its collection (like `_index` in ES).
  Only on pattern calls; a plain call does not change.
- Writes and admin calls on a pattern are `400 pattern_read_only`: insert,
  update, delete, upsert, expire, persist, truncate, drop, PUT of a definition.
- `watch` on a pattern: not in this step (`400 pattern_read_only`).
- Direct mode collections are part of the match like any other.
- SDKs: the collection name is a string, so `ndb.collection("logs-2026-*")`
  works as is for reads. Go `col.Find`, Ruby `ndb.find`, TS `col.where`.
  Rails: `self.collection_pattern = "logs-*"` on a read model, later.
- CLI: `ndb find logs-2026-* ...` and `ndb ls logs-*`.

## Draft design

- `engine.Match(prefix)` lists the collections in name order from the catalog.
- `find`:
  - Each collection runs its own plan (its own indexes, `id`/`index`/`scan`).
  - With `sort`: k-way merge with a heap on the sort key, then `_coll`, then
    `_id`, so the order is total and stable.
  - Without `sort`: collections in name order, each in insertion order.
  - `skip`/`limit` apply after the merge.
- Cursor (`after`): one position per collection plus the set of names, encoded
  in the same opaque token. A cursor from a plain call on a pattern call, or
  the other way, is `cursor_mismatch`.
- `snapshot: true`: one snapshot per collection, taken at the start. There is
  no atomic snapshot over many collections; the docs say so.
- `count`: sum of each collection's total.
- `aggregate`: the pipeline runs over the merged stream of `$match` results.
  `$match` is pushed down to each collection (indexes and zone maps work).
  Partial `$group` per collection and a merge of partials: later, only if a
  benchmark asks for it.
- `explain`: one entry per collection, plus the merge counters.
- Time filters on `logs-*` stay cheap: each segment's zone map skips old row
  groups. Later (maybe): skip a whole collection when its time min/max is out
  of the filter range.
- Limits: at most `max_pattern_collections` (default 256) per call, else
  `400 pattern_too_wide`. Each open collection holds a reader; the cap keeps
  file handles bounded.

## Tests first

- `logs-2026-*` over 3 collections: `find` with `sort: {at: -1}` returns one
  ordered stream; each row has the right `_coll`.
- Cursor pages over 3 collections see every row once; a cursor from another
  pattern is `cursor_mismatch`.
- `count` equals the sum of the per-collection counts.
- `aggregate` `$group` by month over 3 collections equals the result of one
  collection holding the same rows.
- `logs-*` matches `logs-archive` (documented behavior).
- insert / delete / truncate / drop on a pattern → `pattern_read_only`.
- `*`, `lo*gs`, `logs-**` → `invalid_pattern`.
- No match → empty answer, `X-Ndb-Total: 0`.
- A drop of one partition during a long `find` on the pattern: the call ends
  without error and without rows of a half-dropped collection.
- More than `max_pattern_collections` → `pattern_too_wide`.

## Later (not in this step)

> Automatic partitions: done after 1.0 (`partition_by` + `retention`), see
> `internal/engine/partition.go` and docs/dev/http/collections.md.


- `watch` on a pattern (one stream, one consumer position per collection).
- Automatic partitions: `partition_by: month` + `retention: 6mo` in `ndb.yml`;
  ndb creates the next collection and drops the old ones.
- Skip a whole collection by its time range (catalog keeps min/max of `time`).
