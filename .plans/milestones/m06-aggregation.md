# M06 — Aggregation

Status: not started · Depends on: M05 · Plan: §10.2, §11.3, phase 6

## Goal

`aggregate` with the v1 pipeline, fast enough for charts and reports.

## Epics

### E6.1 Pipeline parser

- [ ] Tests first: each stage, each accumulator, invalid pipelines
- [ ] Stages: `$match`, `$group`, `$sort`, `$limit`, `$skip`, `$project`
- [ ] Accumulators: `$count`, `$sum`, `$avg`, `$min`, `$max`, `$first`, `$last`
- [ ] Expressions: field paths, composite `_id`, `$dateTrunc` (minute, hour, day, week, month; time zone)

### E6.2 Row engine and column engine

- [ ] Tests first: both engines give the same result on all aggregate cases
- [ ] Row engine for the memtable
- [ ] Column engine for segments: `$match` + `$group` on vectors, no JSON parse

### E6.3 Parallel partial groups

- [ ] Tests first: merge of partial `$avg` (sum + count), `$first`/`$last` with order
- [ ] One partial group table per segment, then merge
- [ ] Group table over `memory_limit` → error `group_limit_exceeded` (spill later)

### E6.4 Pagination on aggregate

- [ ] Offset and cursor pagination on the final stage output

## Exit criteria

- [ ] Golden results vs DuckDB for all aggregate cases on 10 GB
- [ ] Benchmark: `$group` by 2 fields with `$dateTrunc`, 10 GB raw, < 2 s (target from §13)
- [ ] Aggregate cases added to `spec/cases/`

## Out of scope

- `$lookup` (see [backlog/relationships](../backlog/relationships.md)), `$unwind`, `$facet`, spill to disk.
