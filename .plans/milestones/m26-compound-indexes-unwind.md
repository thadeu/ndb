# M26 — Compound indexes and `$unwind`

Status: not started · Depends on: nothing · Docs: `docs/dev/query.md`, `docs/dev/aggregate.md`

Two gaps of the query side that a SaaS meets first. They do not depend on
each other: Part A and Part B can ship in separate releases.

## Part A — Compound indexes

### Goal

One index on more than one field, so these are index reads and not scans:

```json
{"query": {"account_id": "acc_1", "created_at": {"$gte": "now-7d"}}, "sort": {"created_at": -1}, "limit": 20}
```

And a unique key of more than one field (an email is unique per account):

```json
{"indexes": ["account_id,created_at"], "unique": ["account_id,email"]}
```

Today an index is one field. The query above uses the index of
`account_id` and then reads and sorts every document of the account.

### Design

**Declaration.** An entry of `indexes` or `unique` with commas is a
compound index: `"account_id,created_at"`. The order of the fields is the
order of the key. Dot paths are allowed (`"owner.id,created_at"`). 2 to 4
fields. The same string is the name of the index everywhere: the catalog,
`PUT` and `DELETE /{coll}/_indexes/account_id,created_at`, `_stats`. A
field name has no comma today, so no existing definition changes meaning.
(A comma list in the path is already how many collections are named.)

**Key.** `index.Encode` is order-preserving and prefix-free. The key of a
compound index is the encodings of its fields, one after the other, then
the id. So the bytes sort as the tuple sorts, and the memtable B-tree and
the segment index files (`.ids`) hold it with no new structure. A field
that is missing in a document is encoded as missing, as in a single-field
index (to confirm in the code when E26.1 starts).

**Planner.** A compound index `(a, b, c)` serves a query when:

- its first fields have equality (`a = x`, or `a = x and b = y`);
- then at most one field has a range or an `$in` (`b >= t`);
- and the `sort` is on the next field of the key (`a = x`, sort by `b`):
  the index gives the rows in order, so a top-N stops after N entries. A
  descending sort walks the index backwards (`Index` gets `Descend`).

The planner picks the index that binds the most fields; `explain` names
it. A query on `b` alone does not use `(a, b)` (no skip scan). The single
index of `a` is not needed when `(a, b)` exists: the prefix serves it.

**Unique.** A compound unique key refuses a second live document with the
same tuple (`409 unique_violation`, the message names the fields). A
document that misses one of the fields is not in the key, as a document
that misses the field of a single unique key.

**Lookups** can use a compound index whose first field is the foreign key
(`has_many` with a sort: `fk = id`, sort by the next field).

**Out of scope:** more than 4 fields, an index direction per field, array
fields as many keys (an array is one value, as today), partial and text
indexes, an index on an expression.

### Decisions to confirm

1. The declaration is a string with commas (`"a,b"`), not a JSON array
   (`["a","b"]`). It keeps `indexes` an array of strings in every answer
   and SDK; the HTTP API only adds.
2. A document that misses a field of a compound **unique** key is not
   checked (two documents with no `email` are allowed), as today for one
   field.
3. Limit of 4 fields.

### Epics

#### E26.1 Daemon: the index
- [ ] tests first: order of the tuple across types; a prefix read; equality + range; top-N by the next field, both directions, stops early (`explain`); the same answers as a scan on random data (property test)
- [ ] `catalog`: parse and validate the name (2 to 4 fields, no repeat, no empty); merge rules; `PUT` and `DELETE _indexes/{a,b}`
- [ ] `index`: the tuple key; `Descend`
- [ ] `collection`: maintain it in the memtable and in the segment index files (seal, compaction, rebuild); build it on a collection with data (`building` then `ready`)
- [ ] planner: choose it for `_search`, `_count`, `_update_by_query`, `_delete_by_query`, `$match` and lookups; `explain`

#### E26.2 Daemon: compound unique
- [ ] tests first: the violation on insert, bulk, update, upsert and `_tx`; added later on data with a duplicate tuple (the `PUT` is refused); recovery rebuilds it; a `kill -9` loop
- [ ] `state`: the owner map by tuple; the on-disk unique file

#### E26.3 SDKs and docs
- [ ] Go (a collection option, the struct tags stay for one field), Ruby (`indexes: ["a,b"]` already passes), Rails (`index [:account_id, :created_at]`, `unique:`; `has_many` with an order uses it), TypeScript (schema option)
- [ ] `spec/cases`: queries whose answer must not change with the index
- [ ] `docs/dev/query.md` (which queries an index serves), `http/collections.md`, `configuration.md`, client pages, `llms-agents.md`, CHANGELOG
- [ ] `make bench`: the 20 newest documents of one account among 2 million (budget to set after the first measure; today it reads the whole account)

### Exit criteria

- The query of the Goal reads 20 index entries (`explain`), at any size of the account.
- `make bench`: every check PASS; the write budgets unchanged with one compound index on the loaded collection (one more key per write).

## Part B — `$unwind`

### Goal

Group by the elements of an array. Today a document with
`items: [{sku, qty}, ...]` cannot answer "quantity sold per sku":

```json
{"pipeline": [
  {"$match": {"status": "paid"}},
  {"$unwind": "$items"},
  {"$group": {"_id": "$items.sku", "qty": {"$sum": "$items.qty"}, "orders": {"$distinct": "$_id"}}},
  {"$sort": {"qty": -1}},
  {"$limit": 10}
]}
```

### Design

- `{"$unwind": "$path"}` turns one document into one row per element of
  the array at `path`; in each row, `path` holds that element. As in
  MongoDB: a document where `path` is missing, `null` or an empty array
  gives no row; a value that is not an array gives one row with it.
- The long form adds two options of MongoDB:
  `{"$unwind": {"path": "$items", "preserveNullAndEmptyArrays": true, "includeArrayIndex": "i"}}`.
- **Where:** before `$group`, after the `$match` that uses indexes. One
  `$unwind` per pipeline. A `$match` after it and before `$group` filters
  the element rows (`{"items.sku": "a"}`); it runs on each row, with no
  index.
- **How:** the scan reads the documents as today (the array is read from
  the document, not from a column). The group table gets one row per
  element. `$first` and `$last` follow the order of the documents, then
  of the elements. The memory limit of the group table
  (`group_limit_exceeded`) applies as today.
- `$unwind` needs `$group` in this milestone: a pipeline with no `$group`
  runs as a search, and one document as many rows does not fit its paging.

**Out of scope:** two `$unwind` in one pipeline, `$unwind` after `$group`,
`$unwind` with no `$group`, `$facet`, `$bucket`, window functions.

### Decisions to confirm

1. One `$unwind` per pipeline, and only with `$group`.
2. The MongoDB rules for missing, `null`, empty and non-array values.

### Epics

#### E26.4 Daemon
- [ ] tests first: the rows of each rule above; `$match` before and after; `$first`/`$last` order; `$distinct` of `_id` (documents, not elements); the limit; errors (`invalid_pipeline`: two unwinds, after `$group`, no `$group`, a path without `$`)
- [ ] `agg`: parse; the expansion between the scan and the table; `NeedsPos`
- [ ] patterns and partitioned collections: the same answer as one collection
- [ ] `spec/cases/aggregate`: the shared cases

#### E26.5 SDKs and docs
- [ ] the pipeline builders of Go, Ruby, Rails and TypeScript
- [ ] `docs/dev/aggregate.md` (pipeline shape, stages), `http/queries.md`, client pages, `llms-agents.md`, CHANGELOG
- [ ] `make bench`: an aggregation with `$unwind` on the 2 million documents (budget to set after the first measure)

### Exit criteria

- The pipeline of the Goal gives the same rows as a script that expands the arrays by hand, on random data.
- `make bench`: every check PASS; the aggregations with no `$unwind` are not slower.
