# M08 — Go client

Status: not started · Depends on: M07 · Plan: §12.2

## Goal

`ndb.Collection[T]`: an idiomatic Go API with struct tags and generics.
No query semantics in the client: it only builds JSON.

## Epics

### E8.1 Transport

- [ ] Tests first: against a real daemon started by the test
- [ ] HTTP client on a unix socket, keep-alive, context on every call
- [ ] NDJSON stream decoder; typed errors (`ErrVersionConflict`, `ErrCatalogConflict`, `ErrCursorMismatch`, ...)

### E8.2 Collections

- [ ] Tests first: tag parsing (`id`, `index`, `time`), `ensure_collection` on first use
- [ ] `ndb.Collection[T](db, name)`
- [ ] `Insert`, `Update` (with `IfVersion`), `Delete`, `Find(id)`

### E8.3 Query builder

- [ ] `Where(ndb.Eq(...), ndb.Gte(...), ndb.In(...), ndb.Or(...))`, `Sort`, `Limit`, `Skip`, `Fields`
- [ ] `All(ctx)`, `First(ctx)`, `Count(ctx)`, `Iter(ctx)` (range-over-func iterator)
- [ ] Pagination: `Page(n, size)`, `After(token)` + `NextCursor()`
- [ ] `Aggregate(ctx, stages...)`

### E8.4 Shared suite

- [ ] Runner that executes all `spec/cases/` through the client

## Exit criteria

- [ ] All `spec/cases/` pass through the Go client
- [ ] Example program in `sdk/go/examples/`

## Out of scope

- `Watch` (M09), blobs (M12).
