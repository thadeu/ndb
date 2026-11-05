# M12 — Compaction, retention, blobs, metrics

Status: not started · Depends on: M05 (blobs API needs M07) · Plan: §7.5, §8.5, phase 9

## Goal

ndb runs for months on one server: disk use stays under control, large
values live in the blob store, and the operator can see what happens.

## Epics

### E12.1 Compaction

- [ ] Tests first: results equal before and after; crash during compaction; open snapshot cursors keep old segments
- [ ] Merge small segments; drop old versions and tombstones
- [ ] Atomic swap in the checkpoint; old segments removed after the last snapshot that uses them

### E12.2 Retention

- [ ] Tests first: data older than `retention` is gone from queries; source NDJSON is never deleted
- [ ] Drop derived data by the `time` field (whole segments when possible)

### E12.3 Blob store

- [ ] Tests first: crash between blob write and WAL write leaves no broken reference; same content stored once; `Range` reads
- [ ] `blobs/sha256/..` content-addressed, atomic write
- [ ] `PUT /v1/blobs`, `GET /v1/blobs/{ref}` (streamed, `Range`)
- [ ] Auto-externalize in engine mode (`blob_threshold`)
- [ ] GC: compaction marks referenced blobs; unreferenced blobs older than 24 h are deleted
- [ ] Client support: Go `ndb.Blob` (`io.Reader`), Ruby `attribute :x, :blob` (M10), TS `Blob` stream (M11)

### E12.4 Metrics

- [ ] Prometheus: write rate, commit latency, fsync latency, memtable size, segments, query latency per type, tailer lag, quarantine count, index build progress, blob store size

### E12.5 Backup

- [ ] `ndb backup <dest>`: copies sealed WAL files, segments and blobs (immutable files)
- [ ] Restore test: copy back, start, rebuild, same results

## Exit criteria

- [ ] Compaction and blob crash tests pass
- [ ] A 10 MB value round-trips through the Go client blob API
- [ ] Disk use goes down after compaction on a dataset with many updates
- [ ] Backup + restore test passes

## Out of scope

- Remote backup targets (S3) built in. Use an external copy tool in v1.
