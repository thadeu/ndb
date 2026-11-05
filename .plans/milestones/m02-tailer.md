# M02 — Tailer (direct mode)

Status: done (light verification) · Depends on: M01 · Plan: §6.1, §9, phase 2

## Goal

ndb follows NDJSON folders that the apps write directly, and keeps the
memtable current, with no loss and no duplicate across restarts, rotation
and truncation.

## Epics

### E2.1 Discovery

- [ ] Tests first: new file, new folder, file created before start, glob with `**`
- [ ] Glob at start; fsnotify events; poll every 5 s as a fallback

### E2.2 File cursors

- [ ] Tests first: restart in the middle of a file; cursor saved after each batch
- [ ] Cursor = inode + offset + fingerprint of the first 1 KiB, stored in `meta.bolt`
- [ ] Read only up to the last `\n`; keep the rest for the next read

### E2.3 Rotation and truncation

- [ ] Tests first: rename + new file at the same path; copy-truncate; truncate to 0; file deleted
- [ ] New inode at the same path = new file; finish the old inode first
- [ ] Size < offset → warning + read again from 0

### E2.4 Line semantics

- [ ] Tests first: last version wins by id; `_deleted: true`; `{"_batch":[...]}` all or nothing; collection with no id (`file:offset`)
- [ ] Apply to the memtable through the same path as engine-mode writes

### E2.5 Quarantine

- [ ] Tests first: bad JSON, line over `max_line_size`, interleaved lines from two writers
- [ ] `quarantine/<collection>.ndjson` with file, offset, reason, first 4 KiB
- [ ] Counter per collection (exposed later in `stats` and metrics)

### E2.6 Backpressure

- [ ] Tests first: memtable full → the tailer waits, no line is dropped
- [ ] Memtable size limit from config

## Exit criteria

- [ ] All scenario tests above pass, including in a Docker bind mount (poll fallback)
- [ ] Crash harness: kill ndb during ingestion; after restart, row count equals line count, no duplicates
- [ ] Benchmark: ≥ 100 MB/s of NDJSON ingestion (target from §13)
- [ ] A new line is visible in the memtable in < 100 ms

## Out of scope

- Writes through ndb into direct-mode collections (they are read-only for ndb).
