# M26 — Compound indexes and `$unwind`

Status: in progress (Part A and Part B done; E26.6, the ordered read of an index, not started) · Depends on: nothing · Docs: `docs/dev/query.md`, `docs/dev/aggregate.md`

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
the segment index files hold it with no new structure.

Found in the code when E26.1 started, and built so:

- An index of one field has one key per array element (a filter matches
  when one element matches). A compound index does the same: two array
  fields give every pair, up to 1024 keys per document; over that, the
  document gets one overflow key that every read of the index takes as a
  candidate. So the answer is always the one of a scan.
- A missing field is encoded as missing (it sorts first), so a read by the
  first fields finds the document. A document with no first field has no
  key.
- A range of two sides (`b > 1 and b <= 2`) is two reads of the index and
  the documents in both: with an array, the two conditions can match on
  two different elements.

**Planner.** A compound index `(a, b, c)` serves a query when its first
fields have equality or `$in` (up to 64 combinations), then at most one
field has a range. The planner picks the index that binds the most fields
(`X-Ndb-Plan: index:a,b`). With equality on the first field only, it is
used when that field has no index of its own. A query on `b` alone does
not use `(a, b)` (no skip scan). The documents the index gives are tested
with the whole filter.

**Not built: rows in the order of the index.** The plan was a top-N that
stops after N entries (`a = x`, sort by `b`). The indexes of ndb give a set
of candidate rows per segment (a bitset), not an ordered stream, so this
needs a merge of ordered reads over the memtable and every segment, with
the live bits and the array rule above. It is E26.6, on its own. Measured
without it (300 000 documents, 300 accounts, 1000 per account): the 20
newest of one account with no range reads the 1003 documents of the
account, 50 ms; with a range that keeps 150 of them, 18 ms.

**Unique.** A compound unique key refuses a second live document with the
same tuple (`409 unique_violation`, the message names the fields). A
document that misses one of the fields is not in the key, as a document
that misses the field of a single unique key.

**Lookups** can use a compound index whose first field is the foreign key
(`has_many` with a sort: `fk = id`, sort by the next field).

**Out of scope:** more than 4 fields, an index direction per field, array
fields as many keys (an array is one value, as today), partial and text
indexes, an index on an expression.

### Decisions (confirmed)

1. The declaration is a string with commas (`"a,b"`), not a JSON array
   (`["a","b"]`). It keeps `indexes` an array of strings in every answer
   and SDK; the HTTP API only adds.
2. A document that misses a field of a compound **unique** key is not
   checked (two documents with no `email` are allowed), as today for one
   field.
3. Limit of 4 fields.

### Epics

#### E26.1 Daemon: the index (done)
- [x] tests first: order of the tuple across types; arrays, missing fields and the overflow key; plans and documents examined for equality, range, `$in`, prefix; the same answers as a scan for 2400 random queries (memtable, segments, an index built later, after updates and deletes)
- [x] `catalog`: validate the name (2 to 4 fields, no repeat, no empty, no `_id`); `PUT` and `DELETE _indexes/{a,b}`
- [x] `index`: the tuple key, `PrefixRange`, `TupleCmp`
- [x] `collection`: the memtable and the segment index files hold it (seal, compaction, build on a collection with data)
- [x] planner: `byCompound` for `_search`, `_count`, `_update_by_query`, `_delete_by_query`, `$match` and lookups

#### E26.2 Daemon: compound unique (done)
- [x] tests first: the violation on insert, bulk, update and `_tx`; a missing or `null` field is not checked; the tuple is freed by an update and a delete; after a restart; in segments; added later on data with a duplicate tuple (refused)
- [x] `uniqueKey` gives the tuple

#### E26.3 SDKs and docs (done)
- [x] Go `ndb.Index(...)` and `ndb.Unique(...)`, Ruby (arrays or `"a,b"`), Rails `index :a, :b` and `unique: true`, TypeScript `indexes` and `unique` in the schema
- [ ] `spec/cases`: not done (the cases have no way to declare a compound index and wait for it; each layer has its own tests)
- [x] `docs/dev/query.md` (Indexes), `http/collections.md`, `configuration.md`, client pages, `llms-agents.md`, CHANGELOG
- [x] `make bench`: the part `-compound` (60 000 orders of 60 accounts with items): the same reads with an index of the account and then with compound indexes, each with its plan and the documents examined; budget `compound.worst` 66 ms (60 ms, about 3x the measure, plus 10% for a burst)

#### E26.6 Rows in the order of the index (not started)
- [ ] an ordered read of the memtable index and of each segment index file, merged, with the live bits; both directions
- [ ] used when the filter binds the first fields by equality, the sort is on the next field, and no document of the range has an array there
- [ ] `explain`: the entries read; a bench check for the 20 newest of one account

### Exit criteria

- The query of the Goal uses the index for its filter (done). Reading only 20 entries is E26.6.
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

### Decisions (confirmed)

1. One `$unwind` per pipeline, and only with `$group`.
2. The MongoDB rules for missing, `null`, empty and non-array values.

### Epics

#### E26.4 Daemon (done)
- [x] tests first: the rows of each rule; `$match` before and after; `$first`/`$last` order; `$distinct` of `_id`; errors (`invalid_pipeline`: two unwinds, after `$group`, no `$group`, a path without `$`, unknown option); the same rows as an expansion by hand on random orders, in the memtable and in segments
- [x] `agg`: parse, `Expand` between the scan and the table, the element order for `$first` and `$last`
- [x] patterns and partitioned collections: the same answer as one collection
- [ ] `spec/cases/aggregate`: not done (each layer has its own tests)

#### E26.5 SDKs and docs (done)
- [x] Go `UnwindStage`; Rails `unwind(:items)` on a relation; Ruby and TypeScript pass the stage in the pipeline (tested)
- [x] `docs/dev/aggregate.md` (pipeline shape, stages, `$unwind`), `http/queries.md`, Rails page, `llms-agents.md`, CHANGELOG
- [x] `make bench`: two aggregations with `$unwind` in the part `-compound`, under the same budget

Built so: the array is read from the document (a raw fetch for each matched
row of a segment), not from a column. It is correct and simple; a column
read of the array is a later optimization, when a measure asks for it.

### Exit criteria

- The pipeline of the Goal gives the same rows as a script that expands the arrays by hand, on random data.
- `make bench`: every check PASS; the aggregations with no `$unwind` are not slower.
