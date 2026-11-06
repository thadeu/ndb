# Storage

## Layout

```
<data_dir>/
  LOCK                          flock: one daemon per data dir
  catalog.bolt                  collection definitions
  cursors.bolt                  watch consumer positions
  blobs/                        large values, content-addressed (sha256)
  collections/
    <name>/
      000001.ndjson ...         WAL (engine mode), see wal.md
      segments/                 Parquet segments (derived)
      meta.bolt                 checkpoint, segment list, file cursors (derived)
      quarantine/<name>.ndjson  lines that could not be applied (direct mode)
```

Look at it with `ls`; do not edit it by hand while the daemon runs.

## Write modes

**Engine mode.** Only ndb writes, through the [WAL](wal.md). One writer per collection gives serializable writes; an update by filter with `_v` gives optimistic concurrency.

**Direct mode.** The apps append to their own files, ndb only reads them. Rules for the apps:

1. One writer per file (for example `calls-<host>-<pid>.ndjson`).
2. One line per `write()`, ending with `\n`.
3. An update or delete is a new line. The last line for an `_id` wins. `{"_id": "...", "_deleted": true}` removes a document. A line without `_id` is a document that is never updated, and the `/{_id}` routes cannot reach it. `{"_batch":[...]}` is all or nothing.
4. Rotation is fine. Do not truncate a file that is in use.

ndb finds files by the glob, with fsnotify and a 5 s poll (bind mounts and overlays can miss events). It keeps a cursor per file (inode, offset, fingerprint of the first KiB) and reads only up to the last `\n`.

## Segments and compaction

The recent rows live in memory. They are sealed to an immutable Parquet segment when they reach 64 MiB or 10 minutes since the last seal. Segments hold per-row-group min/max (zone maps), so queries skip what cannot match. DuckDB, Spark and pandas can read them. When 4 or more small segments are adjacent, compaction merges them, and drops old versions, tombstones, expired rows and rows past `retention`.

## Rebuild and recovery

At start the daemon takes the lock, reads the checkpoint, replays the [WAL](wal.md#recovery) after it, continues each source file from its cursor, and removes temporary files and segments the checkpoint does not list. Segments, indexes and checkpoints can be deleted: ndb builds them again from the NDJSON.

Derived files are written as `tmp`, fsync, `rename`, fsync of the directory. All file access goes through an `fs` interface so tests can inject faults.

## Limits

- `max_line_size` per collection: default 1 MiB, maximum 16 MiB. Engine mode: `line_too_large`. Direct mode: the line goes to quarantine (only its first 4 KiB is copied).
- Large binary values: use the blob store. The document keeps `{"$blob":"sha256:...","size":N,"type":"..."}`.
- Nested objects become columns up to depth 4. A value of another type than the column keeps the exact document in `_raw`; `find` always returns the exact stored document.
