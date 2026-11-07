# M15: API 1.0 (routes, query, SDK vocabulary)

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
| POST | `/{coll}/_search` | `{"query","sort","fields","limit","skip","after","count","snapshot","explain","versions"}` | search envelope |
| POST | `/{coll}/_count` | `{"query"}` | `{"count":n}` (new) |
| POST | `/{coll}/_aggregate` | `{"pipeline","limit","skip","after","count","explain"}` | search envelope |
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

### Search envelope

`_search` and `_aggregate` answer JSON:

```json
{
  "hits": [ { "_id": "c1", "status": "answered" } ],
  "total": 1234,
  "next": "eyJ…",
  "plan": "index:status",
  "examined": 1500,
  "took_ms": 3,
  "explain": { }
}
```

- `total` only with `count: true`; `next` absent on the last page;
  `explain` only with `explain: true`. Rows of a pattern carry `_coll`.
- `Accept: application/x-ndjson` gives the rows as NDJSON with the
  `X-Ndb-*` headers of today (exports, `jq`, `ndb query -json`).
- Why JSON by default: the server builds the page in memory anyway
  (`collection.Page.Rows`), so NDJSON saves little on a page; metadata in
  the body is easier for `fetch`, the console, curl and Postman (no CORS
  header exposure). `_watch` stays NDJSON: it is a real stream.

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
| whole body | `c.Search(ctx, body)` → `{Hits, Next, Total}` | `c.search(body)` | `c.search(body)` | `_search` |
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
  `db.Health`. Results: `FindResult` → `SearchResult{Hits, Next, Total}`.
- **TypeScript:** `db.model` → `db.collection`. `where` → `query`,
  `whereRaw` → `rawQuery`, `find` → `search`, `iterate` → `each`,
  `update(idOrFilter)` → `update(id)`/`updateBy`, same for delete; alias
  `create` removed; `watch({ where })` → `watch({ query })`. New: blobs,
  `collections`, `stats`, `size`, `compact`, index admin. `Row._coll` stays.
- **Ruby:** new `Ndb.connect` and `Ndb::Collection` with the builder and the
  table above. `Ndb::Client` stays as the low-level layer, one method per
  route (`search`, `count`, `aggregate`, `get`, `insert`, `bulk`, `update`,
  `update_by`, `delete`, `delete_by`, ...). `Page#next_cursor` → `next`,
  `rows` → `hits`.
- **Rails:** the Rails way does not change (`where`, `where.not`, `order`,
  `limit`, `find_by`, `where_raw`, `Model.search`). Inside it moves to the
  new routes and keys (`query`, `update`). `Model.search` takes `query`.

## Other users of the API

- **Console** (`console/`): `src/ndb.ts`, `discover.ts`, the devtools
  (`devtools.tsx`, `devtools-blocks.ts`, `json-model.ts`), `routes/watch.tsx`,
  `routes/backups.tsx`, and the tests. The devtools examples show the new
  routes and `query`.
- **CLI** (`cmd/ndb`): about 20 calls; flags and output stay. `ndb query`
  reads the envelope (or NDJSON with `-json`).
- **Spec cases** (`spec/cases`): shared by the SDK suites; `where` →
  `query`, routes, envelope.
- **Smoke and golden tests** (`test/`).
- **Docs:** `http-api.md` rewritten from the tables above; `query.md`,
  `watch.md`, `aggregate.md`, `operations.md`, the client guides, the
  deploy recipes (`curl` health checks), the `llms.txt` build, the README.

## Order of work

1. Server: router (system `_` routes, `/{coll}/_action`, `unknown_route`),
   `query`/`update` bodies, options in the body, `_v` in write results,
   `_count`, `PUT _indexes`, search envelope + NDJSON by `Accept`. Tests
   first: a route table test, the envelope, the body errors.
2. CLI and the smoke/golden tests.
3. Spec cases.
4. Go SDK, then TS, then Ruby (new `Collection`), then Rails.
5. Console.
6. Docs, CHANGELOG (a migration table old → new), release 1.0.0.

Each step ends green (`go test ./...`, the SDK suites, the console tests)
before the next.

## Tests first

- Every route of the tables answers; an old `/v1/...` path is `404
  unknown_route`; `/_nope` is `404 unknown_route`.
- A document with `_id` `"_search"` or `"stats"` is read by
  `GET /{coll}/_doc/{id}`.
- `{"where": ...}` is `invalid_query` (unknown key).
- `_update_by_query` with a `$set` inside `query` is `invalid_query`.
- `_search` JSON envelope and NDJSON with `Accept` give the same rows and
  cursor.
- `_count` equals `total` of a search with `count: true`.
- Write results have `_v`, never `v`.
- Each SDK passes the shared spec cases with the new vocabulary; the
  console tests pass.
