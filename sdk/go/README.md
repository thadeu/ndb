# ndb Go SDK

<img src="https://raw.githubusercontent.com/thadeu/ndb/main/brand/png/lockup-on-light.png" width="220">

Go client for [ndb](https://github.com/thadeu/ndb), an NDJSON query daemon. It uses the standard library only. The SDK builds JSON and reads NDJSON. The daemon decides what every query means.

Docs: [client guide](https://github.com/thadeu/ndb/blob/main/docs/dev/clients.md#go-module-githubcomthadeundbsdkgo), [query language](https://github.com/thadeu/ndb/blob/main/docs/dev/query.md), [HTTP API](https://github.com/thadeu/ndb/blob/main/docs/dev/http.md).

## Install

```sh
go get github.com/thadeu/ndb/sdk/go@latest
```

```go
import ndb "github.com/thadeu/ndb/sdk/go"
```

The module needs a running ndb daemon. See the repository for how to run one. Releases of this module are tagged `sdk/go/vX.Y.Z`.

## Connect

```go
// Unix socket.
db, err := ndb.Connect("unix:///run/ndb/ndb.sock")

// TCP with a bearer token.
db, err := ndb.Connect("tcp://10.0.0.5:7070", ndb.WithToken("secret"))

// Address and token from the environment: NDB_URL and NDB_TOKEN.
db, err := ndb.Connect("")

// Retry for up to 5 s while the daemon starts or recovers.
db, err := ndb.Connect("unix:///run/ndb/ndb.sock", ndb.WithReadyTimeout(5*time.Second))

defer db.Close()

err = db.Health(ctx) // nil when the daemon is ready (GET /_health)
```

- `Connect` does not connect. The first call does.
- The address must be `unix:///path` or `tcp://host:port`. Anything else returns an error.
- The token goes only in the `Authorization` header. A unix socket does not need it.
- `WithReadyTimeout` sets how long a call retries when the daemon is not ready (HTTP 503 `not_ready`, or the socket is missing or refuses). The default is `ndb.DefaultReadyTimeout` (10 s). Zero turns the retry off. Writes are retried only when the daemon certainly did not process them. Reads are also retried after a dropped connection.
- The pool keeps up to 16 idle connections per host. There is no setting for it.
- `*DB` is safe for concurrent use. `Close` closes idle connections. The handle stays usable.
- `db.Health` does not wait: a daemon that is recovering is `ErrNotReady`.

## Define a collection

A collection maps to a Go struct. Tags use the form `ndb:"name,option,option"`.

```go
type Call struct {
	ID        string    `ndb:"_id"`
	Status    string    `ndb:"status,index"`
	Caller    string    `ndb:"caller,unique"`
	Duration  int       `ndb:"duration"`
	At        time.Time `ndb:"at,time"`
	ExpiresAt time.Time `ndb:"expires_at,expires"`
	Recording *ndb.Blob `ndb:"recording"`
	Version   uint64    `ndb:"_v,version"`
	Internal  string    `ndb:"-"`
}

calls, err := ndb.Collection[Call](db, "calls")
```

| Option | Meaning |
|---|---|
| `index` | Index this field. |
| `unique` | Unique value. A second document with it fails with `ErrUniqueViolation`. |
| `time` | The collection time field. Used by zone maps, `$dateTrunc` and TTL. |
| `expires` | The field holds the time the document expires. |
| `version` | The field receives `_v`, the document version, on every read. It is never sent on a write. |

- The primary key is the field named `_id` (`ndb:"_id"` or `json:"_id"`). If it is empty on insert, it is not sent and the daemon makes a UUIDv7. For a natural key (email, SKU) keep `_id` and add `unique` to that field. `ndb.NewID()` makes the same kind of id on the client.
- A field without an `ndb` tag uses its `json` name, or its Go name. `ndb:"-"` or `json:"-"` skips a field.
- Only one `time` and `expires` field are allowed. An unknown option is an error (the old `id` option too).
- `Collection` sends nothing. The first call on the collection declares it (`PUT /{coll}`) with the tags of `T`, once per name on one `*DB`. A definition that disagrees with the stored one (time, ttl) makes that call return `ErrCatalogConflict`. `Collection` itself fails only for bad tags.
- A name that ends with `*` is a collection pattern: `ndb.Collection[Line](db, "logs-2026-*")` reads every collection whose name starts with `logs-2026-`. It declares nothing, and a write fails with code `pattern_read_only`. Each row carries `_coll`, the name of its collection: map it with a field tagged `ndb:"_coll"`.

## Insert, get, update, delete

```go
// Insert one document (POST /calls/_doc).
res, err := calls.Insert(ctx, Call{ID: "c1", Status: "answered", Caller: "a", At: time.Now().UTC()})
// res.ID and res.V (the new version).

// Insert many in one transaction, all or nothing (POST /calls/_bulk).
results, err := calls.InsertMany(ctx, []Call{
	{ID: "c2", Status: "failed", Caller: "b", At: time.Now().UTC()},
	{ID: "c3", Status: "failed", Caller: "c", At: time.Now().UTC()},
})

// Get by _id (GET /calls/_doc/c1). A missing id returns ErrNotFound.
call, err := calls.Get(ctx, "c1")

// Atomic update by _id (POST /calls/_update/c1). Guard adds conditions on
// the same document: a _v guard is an optimistic lock.
upd, err := calls.Update(ctx, "c1",
	ndb.Ops{"$set": {"status": "done"}, "$unset": {"duration": true}},
	ndb.Guard(ndb.Eq("_v", call.Version)),
)
// upd.Matched, upd.Modified, upd.Doc (the new document, nil for Many).

if err == nil && upd.Matched == 0 {
	// Someone else wrote first, or the document is gone. Read again and retry.
}

// A guarded decrement: exactly one of two racing calls wins.
upd, err = calls.Update(ctx, "c1",
	ndb.Ops{"$inc": {"duration": -1}},
	ndb.Guard(ndb.Gte("duration", 1)),
)

// Create the document with this _id when it is missing.
_, err = calls.Update(ctx, "c9", ndb.Ops{"$inc": {"duration": 1}}, ndb.Upsert())

// Atomic update by query (POST /calls/_update_by_query): the first match, or
// every match with Many (the query needs the id or an indexed field).
_, err = calls.UpdateBy(ctx, ndb.Eq("status", "failed"), ndb.Ops{"$set": {"status": "retry"}}, ndb.Many())
_, err = calls.UpdateBy(ctx, ndb.Eq("caller", "z"), ndb.Ops{"$inc": {"duration": 1}}, ndb.Upsert())

// Delete by _id (DELETE /calls/_doc/c2), or by query. Both return how many they removed.
deleted, err := calls.Delete(ctx, "c2")
deleted, err = calls.DeleteBy(ctx, ndb.Eq("status", "failed"), ndb.Many())
```

- Operators: `$set`, `$setOnInsert` (like `$set`, but only when an upsert creates the document), `$unset`, `$inc`, `$push` (with `{"$each": [...]}`), `$pull`, `$min`, `$max`. Field names can be dot paths, as they are in the document (the tag names). A key of `Ops` without `$` is an error before anything is sent.
- Nothing matched is not an error: `Matched` is 0. `Modified` is 0 when the operators leave the document as it is.
- `Update` takes `Guard`, `Upsert` (creates the document with that `_id`) and `ExpireIn`. `UpdateBy` takes `Many`, `Upsert` (creates the document from the equality fields of the query) and `ExpireIn`. `DeleteBy` takes `Many`.
- `Many()` needs `_id` or an indexed field in the query (`ErrUnindexedMany`). An empty query is `ErrEmptyFilter`.
- `Filter` is one condition; join several with `ndb.And`. `ndb.Eq("_v", n)` matches the version.
- `Insert` and `InsertMany` fail with `ErrDuplicateID` when an `_id` exists. `InsertMany` writes nothing then.
- `Upsert(ctx, doc)` and `UpsertMany(ctx, docs)` create by `_id`, or merge into the stored document when the `_id` exists (every field of `T` is set), in one transaction, like `doc_as_upsert` in Elasticsearch.

### Truncate and drop

```go
err = calls.Truncate(ctx) // every document goes; the definition and indexes stay
err = calls.Drop(ctx)     // the collection, its definition and its watch consumers go
```

- Both take the same time for 10 or 10 million documents. Watch consumers get one event with `ndb.OpTruncate` (no ID), then the new changes.
- A direct-mode collection is `ErrDirectCollection`. A collection declared in `ndb.yml` comes back, empty, when the daemon restarts after a drop.

### Expiry (TTL like Redis)

Tag one field `expires` (a `time.Time`). The daemon then expires documents one by one.

```go
type Session struct {
	ID        string    `ndb:"_id"`
	ExpiresAt time.Time `ndb:"expires_at,expires"`
}

_, err = sessions.Insert(ctx, Session{ID: "s1"}, ndb.ExpireIn(time.Hour))
_, err = sessions.InsertMany(ctx, []Session{{ID: "s2"}, {ID: "s3"}}, ndb.ExpireIn(time.Hour))
_, err = sessions.Update(ctx, "s1", ndb.Ops{"$set": {"x": 1}}, ndb.ExpireIn(30*time.Minute))

_, err = sessions.Expire(ctx, "s1", 30*time.Minute)         // EXPIRE
_, err = sessions.ExpireAt(ctx, "s1", time.Now().Add(time.Hour))
left, expires, err := sessions.TTL(ctx, "s1")               // expires is false when it never expires
_, err = sessions.Persist(ctx, "s1")                        // PERSIST
```

- `Expire`, `ExpireAt` and `Persist` return the new version.
- `TTL` rounds up to whole seconds. A missing or expired document is `ErrNotFound`.
- `ErrTTLNotEnabled`: no `expires` field. `ErrTTLDerived`: the expiry comes from `ttl` + `time`.
- `ExpireIn` wins over the value of the `expires` field in the document.
- Disk space of expired documents returns after compaction, and fully only with `wal_retention`; see [Freeing disk](https://github.com/thadeu/ndb/blob/main/docs/dev/configuration.md#freeing-disk).

## Query

`Query` builds a search (`POST /{coll}/_search`). It sends nothing until a terminal: `All`, `First`, `Count` or `Each`.

```go
// Every document: Query with no filter.
all, err := calls.Query().All(ctx)

rows, err := calls.
	Query(
		ndb.Eq("status", "answered"),
		ndb.Gte("duration", 30),
		ndb.In("caller", "a", "b"),
		ndb.Not(ndb.Eq("caller", "zzz")),
		ndb.Or(ndb.Lt("duration", 5), ndb.Exists("note", false)),
	).
	Sort(ndb.Desc("at"), ndb.Asc("_id")).
	Fields("_id", "status").
	Limit(50).
	Skip(100).
	All(ctx)
```

- Filters: `Eq`, `Ne`, `Gt`, `Gte`, `Lt`, `Lte`, `In`, `Exists`, `Regex`, `ElemMatch`, `And`, `Or`, `Not`. The filters in one `Query` are ANDed. `q.Query(more...)` adds filters to a query. Dot paths such as `"agent.id"` work.
- `Regex(field, pattern, ndb.IgnoreCase, ...)` matches strings with Go RE2 (no backreferences, no look-around). An anchored prefix such as `^abc` can use an index. For other operators, see the [query language](https://github.com/thadeu/ndb/blob/main/docs/dev/query.md).
- The order of `Sort` keys is the order of the sort. The id is always the last key.
- `Fields` returns only these fields. The other struct fields keep their zero value.
- `Page(n, size)` sets `Skip` and `Limit` for page `n` (from 1).
- `First(ctx)` returns the first row, or `ErrNotFound`.
- The SDK has no explain call. To see the plan counters, send `"explain": true` to `POST /{coll}/_search` and read the `X-Ndb-Explain` header. See the query language doc.

### A query built elsewhere

A query from a search form or a saved search is a `RawQuery`. It is sent as is (the daemon validates it) and combines with other filters:

```go
f, err := ndb.RawQueryJSON([]byte(`{"status":"answered","at":{"$gte":"now-7d"}}`))
if err != nil {
	return err
}

rows, err := calls.Query(f, ndb.Ne("caller", "zzz")).Limit(50).All(ctx)

// The same from a map.
rows, err = calls.Query(ndb.RawQuery(map[string]any{"status": map[string]any{"$in": []any{"a", "b"}}})).All(ctx)
```

For a whole search body (`query`, `sort`, `limit`, `after`, `count`), use `Search`. It answers one page:

```go
res, err := calls.Search(ctx, []byte(`{"query":{"status":"answered"},"sort":{"at":-1},"limit":50,"count":true}`))
// res.Rows ([]Call), res.Next ("" on the last page), res.Total (with "count": true).
```

### Count

```go
n, err := calls.Count(ctx, ndb.Eq("status", "answered")) // POST /calls/_count
n, err = calls.Query(ndb.Eq("status", "answered")).Count(ctx)
total, err := calls.Count(ctx)
```

`Limit`, `Skip` and `Sort` do not change the count.

### Cursor pagination

Offset pages get slower as `Skip` grows. Use a cursor for deep pages.

```go
q := calls.Query(ndb.Eq("status", "answered")).Sort(ndb.Asc("_id")).Limit(100)
page, err := q.All(ctx)
token := q.NextCursor() // "" on the last page

next, err := calls.Query(ndb.Eq("status", "answered")).Sort(ndb.Asc("_id")).Limit(100).After(token).All(ctx)
```

A token works only with the same query and `Sort`. Another query shape returns `ErrCursorMismatch`.

To walk every row, use `Each`. It follows the cursor, with `Limit` as the batch size:

```go
for call, err := range calls.Query(ndb.Eq("status", "answered")).Sort(ndb.Asc("_id")).Limit(500).Each(ctx) {
	if err != nil {
		return err
	}

	fmt.Println(call.ID)
}
```

## Aggregate

The pipeline needs exactly one `$group`. Before it: `$match` and one `$sort`. After it: `$match`, `$sort`, `$limit`, `$skip`, `$project`. Accumulators: `$count`, `$sum`, `$avg`, `$min`, `$max`, `$first`, `$last`.

```go
res, err := calls.Aggregate(ctx, ndb.Pipeline{
	{"$match": map[string]any{"duration": map[string]any{"$gte": 1}}},
	{"$group": map[string]any{
		"_id": map[string]any{
			"status": "$status",
			"hour":   map[string]any{"$dateTrunc": map[string]any{"date": "$at", "unit": "hour"}},
		},
		"n":   map[string]any{"$count": map[string]any{}},
		"avg": map[string]any{"$avg": "$duration"},
	}},
	ndb.SortStage(ndb.Desc("n")),
}, ndb.AggLimit(100), ndb.AggCount())

// res.Rows is []map[string]any. Numbers are float64.
// res.Next is the cursor for the next page ("" on the last one).
// res.Total is set with AggCount.
```

Decode rows into your own type with `AggregateAs`:

```go
type Row struct {
	ID map[string]string `json:"_id"`
	N  int               `json:"n"`
}

typed, err := ndb.AggregateAs[Row](ctx, calls, pipeline)
```

Options: `AggLimit`, `AggSkip`, `AggAfter(token)`, `AggCount()`. `$dateTrunc` units are `minute`, `hour`, `day`, `week` and `month`. Use `ndb.SortStage` (not a Go map) when the sort has more than one key, so the key order stays.

## Watch

A watch streams the changes of a collection to a named consumer. First it sends the events after the last `Ack`. Then it sends new events as they happen.

```go
for ev, err := range calls.Watch(ctx, "relay", ndb.WatchQuery(ndb.Eq("status", "answered"))) {
	if errors.Is(err, ndb.ErrWatchLagged) {
		break // call Watch again. It continues after the last Ack.
	}

	if errors.Is(err, ndb.ErrHistoryTruncated) {
		return err // the saved position is too old. Start over.
	}

	if err != nil {
		return err
	}

	handle(ev.Op, ev.ID, ev.Doc) // ev.Op is ndb.OpPut, ndb.OpDel or ndb.OpTruncate

	if err := calls.Ack(ctx, ev); err != nil {
		return err
	}
}
```

- Delivery is at least once. After a restart an event can come again, so make the work safe to repeat.
- `Ack(ctx, ev)` stores the position of the event for its consumer (`POST /{coll}/_ack`). An older `Ack` is ignored.
- Options: `ndb.FromNow()` (a new consumer starts at the end), `ndb.Limit(n)` (stop after n events), `ndb.WatchQuery(filters...)`.
- A delete event has no document (`ev.Doc` is the zero value). With `WatchQuery`, a delete is sent only if the same stream saw an earlier matching put of that id.
- `ErrWatchLagged` means the consumer was too slow. `ErrHistoryTruncated` means the WAL files for its position were deleted (`wal_retention`).
- The stream ends when `ctx` is cancelled.

## Streams

A stream is an append-only log with consumer groups, with the behavior of
Redis Streams (see [Streams](../../docs/dev/streams.md)). `T` is a struct with
`ndb` tags or `map[string]any`.

```go
orders, err := ndb.StreamOf[Order](db, "orders")

id, err := orders.Append(ctx, Order{Order: 42, Total: 99.9})       // "1731164400000-0"
ids, err := orders.AppendMany(ctx, []Order{{Order: 43}, {Order: 44}}) // one fsync
_, err = orders.Append(ctx, o, ndb.WithTrim(ndb.MaxLen(10_000, true)))

entries, err := orders.Range(ctx, "-", "+", 100) // XRANGE; entries[0].ID, .Time, .Doc
info, err := orders.Info(ctx)                    // length, first and last ID, groups
```

A worker in a group. `Work` reads its own pending entries first, then new
ones (waiting up to `Block`), acks each entry the handler did without an
error, and claims the entries other workers left idle. An entry delivered
`MaxDeliveries` times goes to the dead-letter stream:

```go
g := orders.Group("billing")

if err := g.Ensure(ctx, ndb.StartNew); err != nil { // or ndb.StartAll
	return err
}

err = g.Work(ctx, "worker-1", func(ctx context.Context, e ndb.Entry[Order]) error {
	return charge(ctx, e.Doc) // nil acks; an error leaves it pending
}, ndb.Work{
	Count:         100,
	ClaimIdle:     time.Minute,
	MaxDeliveries: 5,
	DeadLetter:    "orders_dead",
	OnError:       func(err error) { log.Println(err) },
})
```

`Work` returns when `ctx` ends; a failed read goes to `OnError` and `Work`
tries again. Delivery is at least once: make the work safe to repeat. Run many
workers with different consumer names to share the work. An entry whose doc
does not decode into `T` comes with `e.Err` set (and `e.Raw()`), not as an
error of the read; `Work` counts it as a handler failure, so it goes to the
dead letter after `MaxDeliveries`. A handler error while `ctx` ends (shutdown)
leaves the entry pending: it is not a failure. An entry that had `MaxDeliveries`
deliveries without an ack (it crashed the process) goes to the dead letter at
the next start, without the handler.

A delayed retry (ndb only): with `Retry`, `Work` nacks a failed entry with a
wait that can grow with the deliveries, and any worker of the group takes it
back when the wait ends. The entry has no owner while it waits, so a restart
does not take it early.

```go
ndb.Work{
	Retry: ndb.TierSteps( // 15 s, 4 × 1 min, 6 × 10 min, 12 × 1 h, then 6 h
		ndb.Step{Wait: 15 * time.Second, UpTo: 1},
		ndb.Step{Wait: time.Minute, UpTo: 5},
		ndb.Step{Wait: 10 * time.Minute, UpTo: 11},
		ndb.Step{Wait: time.Hour, UpTo: 23},
		ndb.Step{Wait: 6 * time.Hour},
	),
	MaxAge:     15 * 24 * time.Hour,
	DeadLetter: "orders_dead",
}
```

| Field | Default | Meaning |
|---|---|---|
| `Count` | 10 | The most entries per read. |
| `Block` | 5 s | How long a read waits for new entries. |
| `ClaimIdle` | 1 min | Claim the entries pending this long (not the nacked ones that still wait). Negative: off. |
| `ClaimEvery` | `ClaimIdle / 2` (at least 1 s) | How often `Work` looks for idle entries. |
| `MaxDeliveries` | 0 (no limit) | A failure on this delivery sends the entry to `DeadLetter` (reason `max_deliveries`), also with `Retry`. |
| `DeadLetter` | none | The stream of `ndb.DeadEntry` (`Stream`, `Group`, `ID`, `Deliveries`, `Reason`, `Doc`). |
| `Retry` | nil | `func(deliveries int64) time.Duration`: on a handler failure, nack with this wait. `nil`: the entry stays pending for `ClaimIdle`. `ndb.Tiers(waits...)`: the wait of try n is `waits[min(n, len)-1]`. `ndb.TierSteps(ndb.Step{Wait, UpTo}...)`: the first step with `n <= UpTo` (0: no bound); the last step repeats. |
| `MaxAge` | 0 (no limit) | An entry older than this (by its ID time) goes to `DeadLetter` with reason `max_age`, without the handler, and is acked. A failure past `MaxAge` is not nacked: it goes to `DeadLetter` at once. |
| `DueEvery` | 1 s with `Retry`, else off | How often `Work` claims the nacked entries whose wait ended (`ClaimDue`). Negative: off. |
| `OnError` | none | Gets the errors that do not stop `Work`. |

The calls one by one, when you need your own loop:

| Redis | Go |
|---|---|
| `XGROUP CREATE` | `g.Create(ctx, ndb.StartNew)` / `g.Ensure` |
| `XREADGROUP ... >` | `g.Read(ctx, "w1", ndb.Read{Count: 100, Block: 5 * time.Second})` |
| `XREADGROUP ... 0` | `g.Read(ctx, "w1", ndb.Read{Pending: true})` |
| `XACK` | `g.Ack(ctx, ids...)` |
| `XPENDING` | `g.Pending(ctx)` / `g.PendingList(ctx, ndb.PendingQuery{Idle: time.Minute})` |
| `XCLAIM` / `XAUTOCLAIM` | `g.Claim(ctx, "w2", time.Minute, ids...)` / `g.AutoClaim(ctx, "w2", time.Minute, "0-0", 100)` |
| `XCLAIM` / `XAUTOCLAIM ... JUSTID` | `g.ClaimIDs(ctx, "w2", time.Minute, ids...)` / `g.AutoClaimIDs(ctx, "w2", time.Minute, "0-0", 100)` |
| `XREAD` | `orders.Tail(ctx, ndb.Tail{After: lastID, Count: 100, Block: 5 * time.Second})` |
| `XGROUP CREATE` without `MKSTREAM` | `g.Create(ctx, ndb.StartNew, ndb.GroupNoCreate())` |
| `XTRIM` | `orders.Trim(ctx, ndb.MaxLen(1000, false))` |
| `XDEL` | `orders.Delete(ctx, id)` |
| `XINFO GROUPS` / `CONSUMERS` | `orders.Groups(ctx)` / `g.Consumers(ctx)` |
| ndb only: delayed retry | `g.Nack(ctx, "w1", time.Minute, ids...)` / `g.ClaimDue(ctx, "w2", "0-0", 100)` / `ndb.PendingQuery{Due: &yes}` (`PendingEntry.RetryAt`, `PendingSummary.Waiting`) |

A blocked `Read` or `Tail` does not count against `WithMaxConns`.

`Tail` reads without a group: it changes no group and no pending list. `After`
is an ID (default `"0"`, the start) or `ndb.StartNew` (`$`): only the entries
appended after the call starts. To read on, send the ID of the last entry as
`After`. In `Range`, `RevRange` and `PendingList`, `"(id"` leaves that ID out,
as in Redis; an ID with only the ms is `<ms>-0` as a start and `<ms>-<max>` as
an end. A bad ID returns `ndb.ErrInvalidStreamID`.

### The commands of go-redis

An app that uses Redis Streams with go-redis can keep its calls: the module
[`ndbredis`](ndbredis/README.md) has the stream methods of go-redis v9
(`XAdd`, `XReadGroup`, `XAck`, ...) on ndb streams. It is a module of its own,
so this SDK stays without dependencies.

```go
streams := ndbredis.New(db) // a redis.StreamCmdable
```

## Blobs

Large values live in the blob store. The document keeps only a reference. The same content always gives the same reference.

```go
blob, err := db.PutBlob(ctx, file, "audio/wav") // file is an io.Reader (POST /_blobs)

_, err = calls.Update(ctx, "c1", ndb.Ops{"$set": {"recording": blob}})

call, err := calls.Get(ctx, "c1") // call.Recording is *ndb.Blob

rc, err := db.GetBlob(ctx, *call.Recording) // GET /_blobs/{ref}
if err != nil {
	return err
}

defer rc.Close()

_, err = io.Copy(dst, rc)
```

A struct field of type `ndb.Blob` or `*ndb.Blob` needs no special tag. `Blob` has `Ref`, `Size` and `Type`.

## Admin

```go
stats, err := calls.Stats(ctx)            // rows, indexes, files, bytes (GET /calls/_stats)
_, err = calls.Compact(ctx)                // merge every segment now (POST /calls/_compact)
indexes, err := calls.EnsureIndex(ctx, "caller") // built in the background (PUT /calls/_indexes/caller)
indexes, err = calls.DropIndex(ctx, "caller")

defs, err := db.Collections(ctx) // one definition per collection (GET /_collections)
all, err := db.Stats(ctx)        // every collection, totals, version, uptime (GET /_stats)
size, err := db.Size(ctx)        // disk use per collection and part (GET /_size)
```

## Errors

The daemon errors are `*ndb.Error` with `Code`, `Message` and `Status` (the HTTP status). Test them with `errors.Is` and a sentinel. Use `errors.As` to read the fields.

```go
if errors.Is(err, ndb.ErrNotFound) {
}

var apiErr *ndb.Error

if errors.As(err, &apiErr) {
	log.Println(apiErr.Code, apiErr.Status, apiErr.Message)
}
```

| Sentinel | Code |
|---|---|
| `ErrNotFound` | `not_found` |
| `ErrDuplicateID` | `duplicate_id` |
| `ErrUniqueViolation` | `unique_violation` |
| `ErrCatalogConflict` | `catalog_conflict` |
| `ErrCatalogInvalid` | `catalog_invalid` |
| `ErrUnknownCollection` | `unknown_collection` |
| `ErrDirectCollection` | `direct_collection` |
| `ErrInvalidQuery` | `invalid_query` |
| `ErrInvalidPipeline` | `invalid_pipeline` |
| `ErrGroupLimit` | `group_limit_exceeded` |
| `ErrCursorInvalid`, `ErrCursorMismatch`, `ErrCursorExpired` | `cursor_invalid`, `cursor_mismatch`, `cursor_expired` |
| `ErrRequestTooLarge`, `ErrLineTooLarge` | `request_too_large`, `line_too_large` |
| `ErrUnauthorized` | `unauthorized` |
| `ErrUnknownOperator`, `ErrNoUpdate`, `ErrConflictingUpdate`, `ErrInvalidField`, `ErrUnindexedMany`, `ErrEmptyFilter` | `unknown_operator`, `no_update`, `conflicting_update`, `invalid_field`, `unindexed_many`, `empty_filter` |
| `ErrConfirmRequired` | `confirm_required` |
| `ErrNotReady` | `not_ready` |
| `ErrClosed` | `closed` |
| `ErrWatchLagged` | `watch_lagged` |
| `ErrInvalidPosition` | `invalid_position` |
| `ErrHistoryTruncated` | `history_truncated` |
| `ErrTTLNotEnabled`, `ErrTTLDerived` | `ttl_not_enabled`, `ttl_derived` |

Other codes (for example `invalid_request`, `unknown_route`, `pattern_read_only`) are still `*ndb.Error`, but have no sentinel. A response that is not a daemon error has the code `http_status`. A failed connection returns the normal `net` error.

## Testing

The tests of this module build the daemon from the repository (`go build ./cmd/ndb`) in `TestMain`. They start it on a temp unix socket and a free TCP port, with a temp data dir, and stop it at the end:

```sh
ndb -config /nonexistent.yml -data-dir "$(mktemp -d)" -unix /tmp/ndb-test.sock -tcp 127.0.0.1:7070
```

Add `-token-file <path>` to ask for a bearer token on TCP. Use a short socket path: macOS allows 104 bytes. Use a new collection name for each test.

## License

MIT.
