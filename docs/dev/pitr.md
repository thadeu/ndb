# Point in time recovery

Someone deleted data by mistake today. Pick an instant before the mistake, and ndb makes a copy of the collection as it was then. Writes after that instant are not in the copy. A rename puts the copy in place, so the apps change nothing.

```bash
ndb restore calls -until 2026-11-12T14:04:00Z -into calls_restored   # or -until 15m (15 minutes ago)
ndb rename calls_restored calls -replace
```

PITR is for the mistake of the day. An older mistake is a job for the [backups](operations.md#backup-and-restore).

## What it protects

| Protects | Does not protect |
|---|---|
| A `delete_by_query`, an `update_by_query` or a bulk write that went wrong | A dead disk: the snapshots and the WAL are on the same disk as the data |
| A `truncate`, a `drop`, or a `rename -replace` | A mistake older than the window |
| A bad migration or a bad deploy that wrote for an hour | Direct collections: their history is the app files, which ndb never changes |

## The window

`pitr: 1d` is the default for every engine collection. Change it in `ndb.yml`:

```yaml
pitr: 1d            # every engine collection: off, 6h, 1d... at most 7d

collections:
  sessions:
    pitr: off       # this one has none
  calls:
    pitr: 2d
```

A partition uses the window of its logical collection. `ndb pitr` lists every window and the oldest instant it reaches:

```text
collection         window  restorable from        snapshots  disk
calls              1d      2026-11-11 14:30:12    2          18 MiB
calls_old (dropped) 1d     2026-11-11 14:30:12    1          240 MiB
```

## How it works

- **A snapshot a day.** Each engine collection with a window gets one snapshot when it has none, and a new one when the newest is a window old (at most a day) and there were writes since. A snapshot is the segment list of a checkpoint, hard linked (segments never change), and a copy of `meta.bolt`. It takes milliseconds and copies no data.
- **The WAL.** The WAL files from the oldest snapshot on are kept, even with `wal_retention`.
- **Restore** builds the new collection from the newest snapshot at or before the instant: it links its segments, copies its `meta.bolt` and the WAL records up to the instant, then opens it. The normal crash recovery replays those records. The source is not touched.
- **Truncate, drop and rename -replace** move the old dir to `<data_dir>/trash/` and keep it there for the window, so the collection can be restored after them too. A partitioned collection restores its partitions.

## Cost

- **RAM:** none.
- **Disk:** the writes of the window (the WAL, which ndb keeps by default anyway), plus the segments that compaction replaced during the window: a snapshot keeps the old files alive until it goes. A truncated or dropped collection keeps its disk for the window. `ndb size` has a `pitr` column, and `GET /{coll}/_pitr` reports `bytes`.
- **The number of collections does not matter:** an idle collection or an old partition gets no new snapshot and costs nothing.
- **Blobs:** a blob that no document uses any more is kept for the longest window, since a restore may bring its document back.

## API

| | HTTP | CLI | Go | Ruby | TypeScript |
|---|---|---|---|---|---|
| Restore | `POST /{coll}/_restore {"until","into"}` | `ndb restore <coll> -until T -into NAME` | `calls.Restore(ctx, into, until)` | `client.restore(coll, at:, into:)` | `calls.restore({ until, into })` |
| Window | `GET /{coll}/_pitr`, `GET /_pitr` | `ndb pitr [coll]` | `calls.PITR(ctx)` | `client.pitr(coll)` | `calls.pitr()` |
| Rename | `POST /{coll}/_rename {"to","replace"}` | `ndb rename <coll> <to> [-replace]` | `calls.Rename(ctx, to, replace)` | `client.rename(coll, to, replace:)` | `calls.rename(to, { replace })` |

The console has a **PITR** tab with the windows, a restore form and a rename form.

- `until` is RFC 3339. A dropped collection is restored by its name, while it is in the window (`POST /_restore {"collection","until","into"}` does the same with the name in the body).
- **Errors:** `400 out_of_window` (the message gives the start of the window), `400 pitr_off`, `409 collection_exists` (`into` exists), `404 unknown_collection`.
- **Scope:** a scoped token needs `admin` on the collection and on `into` (and on `to` for a rename).

## Rename

`POST /{coll}/_rename {"to":"calls","replace":true}` gives the collection a new name: its documents, indexes and watch positions move with it, and a partitioned collection renames its partitions. Without `replace`, an existing `to` is `409 collection_exists`. With it, the current `to` goes to the trash, where it stays restorable for its window, so a restore can be undone.

- Calls in flight finish first; new calls wait. A rename is crash safe: the next start finishes one that a crash stopped.
- The watch consumers of a replaced collection lose their positions and start over, as on a new collection. A collection declared in `ndb.yml` comes back, empty, under its old name at the next start; rename it there too.
