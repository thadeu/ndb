# M04 — Columnar segments

Status: done (light verification) · Depends on: M03 · Plan: §7.3, §7.4, §8.3, §8.4, phase 4

## Goal

The memtable is sealed into immutable Parquet segments in the background.
Queries return the same results before and after sealing. Derived data
can be rebuilt from the NDJSON.

## Epics

### E4.1 Schema inference

- [ ] Tests first (fuzz with `ndbgen` mixed types): flatten to depth 4; arrays of scalars → list; arrays of objects → JSON; type conflict → dominant type + `_rest`; large strings (> 4 KiB) not a column; missing field = null
- [ ] Schema stored per segment in `meta.bolt`
- [ ] `_raw` column with the full row (decision pending: open question 1)

### E4.2 Segment interface and Parquet

- [ ] `SegmentWriter` / `SegmentReader` interfaces (so the format can change later)
- [ ] `parquet-go` implementation: zstd, dictionary encoding, row groups
- [ ] Zone maps (min/max per row group), null counts
- [ ] Bloom filters for indexed columns
- [ ] Source position stored for each row

### E4.3 Sealer

- [ ] Tests first: crash during sealing leaves the old state; queries during sealing see each row once
- [ ] Trigger by memtable size and by age
- [ ] Atomic write: tmp + fsync + rename + fsync dir
- [ ] One bbolt transaction: new segment in the checkpoint + index locations moved from WAL to segment
- [ ] Release the sealed rows from the memtable

### E4.4 Recovery and rebuild

- [ ] Tests first: delete `segments/` and `meta.bolt`; restart rebuilds the same results
- [ ] Remove orphan `tmp` files and segments not in the checkpoint
- [ ] Snapshot now = segment list + memtable position

## Exit criteria

- [ ] For the same rows, query results from segments equal results from the memtable (all `spec/cases/`)
- [ ] DuckDB CLI reads every segment file
- [ ] Rebuild test passes on 1 GB of generated data
- [ ] Crash harness during sealing: no lost or duplicate rows

## Out of scope

- Pruning, column-only reads, parallel scan (M05). Compaction (M12).
