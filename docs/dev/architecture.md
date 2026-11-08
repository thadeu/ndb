# Architecture

One Go process, `ndb`. It listens on a unix socket (and optional TCP), answers HTTP/JSON, and owns one data directory.

![How data moves through ndb: direct mode (your app appends to its own NDJSON file, the tailer reads it), engine mode (an SDK sends writes over a unix socket or TCP, the writer commits them to the WAL with fsync), both feed memory and indexes, which seal into Parquet segments; find, aggregate and watch read memory and segments over HTTP](img/flow.svg)

- **Direct mode:** your app appends lines to its own `.ndjson` file. The tailer picks them up (fsnotify plus a 5 s poll). ndb never writes these files.
- **Engine mode:** an SDK sends `insert`, `update` or `delete` over the unix socket or TCP. The writer group-commits them to the WAL with `fsync` before it answers.
- Both feed the in-memory table and its indexes. The sealer moves old rows into Parquet segments by size or age. One scheduler runs the seals and compactions of every collection when they are due, so an idle collection costs no CPU, and a collection not used for `idle_close` closes until the next call ([storage.md](storage.md#segments-and-compaction)).
- `find`, `aggregate` and `watch` read memory and segments over HTTP. The newest version of each id wins.

## Guarantees

- One daemon owns a data directory. A second process gets a lock error, so two daemons never corrupt the same files.
- Your NDJSON is the truth (the [WAL](wal.md) in engine mode, your own files in direct mode). Segments, indexes and checkpoints are derived: delete them and ndb builds them again.
- A query sees the data as of its start. Writes that happen while it runs do not change its answer.
- A write in engine mode is answered only after it is on disk (`durability: strict`), and a crash never leaves half a write.
- The SDKs and `curl` give the same answer, because the daemon owns the meaning of every operator.
- `/_health` answers `503 recovering` until the data directory is open, so a load balancer waits for you.
- Version 1 is one node: no replication, no clustering, no joins, no SQL.
