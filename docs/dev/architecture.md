# Architecture

One Go process, `ndb`. It listens on a unix socket (and optional TCP), answers HTTP/JSON, and owns one data directory.

```
 apps (Go / Ruby / TS)                       ndb daemon
 ─────────────────────                       ───────────────────────────────
 append NDJSON (direct mode) ──────────────▶ tailer  (fsnotify + 5 s poll)
                                                   │
 insert / update / delete (engine mode) ──▶ writer (group commit, WAL)
                                                   ▼
                                             in-memory table + indexes
                                                   │ seal (size / age)
                                                   ▼
                                             Parquet segments (+ zone maps)
                                                   ▲
 find / aggregate / watch  ◀── HTTP ───────────────┘
```

## Pieces

| Package | Job |
|---|---|
| `cmd/ndb` | flags, listeners, graceful stop, `ndb backup` |
| `internal/server` | HTTP routes, error mapping, metrics, `watch` stream |
| `internal/engine` | opens the data dir, owns the collections, blobs and backup |
| `internal/catalog` | collection definitions in `catalog.bolt`; merge of code, YAML and implicit declarations |
| `internal/config` | loads the optional `ndb.yml` |
| `internal/collection` | one collection: writes, reads, indexes, sealing, compaction, TTL |
| `internal/writer`, `internal/wal` | engine-mode WAL with CRC and group commit |
| `internal/tailer` | direct mode: follows the source files, keeps a cursor per file, quarantines bad lines |
| `internal/query`, `internal/agg` | parse and run `where`, `sort` and aggregate pipelines |
| `internal/segment` | Parquet segments |
| `internal/index` | `_id` and secondary indexes |
| `internal/feed`, `internal/cursors` | `watch` change feed and consumer positions |
| `internal/blob` | content-addressed blob store |
| `internal/recovery`, `internal/lock` | start-up recovery, one daemon per data dir (`flock`) |

## Rules

- One daemon owns a data dir. A second process gets a lock error.
- The NDJSON (WAL or source files) is the truth. Segments, indexes and checkpoints are derived and can be rebuilt.
- Snapshot reads: a query sees the data as of its start. Writes after that are not visible to it.
- The SDKs build JSON only. Every query meaning (operators, types, order, last version wins) lives in the daemon.
- The daemon listens first and recovers after, so `/health` answers `503 recovering` until the data dir is open.
- Version 1 is one node: no replication, no clustering, no joins, no SQL.

The full design is in [.plans/ndb-v1.md](../../.plans/ndb-v1.md).
