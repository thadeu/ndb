# M15: API 1.0 (routes, query, SDK vocabulary)

> Built (2026-11-07): server router (`internal/server/router.go`), the SDKs,
> the console, the docs HTTP tab (`docs/dev/http.md`, `docs/dev/http/`).
> Changes from this plan: Go inserts are `Insert(ctx, doc)` and
> `InsertMany(ctx, docs)` (one Go method cannot take one doc or many); the
> Ruby event ack is `ack(event, consumer)`; a write operator in `query` is
> `unknown_operator` (not `invalid_query`); the console keeps its own URL
> parameter `where` for its filter box (its calls to ndb send `query`).
>
> Decision (2026-11-07): ndb 1.0.0 is a breaking release. No compatibility
> layer: nobody uses the HTTP API yet except our own console.
> The mix: the URL shape of Elasticsearch (`/{coll}/_action`) and the query
> language of MongoDB (`$gte`, `$in`, `$set`, `$elemMatch`) in the body.

## Why

- `/v1/c/{coll}/find` is noisy. `/{coll}/_search` reads like what it does.
- `/v1/c/{coll}/{id}` collides with the fixed routes: a document with `_id`
  `"stats"` cannot be read by `GET`.
- `DELETE /v1/c/{coll}` (drop) and `DELETE /v1/c/{coll}/{id}` (one doc) are
  one segment apart.
- Update and delete by filter mix the filter and the write operators in one
  object ("a `$` key is an operator"). It is the hardest rule to explain.
- `where` is SQL/Rails wording. A search has a `query`, like in ES.
- The SDKs name the same action in different ways (`Iter`/`iterate`,
  `UpdateID`/`update(idOrFilter)`, `find`/`search`), and each one misses
  things the others have (blobs, collections, health, stats).

## Rules

1. **`_` is the system namespace.** A collection name never starts with `_`
   (`^[a-z0-9][a-z0-9_-]{0,63}$`), so `/_health` and `/{coll}/_search` never
   collide with data. An unknown `_` segment is `404 unknown_route`.
2. **A document id is always after `_doc`, `_update`, `_expire`, `_persist`
   or `_ttl`.** It never collides with an action.
3. **Query is what matches; search is the whole request.** The body key is
   `query` everywhere a filter goes. `where` is gone (Rails keeps the
   `where` method, Rails way).
4. **The write operators go in `update`, apart from `query`.**
5. **Options go in the body,** not in the query string (`many`, `upsert`,
   `expire_in`). Only `confirm` stays in the URL, on purpose, for drop and
   truncate.
6. **One version field name: `_v`.** Write results answer `{"_id","_v"}`
   (today `{"_id","v"}`).
7. No `/v1`. If a version is ever needed again, it is a header, not a path.

## Routes

### System

| Method | Path | Body | Answer |
|---|---|---|---|
| GET | `/_health` | | `{"status":"ok"}` or 503 |
| GET | `/_metrics` | | Prometheus text |
| GET | `/_collections` | | `{"collections":[{definition}...]}` |
| GET | `/_stats` | | every collection, totals, version, uptime |
| GET | `/_size` | | disk use per collection and part |
| POST | `/_backup` | `{"no_blobs"}` | `.ndb` stream; unix socket only |
| POST | `/_blobs` | raw bytes | the `$blob` reference |
| GET | `/_blobs/{ref}` | | the bytes; `Range` |

### Collection

| Method | Path | Body | Answer |
|---|---|---|---|
| PUT | `/{coll}` | definition | `{"definition","indexes"}` |
| GET | `/{coll}` | | `{"definition","indexes"}`; a pattern lists the names |
| DELETE | `/{coll}?confirm={coll}` | | `{"collection","dropped":true}` |
| POST | `/{coll}/_truncate?confirm={coll}` | | `{"collection","truncated":true}` |
| PUT | `/{coll}/_indexes/{field}` | | `{"collection","indexes"}` (new) |
| DELETE | `/{coll}/_indexes/{field}` | | `{"collection","indexes"}` |
| GET | `/{coll}/_stats` | | rows, indexes, files, bytes |
| POST | `/{coll}/_compact` | | before/after segments and bytes |
| GET | `/{coll}/_quarantine?limit=N` | | last bad lines (direct mode) |

### Documents

| Method | Path | Body | Answer |
|---|---|---|---|
| POST | `/{coll}/_doc` | the document | `{"_id","_v"}`; `expire_in` only through `_bulk` |
| POST | `/{coll}/_bulk` | `{"docs":[...],"expire_in"}` | `{"results":[{"_id","_v"}]}`; all or nothing |
| GET | `/{coll}/_doc/{id}` | | the document with `_v`; 404 `not_found` |
| DELETE | `/{coll}/_doc/{id}` | | `{"deleted":0\|1}` |
| POST | `/{coll}/_update/{id}` | `{"update":{"$set"...},"query":{"_v":7},"upsert","expire_in"}` | `{"matched","modified","doc"}` |
| POST | `/{coll}/_expire/{id}` | `{"in"}` or `{"at"}` | `{"_id","_v","expires_at"}` |
| POST | `/{coll}/_persist/{id}` | | `{"_id","_v"}` |
| GET | `/{coll}/_ttl/{id}` | | `{"_id","ttl","expires_at"}` |

- `_bulk` is insert only in 1.0. The ES action-line format (mixed index,
  update, delete in NDJSON) is a later option, not this milestone.
- `POST /{coll}/_doc` creates an unknown collection, like insert today.
- In `_update/{id}`, `query` holds extra guards on the same document (the
  optimistic `_v` today). No match is `matched: 0`, not an error.

### Queries

| Method | Path | Body | Answer |
|---|---|---|---|
| POST | `/{coll}/_search` | `{"query","sort","fields","limit","skip","after","count","snapshot","explain","versions"}` | rows (JSON array or NDJSON) + `X-Ndb-*` headers |
| POST | `/{coll}/_count` | `{"query"}` | `{"count":n}` (new) |
| POST | `/{coll}/_aggregate` | `{"pipeline","limit","skip","after","count","explain"}` | rows (JSON array or NDJSON) + `X-Ndb-*` headers |
| POST | `/{coll}/_update_by_query` | `{"query","update","many","upsert","expire_in"}` | `{"matched","modified","doc"}` |
| POST | `/{coll}/_delete_by_query` | `{"query","many"}` | `{"deleted"}` |
| POST | `/{coll}/_watch` | `{"consumer","from","limit","query"}` | NDJSON stream |
| POST | `/{coll}/_ack` | `{"consumer","pos"}` | `{"ok":true}` |

- Collection patterns work on `_search`, `_count`, `_aggregate` and
  `GET /{pattern}`: `POST /logs-2026-*/_search`. Any other route with a
  pattern is `400 pattern_read_only`.
- `_aggregate` keeps `$match` as the stage name (pipeline, Mongo).
- `_update_by_query` and `_delete_by_query` keep the contract of today:
  `many` needs `_id` or an indexed field (`unindexed_many`), an empty
  `query` is `empty_filter`.

### Search answer

`_search` and `_aggregate` answer the rows, and nothing else, in the body.
The format of the rows is the same in JSON and NDJSON; only the framing
changes:

```
Accept: application/json (default)     [ {"_id":"c1","status":"answered"}, {"_id":"c2",...} ]
Accept: application/x-ndjson           {"_id":"c1","status":"answered"}
                                       {"_id":"c2",...}
```

- The page data is always in headers, in both formats: `X-Ndb-Next`
  (absent on the last page), `X-Ndb-Total` (with `count: true`),
  `X-Ndb-Plan`, `X-Ndb-Examined`, `X-Ndb-Took-Ms` (new), `X-Ndb-Explain`
  (with `explain: true`).
- No CORS: the daemon is called by servers (apps, the console server, the
  CLI), never by a browser directly, so it sends no CORS headers. The
  console reads the `X-Ndb-*` headers on its server side.
- Rows of a pattern carry `_coll`, in both formats.
- Why JSON by default: the server builds the page in memory anyway
  (`collection.Page.Rows`), so NDJSON saves little on a page, and a JSON
  array is what `res.json()`, curl and Postman expect. NDJSON stays
  for exports and `jq` (`ndb query -json`). `_watch` stays NDJSON: it is a
  real stream.

### Errors

Same body `{"error":{"code","message"}}` and codes. New: `unknown_route`
(404). Gone: none.

## SDK vocabulary (Go, Ruby, TypeScript)

One name per action, after the route. Each language keeps its case:
`CamelCase` (Go), `snake_case` (Ruby), `camelCase` (TS).

| Action | Go | Ruby | TS | Route |
|---|---|---|---|---|
| connect | `ndb.Connect(addr, opts...)` | `Ndb.connect(addr, **opts)` | `connect(addr, opts)` | |
| collection | `ndb.Collection[T](db, name)` | `db.collection(name, definition)` | `db.collection(name, schema)` | `PUT /{coll}` on first use |
| insert one / many | `Insert(ctx, docs...)` | `insert(doc_or_docs, expire_in:)` | `insert(docOrDocs, { expireIn })` | `_doc` / `_bulk` |
| get | `Get(ctx, id)` → `ErrNotFound` | `get(id)` → `nil` | `get(id)` → `null` | `_doc/{id}` |
| update one | `Update(ctx, id, ops, opts...)` | `update(id, ops, **opts)` | `update(id, ops, opts)` | `_update/{id}` |
| update by query | `UpdateBy(ctx, query, ops, opts...)` | `update_by(query, ops, **opts)` | `updateBy(query, ops, opts)` | `_update_by_query` |
| delete one | `Delete(ctx, id)` | `delete(id)` | `delete(id)` | `DELETE _doc/{id}` |
| delete by query | `DeleteBy(ctx, query, opts...)` | `delete_by(query, **opts)` | `deleteBy(query, opts)` | `_delete_by_query` |
| query builder | `c.Query(filters...)` | `c.query(hash)` | `c.query(obj)` | `_search` |
| query built elsewhere | `ndb.RawQuery(map)`, `ndb.RawQueryJSON(b)` | `c.raw_query(hash)` | `c.rawQuery(obj)` | `_search` |
| builder steps | `.Sort .Limit .Skip .After .Fields .Page` | same, snake | same, camel | |
| terminals | `.All .First .Count .Each` | `.all .first .count .each` | `.all .first .count .each` | `_search`, `_count` |
| whole body | `c.Search(ctx, body)` → `{Rows, Next, Total}` | `c.search(body)` | `c.search(body)` | `_search` |
| aggregate | `c.Aggregate(ctx, p, opts...)` | `c.aggregate(pipeline, **opts)` | `c.aggregate(pipeline, opts)` | `_aggregate` |
| watch / ack | `c.Watch(ctx, consumer, opts...)` / `c.Ack(ctx, ev)` | `watch(consumer, query:, from:, limit:)` / `ack(ev)` | `watch(consumer, opts)` / `ack(ev)` | `_watch` / `_ack` |
| ttl | `Expire(ctx, id, d)`, `ExpireAt`, `Persist`, `TTL` | `expire(id, in:\|at:)`, `persist`, `ttl` | `expire`, `expireAt`, `persist`, `ttl` | `_expire` `_persist` `_ttl` |
| collection admin | `Truncate Drop Stats Compact EnsureIndex DropIndex` | snake | camel | |
| db admin | `db.Collections Stats Size Health PutBlob GetBlob` | snake | camel | system routes |

Changes per SDK:

- **Go:** `Open` → `Connect`. `Collection` takes no ctx and declares on
  first use; a pattern name needs no `Pattern[T]` (removed). `Where` →
  `Query`, `Raw`/`RawJSON` → `RawQuery`/`RawQueryJSON`, `Find` → `Search`,
  `Iter` → `Each`, `UpdateID`/`Update(filter)` → `Update(id)`/`UpdateBy`,
  `DeleteID`/`Delete(filter)` → `Delete(id)`/`DeleteBy`, `InsertWith` →
  `Insert` with options, `WatchWhere` → `WatchQuery`, `Ack(consumer,pos)` →
  `Ack(event)`. New: `Count` on the collection, `Stats`, `Compact`,
  `EnsureIndex`, `DropIndex`, `db.Collections`, `db.Stats`, `db.Size`,
  `db.Health`. Results: `FindResult` → `SearchResult{Rows, Next, Total}`.
- **TypeScript:** `db.model` → `db.collection`. `where` → `query`,
  `whereRaw` → `rawQuery`, `find` → `search`, `iterate` → `each`,
  `update(idOrFilter)` → `update(id)`/`updateBy`, same for delete; alias
  `create` removed; `watch({ where })` → `watch({ query })`. New: blobs,
  `collections`, `stats`, `size`, `compact`, index admin. `Row._coll` stays.
- **Ruby:** new `Ndb.connect` and `Ndb::Collection` with the builder and the
  table above. `Ndb::Client` stays as the low-level layer, one method per
  route (`search`, `count`, `aggregate`, `get`, `insert`, `bulk`, `update`,
  `update_by`, `delete`, `delete_by`, ...). `Page#next_cursor` → `next`.
- **Rails:** the Rails way does not change (`where`, `where.not`, `order`,
  `limit`, `find_by`, `where_raw`, `Model.search`). Inside it moves to the
  new routes and keys (`query`, `update`). `Model.search` takes `query`.

## Other users of the API

- **Console** (`console/`): `src/ndb.ts`, `discover.ts`, the devtools
  (`devtools.tsx`, `devtools-blocks.ts`, `json-model.ts`), `routes/watch.tsx`,
  `routes/backups.tsx`, and the tests. The devtools examples show the new
  routes and `query`.
- **CLI** (`cmd/ndb`): about 20 calls; flags and output stay. `ndb query`
  reads the JSON rows and headers (NDJSON with `-json`).
- **Spec cases** (`spec/cases`): shared by the SDK suites; `where` →
  `query`, routes, JSON rows.
- **Smoke and golden tests** (`test/`).
- **Docs:** `query.md`, `watch.md`, `aggregate.md`, `operations.md`, the
  client guides, the deploy recipes (`curl` health checks), the README. The
  HTTP reference becomes its own tab (below).

## Docs: an HTTP tab next to UI

The route groups of this plan (system, collection, documents, queries,
watch) are how people and AI agents look for a route. The HTTP reference
gets its own tab in the docs site, next to UI, with one page per group.
`docs/dev/http-api.md` is removed; its content moves here.

```
docs/dev/http.md               /http             Overview
docs/dev/http/system.md        /http/system      _health _metrics _collections _stats _size _backup _blobs
docs/dev/http/collections.md   /http/collections PUT GET DELETE /{coll}, _truncate, _indexes, _stats, _compact, _quarantine
docs/dev/http/documents.md     /http/documents   _doc _bulk _update/{id} _expire _persist _ttl
docs/dev/http/queries.md       /http/queries     _search _count _aggregate _update_by_query _delete_by_query, patterns
docs/dev/http/watch.md         /http/watch       _watch _ack
docs/dev/http/errors.md        /http/errors      the error body and every code, with its status
```

- **Overview (`/http`):** the rules of this plan (`_` namespace, ids after
  `_doc`, query vs search, `update` apart from `query`, options in the body,
  `_v`), unix socket and TCP, the token, the two answer formats and the
  `X-Ndb-*` headers, and one table of every route that links to its page.
- **One template per route,** the same on every page, so a person can scan
  it and an agent can parse it:

  ```
  ### POST /{coll}/_search
  One line: what it does.
  | Body key | Type | Default | Meaning |
  Answer: shape + headers.
  Errors: the codes this route can return (links to /http/errors).
  curl example, then the same call in Go, Ruby, TS (code group).
  ```

- **Navigation:** `docs-site/.vitepress/nav.ts` gets a tab `{ id: 'http',
  text: 'HTTP', link: '/http' }` next to `ui`, with groups Start (Overview,
  Errors) and Routes (System, Collections, Documents, Queries, Watch). The
  Daemon tab "HTTP API" and "Errors" links point to `/http` and
  `/http/errors`. The Query tab links follow the renames (`find` → search).
- **Site config:** a rewrite `'docs/dev/http/:page.md': 'http/:page.md'`, as
  for `ui`.
- **llms.txt / llms-full.txt:** add `docs/dev/http` to `docsRoots` in
  `docs-site/.vitepress/config.ts`. `docs/dev/ui` is missing there today;
  add it too.
- **The HTTP pages are the contract of ndb with the world.** Every rule of
  the protocol is written there, not only the routes. Anything the daemon
  does on the wire and that a client can see must be on these pages; a
  behavior that is not written is a bug in the docs. The Overview page
  covers, each in its own section:
  - **Transport:** HTTP/1.1, unix socket and TCP (`listen.tcp`), keep-alive,
    the host name on a socket, no TLS in the daemon (put a proxy in front).
  - **Auth:** the bearer token on TCP (`-token-file`), none on the socket,
    `401 unauthorized`, `403 forbidden` (backup over TCP).
  - **Requests:** `Content-Type: application/json`, UTF-8, one JSON object
    per body, unknown keys are errors, the body limit (`max_line_size`,
    `413 request_too_large`), numbers (64-bit floats; big integers keep
    their text in documents), times (RFC 3339 on the `time` field, date
    math), ids (UUIDv7 when absent, the `_id` rules).
  - **Answers:** JSON or NDJSON by `Accept`, the shape of rows (`_id`,
    `_v`, `_coll`), every `X-Ndb-*` header with its meaning, status codes.
  - **Order and pagination:** sort, the id tie-break, insertion order,
    `skip`/`limit`/`after`, cursors bound to the query, `snapshot`.
  - **Consistency:** a write is acked after the WAL (durability modes), a
    read after an ack sees the write, one write is atomic, a `many` write is
    atomic per record, optimistic `_v` guards.
  - **Readiness and retries:** `503 not_ready` while recovering, `503
    closed` on shutdown, `499 canceled`; which routes are safe to retry
    (reads, `_doc/{id}` GET) and which are not (writes, unless guarded),
    what the SDKs retry.
  - **Streams:** `_watch` is NDJSON; heartbeat lines and their interval,
    how a stream ends (truncate, drop, shutdown, client gone), resume with
    `from` and `_ack`.
  - **Limits:** every cap in one table (body size, regex 1 KiB, group table
    memory, pattern 256 collections, snapshot TTL 60 s, names 64 chars).
  - **Versioning:** no version in the path; how breaking changes ship
    (major release, CHANGELOG migration table).
  - **No CORS:** the daemon is for servers; browsers go through an app.
- `query.md`, `aggregate.md` and `watch.md` keep the language (operators,
  stages, events) and link to the HTTP pages for the routes, so a route is
  described in one place only.

## Order of work

1. Server: router (system `_` routes, `/{coll}/_action`, `unknown_route`),
   `query`/`update` bodies, options in the body, `_v` in write results,
   `_count`, `PUT _indexes`, JSON rows by default + NDJSON by `Accept`, exposed
   headers. Tests first: a route table test, both formats, the body errors.
2. CLI and the smoke/golden tests.
3. Spec cases.
4. Go SDK, then TS, then Ruby (new `Collection`), then Rails.
5. Console.
6. Docs: the HTTP tab (pages, nav, rewrites, llms roots), then the other
   pages; CHANGELOG (a migration table old → new); release 1.0.0.

Each step ends green (`go test ./...`, the SDK suites, the console tests)
before the next.

## Tests first

- Every route of the tables answers; an old `/v1/...` path is `404
  unknown_route`; `/_nope` is `404 unknown_route`.
- A document with `_id` `"_search"` or `"stats"` is read by
  `GET /{coll}/_doc/{id}`.
- `{"where": ...}` is `invalid_query` (unknown key).
- `_update_by_query` with a `$set` inside `query` is `invalid_query`.
- `_search` in JSON and in NDJSON (`Accept`) give the same rows, in the
  same shape, and the same headers.
- `_count` equals `total` of a search with `count: true`.
- Write results have `_v`, never `v`.
- Each SDK passes the shared spec cases with the new vocabulary; the
  console tests pass.
