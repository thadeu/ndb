# Playbook: API, SDKs and docs (the ndb 1.0 pattern)

> Written after ndb 1.0 (2026-11-07). Use it for the other apps: the same
> shape for the HTTP API, the same SDK vocabulary rules, the same docs, the
> same way of working. Adapt names, keep the rules.

## 1. HTTP API shape

The URL reads like Elasticsearch; the body reads like MongoDB.

- **`_` is the system namespace.** A first path segment that starts with `_`
  is the daemon (`/_health`, `/_metrics`, `/_stats`, `/_collections`). A
  resource name never starts with `_`, so system and data never collide.
- **Resource actions start with `_`:** `/{resource}/_search`, `_count`,
  `_aggregate`, `_bulk`, `_update_by_query`, `_delete_by_query`, `_watch`.
- **An id always comes after an action:** `/{resource}/_doc/{id}`,
  `_update/{id}`. Any id works, even `"stats"` or `"_search"`.
- **Query vs search:** `query` is what matches (the filter body key,
  everywhere). A search is the whole request (query + sort + limit + cursor).
  No `where` in the wire format.
- **Writes:** the filter goes in `query`, the operators in `update`. Options
  (`many`, `upsert`, `expire_in`) go in the body; only a safety `confirm` goes
  in the URL.
- **One version field name** (`_v`) in rows, write results and events.
- **Answers:** rows are a JSON array by default, NDJSON with
  `Accept: application/x-ndjson`; the same rows in both. Page data in
  headers (`X-…-Next`, `-Total`, `-Plan`, `-Took-Ms`), never an envelope.
- **Errors:** one body `{"error":{"code","message"}}`; `code` is the
  contract. Unknown path: `404 unknown_route`; known path, other method:
  `405 method_not_allowed` with `Allow`. A write that matches nothing is
  `200` with `"matched": 0`.
- **Upsert** = create by id or merge fields into the stored doc (ES
  `doc_as_upsert`), one transaction for many. Insert stays create-only.
- **No version in the path.** Breaking changes ship in a major, with a
  migration table in the CHANGELOG. No compatibility layer when nobody uses
  the old API yet.
- Use a small segment router, not mux patterns that overlap
  (`/_blobs/{ref}` vs `/{coll}/_stats`).

## 2. SDK vocabulary

One name per action, after the route; each language keeps its case and
idioms (Go `CamelCase`, Ruby `snake_case`, TS `camelCase`).

| Action | Go | Ruby | TS |
|---|---|---|---|
| connect | `Connect(addr)` | `Ndb.connect(addr)` | `connect(addr)` |
| handle | `Collection[T](db, name)` | `db.collection(name, def)` | `db.collection(name, schema)` |
| insert | `Insert(doc)`, `InsertMany(docs)` | `insert(doc_or_docs)` | `insert(docOrDocs)` |
| upsert | `Upsert(doc)`, `UpsertMany(docs)` | `upsert`, `upsert_all` | `upsert`, `upsertAll` |
| by id | `Get`, `Update(id)`, `Delete(id)` | `get`, `update(id)`, `delete(id)` | same, camel |
| by query | `UpdateBy`, `DeleteBy` | `update_by`, `delete_by` | `updateBy`, `deleteBy` |
| read | `Query(...)…All/First/Count/Each`, `Search(body)`, `RawQuery` | `query(...)…all/first/count/each/page`, `search`, `raw_query` | `query(...)…all/first/count/each`, `search`, `rawQuery` |
| admin | `Stats`, `Compact`, `EnsureIndex`, `DropIndex`; db `Health`, `Collections`, `Stats`, `Size` | snake | camel |

- A handle declares its resource lazily, on the first call. Truncate and drop
  never declare.
- Every SDK has the same features (no "only in Ruby" gaps): blobs, admin,
  health, raw queries.
- Ruby: two layers (friendly `Collection` on a low-level `Client`, one method
  per route). Use `Data.define`, never `Struct`. Methods accept both a Hash
  and keyword arguments.
- Rails keeps the Rails way (`where`, `order`, `find_by`, `Model.upsert`),
  and only changes the routes inside.
- "Not found": idiomatic per language (Go error, Ruby `nil`, TS `null`).

## 3. Docs

- **An HTTP tab next to UI** in the docs site, one page per route group:
  Overview, System, Resources/Collections, Documents, Queries, Watch, Errors.
- **The HTTP pages are the contract with the world:** every wire behavior is
  written, or it is a docs bug. The Overview covers: rules, every route (one
  table with links), transport, auth, requests, answers and headers, order
  and pagination, consistency, readiness and retries, streams, limits (one
  table), versioning, no CORS.
- **One template per route:** `## \`METHOD /path\`` (in backticks, so `{id}`
  is not read as a markdown attribute), one line of purpose, a body table
  (key, type, default, meaning), the answer, the errors, then a code group:
  curl, Go, Ruby, Rails, TS.
- The language pages (query, aggregate, watch) explain semantics and link to
  the route pages: a route is described in one place only.
- `llms.txt` / `llms-full.txt` include every docs folder (HTTP and UI too).
- Check the build, the anchors in the built HTML, and screenshots at desktop
  and 360 px.

## 4. Landing page

- Hero: the positioning in one line ("The API of X. The filters of Y.
  One binary."), a real request in the lead, buttons to the API and to "Why".
- "The best of two worlds": what comes from each side, then **the same call
  in tabs** (curl, Go, Ruby, Rails, TS).
- "Why developers try it": six short reasons.
- One honest note on what it does not do. No invented numbers.
- Site description and README top say the same thing.

## 5. Way of working

1. Discuss the idea, then write the plan in `.plans/` (decisions, routes,
   bodies, SDK table, docs, order of work, tests first). Record every choice
   the user makes, and the changes from the plan when building.
2. Build in layers, each one green before the next, one commit each:
   server → CLI and smoke tests → shared spec cases → Go → TS → Ruby → Rails
   → console → docs → CHANGELOG (with migration tables) → release.
3. Delegate independent, file-disjoint work (examples, READMEs) to a
   background agent while writing docs; review and run its result.
4. Release: versions in every SDK (and the Rails → core gem dependency on a
   major), CHANGELOG section and compare links, all suites, merge to `main`,
   tag. The cloud session cannot push tags: the user pushes the tag from the
   Mac. Do not watch CI with `gh` (it spends the user's CLI limit).
5. Conversation in Portuguese; code, commits and docs in plain English with
   short sentences.
