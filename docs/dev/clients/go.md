# Go

The Go module `github.com/thadeu/ndb/sdk/go`. Standard library only.

The source and the same guide are in [`sdk/go`](https://github.com/thadeu/ndb/tree/main/sdk/go).

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
```

- `Open` does not connect. The first call does.
- The address must be `unix:///path`, `tcp://host:port`, or `https://host[:port]` (ndb behind a TLS proxy; `http://` works too). Anything else returns an error.
- The token goes only in the `Authorization` header. A unix socket does not need it.
- `WithReadyTimeout` sets how long a call retries when the daemon is not ready (HTTP 503 `not_ready`, or the socket is missing or refuses). The default is `ndb.DefaultReadyTimeout` (10 s). Zero turns the retry off. Writes are retried only when the daemon certainly did not process them. Reads are also retried after a dropped connection.
- The pool opens at most `ndb.DefaultMaxConns` (64) connections. Over that, a call waits for a free one (until its `ctx` ends) instead of opening another. `ndb.WithMaxConns(n)` changes the limit (0 is no limit); `ndb.WithIdleTimeout(d)` sets how long an unused connection stays open (90 s). A `Watch` has its own connections, out of the limit.
- Without a limit, a peak of goroutines opens hundreds of connections at once. Measured on a Mac, 1000 goroutines of 20 counts each over the unix socket: no limit gives p99 35 ms and max 137 ms; the limit of 64 gives p99 9.5 ms and max 12 ms. Over TCP with no limit, some calls failed. In a Docker network (the fsw-esl test), the limit took the peaks of 1 s and 4 s away. Measure it with `NDB_POOL_BENCH=1 go test -run TestPoolBench -v .` in `sdk/go`.
- Use the unix socket when the app and ndb share a host or a pod: it was 2x faster than TCP in the same test.
- `*DB` is safe for concurrent use. `Close` closes idle connections. The handle stays usable.

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
- `Collection` sends nothing. The first call on the collection sends `ensure_collection` (`PUT /calls`) once per name on one `*DB`. A name with `*` (a [pattern](#collection-patterns)) never declares. A definition that disagrees with the stored one (time, ttl) returns `ErrCatalogConflict`.

## Insert, get, update, delete

```go
// Insert one document (POST /calls/_doc), or many in one transaction (_bulk).
res, err := calls.Insert(ctx, Call{ID: "c1", Status: "answered", Caller: "a", At: time.Now().UTC()})
// res.ID and res.V (the new version).

results, err := calls.InsertMany(ctx, []Call{
	{ID: "c2", Status: "failed", Caller: "b", At: time.Now().UTC()},
	{ID: "c3", Status: "answered", Caller: "c", At: time.Now().UTC()},
})

// Get by _id (GET /calls/_doc/c1). A missing id returns ErrNotFound.
call, err := calls.Get(ctx, "c1")

// Update one document by _id, in one atomic step. Guard adds conditions on the
// same document: a _v guard is an optimistic lock.
upd, err := calls.Update(ctx, "c1",
	ndb.Ops{"$set": {"status": "done"}, "$unset": {"duration": true}},
	ndb.Guard(ndb.Eq("_v", call.Version)),
)
// upd.Matched, upd.Modified, upd.Doc (the new document).

if err == nil && upd.Matched == 0 {
	// Someone else wrote first, or the document is gone. Read again and retry.
}

// Atomic update by query: exactly one of two racing calls wins.
upd, err = calls.UpdateBy(ctx,
	ndb.And(ndb.Eq("_id", "c1"), ndb.Gte("duration", 1)),
	ndb.Ops{"$inc": {"duration": -1}},
)

// Every match (the query needs the id or an indexed field), or create it.
_, err = calls.UpdateBy(ctx, ndb.Eq("status", "failed"), ndb.Ops{"$set": {"status": "retry"}}, ndb.Many())
_, err = calls.Update(ctx, "c9", ndb.Ops{"$inc": {"duration": 1}}, ndb.Upsert())

// Delete by _id, or by query. Both return how many they removed.
deleted, err := calls.Delete(ctx, "c2")
deleted, err = calls.DeleteBy(ctx, ndb.Eq("status", "failed"), ndb.Many())
```

- Operators: `$set`, `$setOnInsert` (like `$set`, but only when an upsert creates the document), `$unset`, `$inc`, `$push` (with `{"$each": [...]}`), `$pull`, `$min`, `$max`. Field names can be dot paths, as they are in the document (the tag names). A key of `Ops` without `$` is an error before anything is sent.
- Nothing matched is not an error: `Matched` is 0. `Modified` is 0 when the operators leave the document as it is.
- `Update` (by `_id`) takes `Guard`, `Upsert()` (creates the document with that `_id`) and `ExpireIn`. `UpdateBy` takes `Many()`, `Upsert()` and `ExpireIn`.
- `Many()` needs `_id` or an indexed field in the query (`ErrUnindexedMany`). `Upsert()` on `UpdateBy` creates the document from the equality fields of the query.
- A `Filter` is one condition; join several with `ndb.And`. `ndb.Eq("_v", n)` matches the version.
- `Insert` and `InsertMany` fail with `ErrDuplicateID` when an `_id` exists.
- `Upsert(ctx, doc)` and `UpsertMany(ctx, docs)` create by `_id`, or merge into the stored document when the `_id` exists, in one transaction. See [Upsert](../http/documents.md#upsert).

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
- Disk space of expired documents returns after compaction, and fully only with `wal_retention`; see [Freeing disk](../configuration.md#freeing-disk).

## Query

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

- Filters: `Eq`, `Ne`, `Gt`, `Gte`, `Lt`, `Lte`, `In`, `Exists`, `Regex`, `ElemMatch`, `And`, `Or`, `Not`, `RawQuery`. The filters in one `Query` are ANDed. Dot paths such as `"agent.id"` work.
- `ElemMatch(field, filters...)` matches when one element of an array matches every filter: `ElemMatch("items", Eq("sku", "a"), Gt("qty", 2))`. For an array of scalars, use an empty field name: `ElemMatch("scores", Gte("", 10), Lt("", 20))`.
- On the time field, a string that starts with `now` is [date math](../query.md#date-math): `Gte("ts", "now-7d/d")`.
- `Regex(field, pattern, ndb.IgnoreCase, ...)` matches strings with Go RE2 (no backreferences, no look-around). An anchored prefix such as `^abc` can use an index. For other operators, see the [query language](../query.md).
- The order of `Sort` keys is the order of the sort. The id is always the last key.
- `Fields` returns only these fields. The other struct fields keep their zero value.
- `Page(n, size)` sets `Skip` and `Limit` for page `n` (from 1).
- `First(ctx)` returns the first row, or `ErrNotFound`.
- The SDK has no explain call. To see the plan counters, send `"explain": true` in a raw `find` request and read the `X-Ndb-Explain` header. See the query language doc.

### Queries built elsewhere

For a search form or a saved search, take the JSON as is. `ndb.RawQueryJSON` (or `ndb.RawQuery` with a map) is a `Filter` that mixes with the others; `Search` takes a whole search body and returns one page.

```go
form, err := ndb.RawQueryJSON([]byte(`{"status":{"$in":["answered","failed"]},"ts":{"$gte":"now-7d"}}`))
rows, err := calls.Query(form, ndb.Eq("dnis", "1000")).Sort(ndb.Desc("ts")).All(ctx)

page, err := calls.Search(ctx, []byte(`{"query":{"status":"answered"},"sort":{"ts":-1},"limit":50,"count":true}`))
page.Rows   // []Call
page.Next   // cursor for {"after": ...}; "" on the last page
page.Total  // with "count": true
```

Rows decode like any other query. The daemon validates the JSON (`invalid_query`), but it takes any field: for a public form, keep an allowlist of fields and operators, cap `limit`, and ask for an indexed field or a time window, as you would before sending a form to Elasticsearch `_search`.

### Collection patterns

A collection name with `*` at the end reads every collection whose name starts with the prefix (see [Collection patterns](../http/queries.md#collection-patterns)). It declares nothing; writes fail with `pattern_read_only`. A field tagged `ndb:"_coll"` gets the name of the collection of each row, and is never sent on writes.

```go
type Log struct {
	ID   string    `ndb:"_id"`
	At   time.Time `ndb:"at,time"`
	Lvl  string    `ndb:"lvl,index"`
	Coll string    `ndb:"_coll"`
}

logs, err := ndb.Collection[Log](db, "logs-2026-*")

rows, err := logs.Query(ndb.Eq("lvl", "error")).Sort(ndb.Desc("at")).Limit(50).All(ctx)
n, err := logs.Count(ctx, ndb.Eq("lvl", "error"))
```

### Count

```go
n, err := calls.Count(ctx, ndb.Eq("status", "answered")) // POST /calls/_count
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

A token works only with the same `Query` and `Sort`. Another query shape returns `ErrCursorMismatch`.

To walk every row, use `Iter`. It follows the cursor, with `Limit` as the batch size:

```go
for call, err := range calls.Query(ndb.Eq("status", "answered")).Sort(ndb.Asc("_id")).Limit(500).Each(ctx) {
	if err != nil {
		return err
	}

	fmt.Println(call.ID)
}
```

## Lookup

`lookup` is a key of the HTTP search (`POST /{coll}/_search`): in the same call, the daemon reads the documents of other collections that each row points to, and puts them in the row. The HTTP API is the primitive and has every rule ([Lookup](../query.md#lookup)); this SDK only builds that JSON.

| Relation | The foreign key lives | Match | The row gets |
|---|---|---|---|
| belongs to | in **this** collection | `row.fk == other.pk` | an object, or null |
| has many | in the **other** collection | `other.fk == row.pk` | an array (100 unless a limit, at most 1000) |
| has one | in the **other** collection | `other.fk == row.pk` | an object, or null |

`pk` is `_id` unless you say otherwise. The field that is searched on the other side (its `pk` for a belongs to, its `fk` for a has many) must be `_id` or indexed, else the daemon answers `lookup_unindexed`.

### Basic use

Give the struct a field for each relation, with the `lookup` tag option: a read fills it, a write leaves it out. Its type decodes with `encoding/json` (json tags).

```go
type Customer struct {
	ID   string `json:"_id"`
	Name string `json:"name"`
}

type Item struct {
	ID      string `json:"_id"`
	OrderID string `json:"order_id"`
	SKU     string `json:"sku"`
}

type Order struct {
	ID         string    `ndb:"_id"`
	CustomerID string    `ndb:"customer_id"`
	Customer   *Customer `ndb:"customer,lookup"`
	Items      []Item    `ndb:"items,lookup"`
}

orders, _ := ndb.Collection[Order](db, "orders")

// belongs to: orders.customer_id -> customers._id
rows, err := orders.Query(ndb.Eq("status", "paid")).
	Lookup("customer", ndb.BelongsTo("customers", "customer_id")).
	All(ctx)

rows[0].Customer.Name // "Ana" (nil when there is no customer)

// has many: order_items.order_id -> orders._id
rows, err = orders.Query().
	Lookup("items", ndb.HasMany("order_items", "order_id")).
	All(ctx)

// has one: invoices.order_id -> orders._id
rows, err = orders.Query().
	Lookup("invoice", ndb.HasOne("invoices", "order_id")).
	All(ctx)
```

### Options

Each method returns a new `Relation`:

```go
ndb.HasMany("order_items", "order_id").
	Query(ndb.Gt("qty", 0)).       // filter the related documents
	Sort(ndb.Asc("pos")).          // their order
	Limit(50).                     // per row, 1 to 1000
	Fields("sku", "qty").          // keep these fields
	Lookup("product",              // nested, two levels at most
		ndb.BelongsTo("products", "sku").PK("code")) // products.code instead of _id
```

`Query`, `Sort` and `Limit` are for `HasMany` and `HasOne`. The JSON it sends:

```json
{ "lookup": { "items": { "has_many": "order_items", "fk": "order_id", "query": { "qty": { "$gt": 0 } },
  "sort": { "pos": 1 }, "limit": 50, "fields": ["sku", "qty"],
  "lookup": { "product": { "belongs_to": "products", "fk": "sku", "pk": "code" } } } } }
```

### Index

A `HasMany("order_items", "order_id")` needs an index on `order_items.order_id`: tag it in the struct of that collection (`ndb:"order_id,index"`) or call `items.EnsureIndex(ctx, "order_id")`.

### Errors

`ErrLookupInvalid` (a bad relation), `ErrLookupUnindexed` (the other side has no index; the message gives the route), `ErrLookupTooLarge` (more than 10 000 documents in one answer), `ErrUnknownCollection`.

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
- `Ack` stores the position of the event. An older `Ack` is ignored.
- Options: `ndb.FromNow()` (a new consumer starts at the end), `ndb.Limit(n)` (stop after n events), `ndb.WatchQuery(filters...)`.
- A delete event has no document (`ev.Doc` is the zero value). With `WatchQuery`, a delete is sent only if the same stream saw an earlier matching put of that id.
- `ErrWatchLagged` means the consumer was too slow. `ErrHistoryTruncated` means the WAL files for its position were deleted (`wal_retention`).
- The stream ends when `ctx` is cancelled.

## Admin

```go
err := db.Health(ctx)                  // nil when ready (GET /_health)
defs, err := db.Collections(ctx)       // the catalog (GET /_collections)
stats, err := db.Stats(ctx)            // every collection and totals (GET /_stats)
size, err := db.Size(ctx)              // disk use (GET /_size)

stats, err = calls.Stats(ctx)          // GET /calls/_stats
report, err := calls.Compact(ctx)      // POST /calls/_compact
fields, err := calls.EnsureIndex(ctx, "dnis") // PUT /calls/_indexes/dnis
fields, err = calls.DropIndex(ctx, "dnis")
```

The answers are the JSON of the [HTTP routes](../http/system.md), as `map[string]any`.

## Blobs

Large values live in the blob store. The document keeps only a reference. The same content always gives the same reference.

```go
blob, err := db.PutBlob(ctx, file, "audio/wav") // file is an io.Reader

_, err = calls.Update(ctx, "c1", ndb.Ops{"$set": {"recording": blob}})

call, err := calls.Get(ctx, "c1") // call.Recording is *ndb.Blob

rc, err := db.GetBlob(ctx, *call.Recording)
if err != nil {
	return err
}

defer rc.Close()

_, err = io.Copy(dst, rc)
```

A struct field of type `ndb.Blob` or `*ndb.Blob` needs no special tag. `Blob` has `Ref`, `Size` and `Type`.

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

Each code of the [error table](../http/errors.md) has a sentinel: `ndb.Err` and the code in CamelCase, for example `ErrNotFound` (`not_found`), `ErrInvalidPath` (`invalid_path`), `ErrUniqueViolation` (`unique_violation`). The exceptions are `ErrGroupLimit` (`group_limit_exceeded`), `ErrTTLNotEnabled` and `ErrTTLDerived`, and `ErrPITROff`. A test of the repository fails when the daemon gets a code that has no sentinel.

A response that is not a daemon error has the code `http_status`. A failed connection returns the normal `net` error.

## Testing

ndb has no transactions, so a test cannot roll back. `ndb.Testing.Clean` truncates the collections that the `*DB` used since the last clean (a few ms at any size; the definition and the indexes stay), like `Ndb::Testing` in Rails:

```go
func TestMain(m *testing.M) {
	code := m.Run()

	ndb.Testing.Clean(context.Background(), db)
	os.Exit(code)
}

// or after each test
t.Cleanup(func() { ndb.Testing.Clean(ctx, db) })
```

- `ndb.CleanOnly(names...)` truncates these, not the ones used; `ndb.CleanExcept(names...)` leaves some out.
- `ndb.Testing.CleanAll(ctx, db)` truncates every engine collection with the prefix of the DB, for the start of a run. Direct collections and partitions are left out.
- `ndb.WithPrefix("test1_")` on `Connect` puts a prefix before the name of every `Collection`: each parallel package or worker gets collections of its own.
- A collection that does not exist is not an error.
- A clean runs only under `go test` (`testing.Testing()`). Anywhere else it returns `ndb.ErrUnsafeClean`, so it never truncates production data. `NDB_ALLOW_CLEAN=1` forces it.
- The SDK does not start a daemon: start it in CI as a service, with `make up`, or with Docker, and connect with `NDB_URL`.

### Tests of this module

The tests of this module build the daemon from the repository (`go build ./cmd/ndb`) in `TestMain`. They start it on a temp unix socket and a free TCP port, with a temp data dir, and stop it at the end:

```sh
ndb -config /nonexistent.yml -data-dir "$(mktemp -d)" -unix /tmp/ndb-test.sock -tcp 127.0.0.1:7070
```

Add `-token-file <path>` to ask for a bearer token on TCP. Use a short socket path: macOS allows 104 bytes. Use a new collection name for each test.
