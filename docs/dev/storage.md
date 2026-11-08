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
      segments/                 Parquet segments and their key files (derived)
        000007.parquet          the rows
        000007.ids              id → row (sorted, with a bloom filter)
        000007.i.<hex field>    an index field: key → row
        000007.u.<hex field>    a unique field: value → id
      meta.bolt                 checkpoint, segment list, live bits, file cursors (derived)
      quarantine/<name>.ndjson  lines that could not be applied (direct mode)
    <name>~new/                 a truncate in progress (finished or removed at start)
  trash/                        dirs of truncated and dropped collections, deleted in the background
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

A direct collection is disk first, like an engine one: its rows are sealed to segments, and each checkpoint saves the cursor of every file with them. After a restart the tailer goes on from the cursors of the last checkpoint, not from offset 0; the few lines after them are read again, which changes nothing because the last line of an `_id` wins. While the memtable is full the tailer waits for a seal, so a large backlog does not grow the memory.

## Segments and compaction

The recent rows live in memory (the memtable). They are sealed to an immutable Parquet segment when the memtable of a collection reaches its share of `write_buffer` (32 MiB for the whole daemon, the largest memtable goes first) or 10 minutes since the last seal. Segments hold per-row-group min/max (zone maps), so queries skip what cannot match. DuckDB, Spark and pandas can read them. When 4 or more small segments are adjacent, compaction merges them (at most 512k rows at a time), and drops old versions, tombstones, expired rows and rows past `retention`. A segment whose rows are mostly replaced is rewritten alone. A seal can run while a compaction builds its segment.

The seals, compactions, WAL truncation and TTL sweeps of every collection run from one scheduler, not from a timer per collection. Each job has a deadline: a write gives the seal one (when the memtable turns 10 minutes old, or now when it passed its size), a seal gives the compaction one, and a job with nothing left to do gives none. A collection that is not written costs no CPU: 1 000 idle collections use 0 ms of CPU in 10 s (`ndb bench -idle`). Work that comes with time and not with writes (segments past `retention`, WAL files a reader still holds) is checked once a minute, only for the collections that have it.

## Memory

ndb keeps data on disk; memory has a fixed size, whatever the number of documents, collections or partitions:

| In memory | Size |
|---|---|
| memtables (rows not sealed yet) | `write_buffer`, 32 MiB; writes wait while a memtable is over twice its seal size |
| per sealed row: a live bit, a share of the bloom filter and of the sparse index of its ids | about 20–40 bytes |
| a decoded row group per segment being read, the plan of one compaction | a few MB |

A sealed document is not in a map: a lookup by `_id` asks the memtable, then the key files of the segments from the newest. One bitmap per segment marks the rows that are still the current version of their document; a scan reads only those, and a `_count` with no conditions adds whole row groups from the bits. The bits are saved with each checkpoint, and the WAL tail clears them again after a crash.

`memory_limit` (default 384 MiB) is the Go heap soft limit: with it the daemon stays near 512 MB of RSS. The OS page cache holds the hot parts of the segments; it is not the daemon's memory, and the kernel takes it back under pressure.

A data dir of a version before 1.1 has segments without key files: the first start writes them once (it reads every row id, so it needs memory for one map of the ids).

## Rebuild and recovery

At start the daemon takes the lock, reads the checkpoint, replays the [WAL](wal.md#recovery) after it, continues each source file from its cursor, and removes temporary files and segments the checkpoint does not list. Segments, indexes and checkpoints can be deleted: ndb builds them again from the NDJSON.

Derived files are written as `tmp`, fsync, `rename`, fsync of the directory. All file access goes through an `fs` interface so tests can inject faults.

## Limits

- `max_line_size` per collection: default 1 MiB, maximum 16 MiB. Engine mode: `line_too_large`. Direct mode: the line goes to quarantine (only its first 4 KiB is copied).
- Large binary values: use the blob store. The document keeps `{"$blob":"sha256:...","size":N,"type":"..."}`.
- Nested objects become columns up to depth 4. A value of another type than the column keeps the exact document in `_raw`; `find` always returns the exact stored document.
