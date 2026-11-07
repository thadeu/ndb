# Truncate and drop (backlog)

> Decision (2026-11-06): important, after the `_id` work (0.1.3).
> Done (2026-11-07): built as a swap of the collection dir, see
> `internal/engine/truncate.go` and docs/dev/operations.md#truncate-and-drop.

## Why

- Time-split collections (`calls_2026_11`, `logs_2026_w45`, `events_2026`) are
  dropped or emptied whole when their period ends. Today ndb cannot do that.
- There is no route to empty or remove a collection. `delete` cannot do it
  either: an empty filter is `empty_filter`, and `many` without an index is
  `unindexed_many`.
- A truncate must be O(1) in the number of documents: the same cost for 10k or
  10M rows. It must not walk the delete path (one tombstone per document).

## Draft contract

```
POST   /v1/c/{coll}/truncate?confirm={coll}   keep the definition and indexes, remove the data
DELETE /v1/c/{coll}?confirm={coll}            drop: remove the data and the definition
ndb truncate <coll>   /   ndb drop <coll>     ask for confirmation (or -yes)
```

- `confirm` must equal the collection name, else `400 confirm_required`.
- SDKs: `col.Truncate(ctx)` / `col.Drop(ctx)`, `truncate`/`drop` in Ruby, TS,
  and `Model.truncate!` / `Model.drop!` in Rails.
- Direct mode: refused with `409 direct_collection`. The files belong to the app.
  (Maybe later: a drop that only forgets the collection, never the files.)

## Draft design

- The writer appends one `truncate` marker to the WAL, with the next epoch.
  Replay drops everything before the marker.
- Then, outside the writer: swap in empty memtable, head and indexes; move the
  old segments and WAL files to a trash dir; delete the trash in the background.
- `watch`: one `{"op":"truncate"}` event instead of N deletes. Consumers with a
  position before the marker get it once, then continue.
- `wal_retention` and checkpoints: the marker is a checkpoint; older files are
  free to delete at once.
- Drop also removes the catalog entry, the index state in bbolt, and the watch
  consumer positions of that collection.
- Backup taken during a truncate: sees before or after, never half.

## Tests first

- truncate of 100k rows takes about the same time as of 10 rows.
- crash after the marker and before the trash delete: restart shows an empty
  collection and cleans the trash.
- watch consumer sees one truncate event and then the new inserts.
- confirm missing or wrong → 400; direct collection → 409.

## Related: delete by index only (done, 0.3.2)

Done in 0.3.2, together with the fix for `many` writes over `max_line_size`
(`internal/collection/many.go`). A `many` write lists its candidates once,
sorted by segment and row, and writes them in records of half of
`max_line_size`. When `query.IndexOnly` holds, an unchanged candidate is
deleted without reading it. On 1M sealed rows: 868k deleted in about 3 s.
