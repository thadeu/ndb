# M17 — PITR and rename

Status: built (2026-11-08) · Depends on: M16 · Diagram: the
"PITR do ndb" artifact of 2026-11-08

## Goal

The DynamoDB button: someone deleted data by mistake today, the operator
picks an instant before the mistake, and ndb makes a copy of the collection
as it was then. What came after that instant is not in the copy. A rename
puts the copy in place, so the apps change nothing.

PITR is for the mistake of the day. A mistake older than the window is a job
for the backups (R2).

## Decisions (2026-11-08)

1. **Window: 1 day by default.** `pitr: 1d` in the daemon config applies to
   every engine collection; a collection overrides it (`pitr: off`, `pitr:
   6h`, at most `7d`). Partitions inherit the logical collection.
2. **Model: one snapshot a day + the WAL.** A snapshot is the segment list
   of a checkpoint, hard linked (segments never change), with meta.bolt
   (checkpoint and live bits). The WAL files from the oldest kept snapshot
   on are not deleted. A collection with no write since its last snapshot
   gets no new one.
3. **Restore makes a new collection** (`into`); the source is not touched.
4. **Local only.** Snapshots and WAL are on the data disk: PITR protects
   against human error, not a dead disk. Shipping the WAL to R2 every minute
   is a later phase.
5. **Cost** is the writes of the last day plus the segments compaction
   rewrote in that day (the old files stay linked). RAM does not change. The
   number of collections does not matter: an idle collection or an old
   partition costs nothing.
6. **Direct collections have no PITR**: the files of the apps are the
   history; ndb never changes them.
7. **Rename** exists apart from PITR, and closes its flow.

## Flow

```
POST /calls/_restore          {"until":"2026-11-12T14:04:00Z","into":"calls_restored"}
POST /calls_restored/_rename  {"to":"calls","replace":true}
```

`replace: true` moves the current `calls` aside atomically: with PITR on it
is kept for the window (a dropped collection, restorable), so a restore can
be undone.

## Epics

### E17.1 Rename

- [x] `POST /{coll}/_rename {"to", "replace"}`: under the engine swap lock,
      close, rename the dir, update the catalog, move the watch cursors,
      reopen. A partitioned collection renames its partitions with it.
- [x] `replace: true` with an existing target: the target goes aside (kept
      by the PITR window, else deleted in the background like a drop).
      Without `replace`: `409 collection_exists`.
- [x] Errors: `404 unknown_collection`, `400 catalog_invalid` (bad name,
      a pattern, a partition name), `409 collection_exists`.
- [x] Scope: admin on both names. CLI `ndb rename <coll> <to> [-replace]`.
      SDKs: `rename(to, replace:)`. Docs, console (a Rename action).

### E17.2 Snapshots

- [x] Config: `pitr` (daemon default `1d`, per collection override, `off`).
- [x] A daily snapshot per engine collection with writes since the last
      one: under the commit lock (no seal or compaction commit in between),
      hard link the live segments and their key files into
      `pitr/<unix-ms>/`, copy meta.bolt (bolt `Tx.CopyFile`).
- [x] Keep the snapshots that cover the window (today and yesterday for
      1d); delete older ones.
- [x] TruncateWAL never deletes a WAL file the oldest kept snapshot needs.
- [x] Blob GC keeps the blobs younger than the window (a restored doc may
      point to them).

### E17.3 Restore

- [x] `POST /{coll}/_restore {"until", "into"}` and, for a collection that
      was dropped or replaced within the window,
      `POST /_restore {"collection", "until", "into"}`.
- [x] Build `into` from the newest snapshot at or before `until`: link its
      segments, copy its meta.bolt, copy the WAL records from its position
      up to `until` (by the record time), then open it: the normal recovery
      replays them. A partitioned collection restores the partitions with
      writes in the window; the others are linked as they are.
- [x] Errors: `400 pitr_off`, `400 out_of_window` (with the oldest instant
      that can be restored), `409 collection_exists` for `into`.
- [x] Scope: admin on the source and on `into`. CLI `ndb restore <coll>
      --until T --into NAME` (the backup restore keeps its file argument).
- [x] `GET /{coll}/_pitr`: the window (oldest and newest instant) and the
      disk it uses. Console: a Restore action with a date picker.

### E17.4 Truncate and drop with PITR

- [x] With PITR on, truncate and drop move the collection dir to
      `pitr-trash/<name>.<unix-ms>/` for the window instead of deleting it;
      `POST /_restore` can bring it back.

## As built

- Rename is crash safe through `rename.json` (stages aside, move); the
  next start finishes it.
- Snapshots live in `<coll>/pitr/<unix ms>/` (segments linked, meta.bolt,
  point.json), so they follow a rename. A trash entry keeps `trash.json`
  (definition, time, window); a restore of a truncated, dropped or replaced
  name reads the trash entry that left after `until`.
- Blob GC holds an orphan for the longest window (in memory: a restart only
  delays a delete).
- Ruby names the instant `at:` (`until` is a keyword).
- Extra: `GET /_pitr`, `ndb pitr`, `-until 15m`, the `pitr` column of
  `ndb size`.

## Tests first

- Rename: plain, partitioned, replace, cursors follow, crash in the middle
  (the swap leaves one name or the other, never none).
- Restore: delete_by_query at T, restore to T-1s has every doc; restore to
  T+1s has none of the deleted; a seal and a compaction between the
  snapshot and T; restore of a dropped collection; out of window.
- The window: after a day the old snapshot and its WAL are gone; the disk
  of an idle collection does not grow.
- `make bench` with PITR on: no change in write latency or RSS.

## Exit criteria

- The flow above works from the CLI, the API and the console.
- The ESL bench with `pitr: 1d`: write p99 and RSS as without it.
- Docs: a PITR page (what it protects, what not, the cost), the rename
  route, the config key; CHANGELOG.
