# M12 — Compaction, retention, TTL, blobs, metrics

Status: not started · Depends on: M05 (blobs API needs M07) · Plan: §7.5, §8.5, §11.5, phase 9

## Goal

ndb runs for months on one server: disk use stays under control, documents
can expire like Redis keys, large values live in the blob store, and the
operator can see what happens.

## Epics

### E12.1 Compaction

- [ ] Tests first: results equal before and after; crash during compaction; open snapshot cursors keep old segments
- [ ] Merge small segments; drop old versions and tombstones
- [ ] Atomic swap in the checkpoint; old segments removed after the last snapshot that uses them

### E12.2 Retention

- [ ] Tests first: data older than `retention` is gone from queries; source NDJSON is never deleted
- [ ] Drop derived data by the `time` field (whole segments when possible)

### E12.6 Document TTL (plan §11.5)

- [ ] Tests first: an expired doc is hidden from find/count/aggregate at once (no job ran); an update that moves `expires_at` forward keeps it; a doc without `expires_field` never expires; `ttl` needs a `time` field (catalog validation)
- [ ] Catalog: `ttl` and `expires_field` (one of the two), merge rules like `time` (conflict on change)
- [ ] Read filter against the snapshot time; range skip when `expires_field` is indexed
- [ ] Engine-mode sweeper: tombstones for expired docs in small batches; `watch` shows `"_expired": true`; crash during a sweep is safe (tombstones are idempotent)
- [ ] Direct mode: hide only, never touch the apps' files; compaction drops expired rows from derived data
- [ ] Model layers: Rails `ttl` / `expires_field`, Go tag `expires`, TS `expires`

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
- [ ] TTL: expired docs are hidden at once and, in engine mode, swept to tombstones that `watch` delivers
- [ ] Backup + restore test passes

## Out of scope

- Remote backup targets (S3) built in. Use an external copy tool in v1.
