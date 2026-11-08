# WAL

## What it is

In engine mode, the WAL is a set of plain NDJSON files. There is no binary log and no second copy of your data: the WAL is the source of truth, and segments and indexes are built from it.

One write request is one transaction, and one transaction is one line:

```json
{"_tx":1,"_ts":"2026-11-05T19:38:03.730304Z","_op":"put","_id":"c1","_v":1,"doc":{"_id":"c1","status":"answered","duration":42},"_crc":"47d5d3bb"}
```

| Field | Meaning |
|---|---|
| `_tx` | transaction number. It grows by one per line. |
| `_ts` | commit time (UTC). |
| `_op`, `_id`, `_v`, `doc` | `put` or `del`, the document id, its new version, the document. |
| `ops` | a transaction with more than one change has an `ops` array of these fields instead. |
| `_crc` | CRC32-C of every byte before `,"_crc"`, in hex. |

The line is complete, or it is not there. You can read it with `cat`, `tail -f` or `jq`:

```bash
jq -c 'select(._op == "put") | .doc' data/collections/calls/*.ndjson
```

Read the files; do not edit them while the daemon runs. A hand edit breaks the CRC.

## Group commit and fsync

Each collection has one writer. It takes every request that waits, checks them (unique keys, the filter of an update), appends all lines with one `write()` and calls one `fdatasync`. Many clients share one fsync, so throughput does not fall to one disk flush per request.

When the client gets its answer depends on `durability`:

| Mode | The client gets the answer | After a crash |
|---|---|---|
| `strict` (default) | after `fdatasync` | every answered write is on disk |
| `relaxed` | after `write()` | you can lose the last 200 ms; the WAL is never corrupt |

In `relaxed` mode a background loop calls `fdatasync` every 200 ms. `watch` events wait for that fsync, so a consumer never sees a write that a crash can take back.

If a write or an fsync fails, ndb exits. It does not retry: after a failed fsync, Linux can drop the dirty pages, so the only safe step is to stop and recover from the WAL.

## Files and rotation

```
<data_dir>/collections/<name>/
  000001.ndjson
  000002.ndjson
  ...
```

The files are numbered. ndb appends to the last one. When a line would pass 64 MiB, the writer closes the file and starts the next number. Files that are closed never change again, so they are safe to copy for a backup. See [operations.md](operations.md#backup-and-restore).

On Linux the writer reserves disk blocks ahead of the data (`fallocate`), so an append does not wait for the file system to allocate. The reserve is as big as the data already in the file, between 64 KiB and 4 MiB: a collection that writes little holds little, and one under load gets 4 MiB ahead. The reserve goes back when the file is closed (rotation or shutdown). 300 collections of 10 documents hold 80 KB each.

## Recovery

At start, ndb reads the WAL after the last checkpoint and rebuilds memory and indexes from it.

- **A cut tail is normal.** If the process died in the middle of a write, the last line has no `\n` or a wrong CRC. That write was never answered. ndb cuts the file at that point, in place, and goes on.
- **Damage in an older file is an error.** A bad line in a file that is already closed is real corruption. ndb stops with `corrupt line in a sealed WAL segment` and the file and offset. It never drops that data in silence.
- **Tx gaps are an error.** Transaction numbers must grow by one. A jump means a line or a file is missing, and ndb stops.

Segments, indexes and checkpoints can be deleted: ndb builds them again from the WAL (see [storage.md](storage.md#rebuild-and-recovery)). This is true as long as the WAL files are still there; see the next section.

## Truncation

By default ndb never deletes a WAL file. Set `wal_retention` on an engine-mode collection to delete old ones:

```yaml
collections:
  outbox:
    wal_retention: 7d
```

A file is deleted only when all three rules hold:

1. **Sealed.** The checkpoint is past the file: its rows are already in a Parquet segment, and a restart never reads it.
2. **Nobody needs it.** Every saved `watch` cursor is after the file, and no open `watch` stream is still reading it. A consumer that never acked holds the whole history back.
3. **Old enough.** The file was closed more than `wal_retention` ago.

Files go from the oldest, and ndb stops at the first one that stays, so the files you keep are always in a row. The newest file is never deleted.

With truncation on, the WAL alone no longer rebuilds the whole collection. Keep the segments and `meta.bolt`; a backup has both.

If a `watch` asks for history that was deleted (`from: "beginning"`, or a position older than the first kept file), the answer is `history_truncated` (HTTP 410). The stream never skips the gap in silence. Start from `now`, or rebuild from `find`. A saved position is safe: ndb keeps the file that holds it.

## Direct mode

A direct collection has no WAL of its own. Your files, the ones that match `path`, play that role: your app appends lines, ndb reads them and keeps a cursor for each file. ndb never writes them, so there is no group commit and no `durability`; the app that appends the file owns durability. See [storage.md](storage.md#write-modes) for the rules for your app, and [configuration.md](configuration.md) for `path`.

## Code

- internal/wal/record.go: Encode, Decode, CRC
- internal/writer/writer.go: group commit, fsync, rotation
- internal/recovery/recovery.go: replay, cut tail, ErrCorruptSealed, ErrTxGap
- internal/collection/waltrunc.go: TruncateWAL, deletable
