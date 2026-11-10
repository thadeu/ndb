# Document routes

Write, read and remove one document by `_id`, insert many, and set the expiry of a document. The id always comes after the action (`/{coll}/_doc/{id}`), so any id works; escape `/` as `%2F`. The rules that apply to every route are on [HTTP API](../http.md).

Writes by query (`_update_by_query`, `_delete_by_query`) are on [Queries](queries.md). Writes are engine mode only: on a direct collection they are `409 direct_collection`.

## `POST /{coll}/_doc`

Inserts one document. The body is the document. Without `_id`, the daemon makes a UUIDv7 (time ordered). An unknown collection is created with the default definition, unless `allow_implicit: false`.

Field names follow the rules of `$set`, on every write (`_doc`, `_bulk`, `_bulk` with `upsert`):

- `_v` at the top level is the version, which the daemon sets and adds to every answer. A `_v` that you send (for example a document you read and send back in an upsert) is dropped: it is never stored and never changes the version. To write only on a given version, use the `_v` guard of `_update`.
- A field name that starts with `$`, at any depth (also in the objects of an array), is `400 invalid_field`. The one exception is `$blob`, the key of a [blob reference](system.md#post-blobs).

**Answer:** `{"_id":"c1","_v":1}`.

**Errors:** `409 duplicate_id`, `409 unique_violation`, `400 invalid_doc`, `invalid_id`, `invalid_field`, `413 request_too_large`.

::: code-group

```bash [curl]
curl --unix-socket /run/ndb/ndb.sock http://ndb/calls/_doc \
  -d '{"_id":"c1","status":"answered","ts":"2026-11-07T10:00:00Z"}'
# {"_id":"c1","_v":1}
```

```go [Go]
res, err := calls.Insert(ctx, Call{ID: "c1", Status: "answered", TS: time.Now()})
```

```ruby [Ruby]
calls.insert(_id: "c1", status: "answered", ts: Time.now)
# => {"_id" => "c1", "_v" => 1}
```

```ts [TypeScript]
const res = await calls.insert({ _id: 'c1', status: 'answered', ts: new Date() })
```

:::

## `POST /{coll}/_bulk`

Inserts many documents in one transaction: all or nothing. On a partitioned collection the call is all or nothing too, except in two narrow cases (a disk error, or another call that takes the same id or unique value at the same time): see [Partitioned collections](collections.md#partitioned-collections).

| Body key | Type | Default | Meaning |
|---|---|---|---|
| `docs` | object[] | required | the documents, at least one |
| `expire_in` | duration | none | every document expires this long from now (`"30m"`, `"1d"`, or seconds); needs `expires_field` |
| `upsert` | bool | `false` | create or merge by `_id`, see [Upsert](#upsert) |

**Answer:** `{"results":[{"_id","_v"},...]}`, in the order of `docs`.

**Errors:** as for `_doc`; `400 invalid_request` for an empty `docs`; `ttl_not_enabled`, `ttl_derived` with `expire_in`.

::: code-group

```bash [curl]
curl --unix-socket /run/ndb/ndb.sock http://ndb/calls/_bulk \
  -d '{"docs":[{"_id":"c1","status":"answered"},{"_id":"c2","status":"failed"}]}'
# {"results":[{"_id":"c1","_v":1},{"_id":"c2","_v":1}]}
```

```go [Go]
res, err := calls.InsertMany(ctx, []Call{{ID: "c1"}, {ID: "c2"}}, ndb.ExpireIn(time.Hour))
```

```ruby [Ruby]
calls.insert([{ _id: "c1" }, { _id: "c2" }], expire_in: "1h")
```

```ts [TypeScript]
const res = await calls.insert([{ _id: 'c1' }, { _id: 'c2' }], { expireIn: '1h' })
```

:::

### Upsert

`insert` only creates: an `_id` that exists is `409 duplicate_id`. With `"upsert": true`, `_bulk` writes each document by its `_id`, like `doc_as_upsert` in Elasticsearch:

- the `_id` is free: the document is created (`_v` 1);
- the `_id` exists: its top-level fields **replace** the fields of the same name in the stored document; the other fields stay. `_v` goes up by one;
- no `_id`: an insert with a generated id.

The whole call is one transaction: a unique violation on any document leaves every document out. Use `_update/{id}` with `upsert` for operators (`$inc`, `$push`) instead of plain fields.

::: code-group

```bash [curl]
curl --unix-socket /run/ndb/ndb.sock http://ndb/users/_bulk \
  -d '{"docs":[{"_id":"u1","plan":"pro"},{"_id":"u2","name":"Bia"}],"upsert":true}'
# {"results":[{"_id":"u1","_v":2},{"_id":"u2","_v":1}]}
```

```go [Go]
res, err := users.Upsert(ctx, User{ID: "u1", Plan: "pro"})
all, err := users.UpsertMany(ctx, []User{{ID: "u1", Plan: "pro"}, {ID: "u2", Name: "Bia"}})
```

```ruby [Ruby]
users.upsert(_id: "u1", plan: "pro")
users.upsert_all([{ _id: "u1", plan: "pro" }, { _id: "u2", name: "Bia" }])
```

```ruby [Rails]
User.upsert(id: "u1", plan: "pro")
User.upsert_all([{ id: "u1", plan: "pro" }, { id: "u2", name: "Bia" }])
```

```ts [TypeScript]
await users.upsert({ _id: 'u1', plan: 'pro' })
await users.upsertAll([{ _id: 'u1', plan: 'pro' }, { _id: 'u2', name: 'Bia' }])
```

:::

In Go, a struct sends every field of `T`, so every field of `T` is set; fields of the stored document that `T` does not have stay. Rails sends only the attributes you pass, and runs no validations or callbacks, like `upsert` in ActiveRecord.

## `GET /{coll}/_doc/{id}`

The document with its `_v`. The answer has one `_v`, the version: a document stored with its own `_v` (before the write refused it) answers without that one, and the next update of the document removes it.

**Errors:** `404 not_found`. In direct mode, a document without `_id` is never found by id.

::: code-group

```bash [curl]
curl --unix-socket /run/ndb/ndb.sock http://ndb/calls/_doc/c1
# {"_id":"c1","_v":1,"status":"answered"}
```

```go [Go]
call, err := calls.Get(ctx, "c1") // ndb.ErrNotFound when missing
```

```ruby [Ruby]
calls.get("c1") # => nil when missing
```

```ts [TypeScript]
const call = await calls.get('c1') // null when missing
```

:::

## `DELETE /{coll}/_doc/{id}`

Removes the document. **Answer:** `{"deleted":1}`, or `{"deleted":0}` when it was not there (not an error).

::: code-group

```bash [curl]
curl --unix-socket /run/ndb/ndb.sock -X DELETE http://ndb/calls/_doc/c1
```

```go [Go]
n, err := calls.Delete(ctx, "c1")
```

```ruby [Ruby]
calls.delete("c1")
```

```ts [TypeScript]
await calls.delete('c1')
```

:::

## `POST /{coll}/_update/{id}`

Applies write operators to the document with this `_id`, in one atomic step against the newest state.

| Body key | Type | Default | Meaning |
|---|---|---|---|
| `update` | object | required | write operators: `$set`, `$setOnInsert`, `$unset`, `$inc`, `$push`, `$pull`, `$min`, `$max` |
| `query` | object | none | extra conditions on the same document, such as `{"_v": 7}` or `{"qty": {"$gte": 1}}`; it must not name `_id` |
| `upsert` | bool | `false` | create the document with this `_id` when it is missing |
| `expire_in` | duration | none | set `expires_field` to now + `expire_in` |

**Answer:** `{"matched":1,"modified":1,"doc":{...with "_v"}}`. When a condition fails or the document is missing: `{"matched":0,"modified":0}`, not an error. `modified` is 0 when the operators left the document as it was.

**Errors:** `no_update`, `unknown_operator`, `conflicting_update`, `invalid_field`, `invalid_path`, `immutable_id` (see [Contract errors](../query.md#contract-errors)), `invalid_request` for `_id` in `query` or `many`, `409 unique_violation`, `ttl_not_enabled` and `ttl_derived` with `expire_in`, `413 request_too_large`, `404 unknown_collection` (`_update` never creates the collection, also with `upsert`).

::: code-group

```bash [curl]
curl --unix-socket /run/ndb/ndb.sock http://ndb/stock/_update/sku-1 \
  -d '{"query":{"_v":7},"update":{"$inc":{"qty":-1},"$set":{"seen":true}}}'
# {"matched":1,"modified":1,"doc":{"_id":"sku-1","_v":8,"qty":4,"seen":true}}
```

```go [Go]
res, err := stock.Update(ctx, "sku-1", ndb.Ops{"$inc": {"qty": -1}}, ndb.Guard(ndb.Eq("_v", 7)))
```

```ruby [Ruby]
stock.update("sku-1", { "$inc" => { qty: -1 } }, guard: { _v: 7 })
```

```ts [TypeScript]
await stock.update('sku-1', { $inc: { qty: -1 } }, { guard: { _v: 7 } })
```

:::

The operators are on [Write operators](../query.md#write-operators).

## `POST /{coll}/_expire/{id}`

Makes a document expire, like `EXPIRE` in Redis. It is a normal write: a new `_v`, a WAL record and a watch event. The collection needs `expires_field`.

| Body key | Type | Meaning |
|---|---|---|
| `in` | duration | expire this long from now (`"30m"`, `"1d"`, or seconds) |
| `at` | string | expire at this RFC 3339 time; a time in the past expires it at once |

Exactly one of `in` and `at`.

**Answer:** `{"_id","_v","expires_at"}`.

**Errors:** `404 not_found` (missing or already expired), `400 ttl_not_enabled` (no `expires_field`), `400 ttl_derived` (the expiry comes from `ttl` + `time`), `400 invalid_request` (not exactly one of `in` and `at`), `404 unknown_collection`, `409 direct_collection` (a direct collection is written by its apps).

::: code-group

```bash [curl]
curl --unix-socket /run/ndb/ndb.sock http://ndb/sessions/_expire/s1 -d '{"in":"30m"}'
```

```go [Go]
v, err := sessions.Expire(ctx, "s1", 30*time.Minute)
```

```ruby [Ruby]
sessions.expire("s1", in: "30m")
```

```ts [TypeScript]
await sessions.expire('s1', '30m')
```

:::

## `POST /{coll}/_persist/{id}`

Removes the expiry of a document, like `PERSIST` in Redis. No body: ndb ignores a body if you send one. **Answer:** `{"_id","_v"}`. **Errors:** as for `_expire`, without `invalid_request`.

::: code-group

```bash [curl]
curl --unix-socket /run/ndb/ndb.sock -X POST http://ndb/sessions/_persist/s1
```

```go [Go]
v, err := sessions.Persist(ctx, "s1")
```

```ruby [Ruby]
sessions.persist("s1")
```

```ts [TypeScript]
await sessions.persist('s1')
```

:::

## `GET /{coll}/_ttl/{id}`

The time left of a document. **Answer:** `{"_id","ttl","expires_at"}`: `ttl` is the whole seconds left, rounded up, also for an expiry centuries away; a document that never expires has `"ttl": -1` and `"expires_at": null`. With `ttl` + `time` in the definition the expiry comes from the time field. A definition cannot have both `ttl` and `expires_field` (`400 catalog_invalid`).

**Errors:** `404 not_found` (missing or expired), `400 not_supported` for a direct collection.

The value of `expires_field` is an RFC 3339 string (`"2026-11-07T10:30:00Z"`) or a number of unix seconds. ndb does not check the value at write: a document whose `expires_field` holds another value (`"tomorrow"`, `true`, an object) is stored, and it never expires (`"ttl": -1`). To set an expiry that ndb checks, use `expire_in` or `_expire`.

::: code-group

```bash [curl]
curl --unix-socket /run/ndb/ndb.sock http://ndb/sessions/_ttl/s1
# {"_id":"s1","ttl":1799,"expires_at":"2026-11-07T10:30:00Z"}
```

```go [Go]
left, expires, err := sessions.TTL(ctx, "s1")
```

```ruby [Ruby]
sessions.ttl("s1") # => 1799, -1 (never), nil (missing)
```

```ts [TypeScript]
const left = await sessions.ttl('s1') // seconds, -1, or null
```

:::

The expiry model and the notes on disk space are on [TTL like Redis](../configuration.md#ttl-like-redis).
