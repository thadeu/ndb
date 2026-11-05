# M05 — Planner + column scan

Status: done (light verification; time-field pruning, index cursor fast path and mmap open) · Depends on: M04 · Plan: §11.2, §11.4, phase 5

## Goal

Queries with no index are fast: segment and row-group pruning, reading only
the needed columns, filters on column vectors, parallel scan.

## Epics

### E5.1 Planner

- [ ] Tests first: the plan chosen for id, indexed equality, indexed range, `$or`, no index
- [ ] Rule-based planner: id → index → scan
- [ ] `"explain": true` returns the plan and counters (segments read/skipped, rows read)

### E5.2 Pruning

- [ ] Tests first: zone maps skip segments and row groups; bloom filters skip on equality
- [ ] Pruning on the `time` field and on any column with stats

### E5.3 Column scan

- [ ] Tests first: results equal the row-based evaluator on all `spec/cases/`
- [ ] Read only the columns in `where`, `sort`, `fields`
- [ ] Filter on column vectors; `_rest` evaluated for rows with type conflicts
- [ ] Fetch `_raw` only for the rows that match

### E5.4 Parallel scan and merge

- [ ] Tests first: last version wins and tombstones across segments + memtable; sort + limit with a top-k heap
- [ ] One worker per segment (bounded pool)
- [ ] Merge partial results with the memtable

### E5.5 Memory

- [ ] `GOMEMLIMIT` from `memory_limit`
- [ ] mmap for segment reads where possible
- [ ] LRU of decoded documents for `find` by id

## Exit criteria

- [ ] Golden results vs DuckDB on 10 GB of generated data
- [ ] Benchmark: `find` on a field with no index, 10 GB of raw NDJSON, < 2 s (target from §13)
- [ ] `explain` tests pass

## Out of scope

- Cost-based planning. Spill to disk.
