# M18 — Lookup

Status: in progress (plan 2026-11-08) · Depends on: M15 (API 1.0) ·
Replaces `backlog/relationships.md`

## Goal

Read related documents of other collections in one call, inside the daemon:
`lookup` in `_search` (the `includes` of Rails) and `$lookup` at the end of an
`_aggregate` pipeline (MongoDB names accepted). Storage, WAL, segments,
indexes and the planner do not change: lookup composes the reads that exist.

```json
POST /orders/_search
{
  "query": { "status": "paid" },
  "limit": 20,
  "lookup": {
    "customer": { "belongs_to": "customers", "fk": "customer_id" },
    "items":    { "has_many": "order_items", "fk": "order_id", "sort": { "position": 1 }, "limit": 50,
                  "lookup": { "product": { "belongs_to": "products", "fk": "sku" } } },
    "invoice":  { "has_one": "invoices", "foreign_key": "order_id", "fields": ["number", "total"] }
  }
}
```

## Decisions (2026-11-08)

1. The body key is `lookup` (the `$lookup` stage in aggregate; `includes` in
   Rails, `Lookup` in Go, `lookup` in TS).
2. Rails names: `belongs_to` (the fk is here: `here.fk == there.pk`),
   `has_many` and `has_one` (the fk is there: `there.fk == here.pk`);
   `foreign_key`/`fk`, `primary_key`/`pk` (default `_id`). `query`, `sort`,
   `limit`, `fields` filter the other side of `has_many`/`has_one`.
3. Output like Rails: `belongs_to`/`has_one` give an object or `null`;
   `has_many` an array. A fk that is an array gives an array (MongoDB).
4. Two levels at most (`lookup` inside a relation).
5. `$lookup` in aggregate only after the last stage (it enriches the output
   rows), with `from`, `localField`, `foreignField`, `as` (an array, like
   MongoDB) or the names above.

## Rules

- **Index:** the side that is searched must be indexed: `has_many`/`has_one`
  need an index (or `_id`) on `fk` of the other collection; `belongs_to` needs
  `pk` to be `_id` or indexed. Else `400 lookup_unindexed`.
- **Cost:** `belongs_to` is one `$in` query per relation and page;
  `has_many`/`has_one` is one query per parent (with its own limit), 4 at a
  time.
- **Limits:** `has_many` limit default 100, at most 1000; at most 10 000
  documents attached per answer, else `422 lookup_too_large`.
- **fields:** a fk that `fields` leaves out is read and removed again.
- **Scope:** read on every collection of the lookup, else `403 forbidden`.
- **Consistency:** each collection reads its own snapshot; no snapshot across
  collections (as MongoDB without a transaction).
- **Name clash:** the lookup name replaces a field of that name.
- **Errors:** `400 lookup_invalid` (bad spec), `400 lookup_unindexed`,
  `404 unknown_collection` (the other collection), `422 lookup_too_large`.

## Epics

### E18.1 Lookup in `_search`

- [ ] `internal/lookup`: parse and check the spec (Rails names, MongoDB names,
      aliases, defaults, two levels)
- [ ] engine: attach to a page (belongs_to batched, has_many per parent,
      fields, limits, index check), on plain, partitioned and pattern sources
      and targets
- [ ] server: the key on `_search` (plain, partitioned, pattern), scope check

### E18.2 `$lookup` in `_aggregate`

- [ ] trailing `$lookup` stages, MongoDB and Rails names

### E18.3 SDKs

- [ ] Go `Lookup`, TS `lookup`, Ruby `lookup`

### E18.4 Rails

- [ ] `belongs_to`/`has_many`/`has_one` on `Ndb::Record`, `includes(...)`

## Exit criteria

- Orders with customer and items (with product) in one call, from every SDK.
- No change in `make bench`.
- Docs: query.md, aggregate.md, the routes, the errors; CHANGELOG.
