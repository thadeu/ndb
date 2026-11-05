# ndb Go SDK

<img src="https://raw.githubusercontent.com/thadeu/ndb/main/brand/png/lockup-on-light.png" width="220">

Go client for [ndb](https://github.com/thadeu/ndb), an NDJSON query daemon. It uses the standard library only. The SDK builds JSON and reads NDJSON. The daemon decides what every query means.

Docs: [client guide](https://github.com/thadeu/ndb/blob/main/docs/dev/clients.md#go-module-githubcomthadeundbsdkgo), [query language](https://github.com/thadeu/ndb/blob/main/docs/dev/query.md), [HTTP API](https://github.com/thadeu/ndb/blob/main/docs/dev/http-api.md).

## Install

```sh
go get github.com/thadeu/ndb/sdk/go@v0.1.0
```

```go
import ndb "github.com/thadeu/ndb/sdk/go"
```

The module needs a running ndb daemon. See the repository for how to run one. Releases of this module are tagged `sdk/go/vX.Y.Z`.

## Connect

```go
// Unix socket.
db, err := ndb.Open("unix:///run/ndb/ndb.sock")

// TCP with a bearer token.
db, err := ndb.Open("tcp://10.0.0.5:7070", ndb.WithToken("secret"))

// Address and token from the environment: NDB_URL and NDB_TOKEN.
db, err := ndb.Open("")

// Retry for up to 5 s while the daemon starts or recovers.
db, err := ndb.Open("unix:///run/ndb/ndb.sock", ndb.WithReadyTimeout(5*time.Second))

defer db.Close()
```

- `Open` does not connect. The first call does.
- The address must be `unix:///path` or `tcp://host:port`. Anything else returns an error.
- The token goes only in the `Authorization` header. A unix socket does not need it.
- `WithReadyTimeout` sets how long a call retries when the daemon is not ready (HTTP 503 `not_ready`, or the socket is missing or refuses). The default is `ndb.DefaultReadyTimeout` (10 s). Zero turns the retry off. Writes are retried only when the daemon certainly did not process them. Reads are also retried after a dropped connection.
- The pool keeps up to 16 idle connections per host. There is no setting for it.
- `*DB` is safe for concurrent use. `Close` closes idle connections. The handle stays usable.

## Define a collection

A collection maps to a Go struct. Tags use the form `ndb:"name,option,option"`.

```go
type Call struct {
	CallID    string    `ndb:"call_id,id"`
	Status    string    `ndb:"status,index"`
	Caller    string    `ndb:"caller,unique"`
	Duration  int       `ndb:"duration"`
	At        time.Time `ndb:"at,time"`
	ExpiresAt time.Time `ndb:"expires_at,expires"`
	Recording *ndb.Blob `ndb:"recording"`
	Version   uint64    `ndb:"_v,version"`
	Internal  string    `ndb:"-"`
}

calls, err := ndb.Collection[Call](ctx, db, "calls")
```

| Option | Meaning |
|---|---|
| `id` | The document id. Without it the id field is `_id`. If the field is empty on insert, the daemon makes an id. |
| `index` | Index this field. |
| `unique` | Unique value. A second document with it fails with `ErrUniqueViolation`. |
| `time` | The collection time field. Used by zone maps, `$dateTrunc` and TTL. |
| `expires` | The field holds the time the document expires. |
| `version` | The field receives `_v`, the document version, on every read. It is never sent on a write. |

- A field without an `ndb` tag uses its `json` name, or its Go name. `ndb:"-"` or `json:"-"` skips a field.
- Only one `id`, `time` and `expires` field are allowed. An unknown option is an error.
- `Collection` sends `ensure_collection` the first time for a name on one `*DB`. Later calls send nothing. A definition that disagrees with the stored one (id, time) returns `ErrCatalogConflict`.

## Insert, get, update, delete

```go
// Insert. All documents go in one transaction.
results, err := calls.Insert(ctx,
	Call{CallID: "c1", Status: "answered", Caller: "a", At: time.Now().UTC()},
	Call{CallID: "c2", Status: "failed", Caller: "b", At: time.Now().UTC()},
)
// results[i].ID and results[i].V (the new version).

// Get by id. A missing id returns ErrNotFound.
call, err := calls.Get(ctx, "c1")

// Update with optimistic locking.
v, err := calls.Update(ctx, "c1", map[string]any{"status": "done"}, ndb.IfVersion(call.Version))

if errors.Is(err, ndb.ErrVersionConflict) {
	// Someone else wrote first. Read again and retry.
}

// Remove a field.
_, err = calls.Update(ctx, "c1", nil, ndb.Unset("duration"))

// Delete. IfVersion is optional here too.
err = calls.Delete(ctx, "c1", ndb.IfVersion(v))
```

- `Update` returns the new version. Without `IfVersion` there is no check.
- `Insert` fails with `ErrDuplicateID` when the id exists.
- `Update` takes field names as they are in the document (the tag names).

## Query

```go
rows, err := calls.
	Where(
		ndb.Eq("status", "answered"),
		ndb.Gte("duration", 30),
		ndb.In("caller", "a", "b"),
		ndb.Not(ndb.Eq("caller", "zzz")),
		ndb.Or(ndb.Lt("duration", 5), ndb.Exists("note", false)),
	).
	Sort(ndb.Desc("at"), ndb.Asc("call_id")).
	Fields("call_id", "status").
	Limit(50).
	Skip(100).
	All(ctx)
```

- Filters: `Eq`, `Ne`, `Gt`, `Gte`, `Lt`, `Lte`, `In`, `Exists`, `Regex`, `Or`, `Not`. The filters in one `Where` are ANDed. Dot paths such as `"agent.id"` work.
- `Regex(field, pattern, ndb.IgnoreCase, ...)` matches strings with Go RE2 (no backreferences, no look-around). An anchored prefix such as `^abc` can use an index. For other operators, see the [query language](https://github.com/thadeu/ndb/blob/main/docs/dev/query.md).
- The order of `Sort` keys is the order of the sort. The id is always the last key.
- `Fields` returns only these fields. The other struct fields keep their zero value.
- `Page(n, size)` sets `Skip` and `Limit` for page `n` (from 1).
- `First(ctx)` returns the first row, or `ErrNotFound`.
- The SDK has no explain call. To see the plan counters, send `"explain": true` in a raw `find` request and read the `X-Ndb-Explain` header. See the query language doc.

### Count

```go
n, err := calls.Where(ndb.Eq("status", "answered")).Count(ctx)
```

`Limit`, `Skip` and `Sort` do not change the count.

### Cursor pagination

Offset pages get slower as `Skip` grows. Use a cursor for deep pages.

```go
q := calls.Where(ndb.Eq("status", "answered")).Sort(ndb.Asc("call_id")).Limit(100)
page, err := q.All(ctx)
token := q.NextCursor() // "" on the last page

next, err := calls.Where(ndb.Eq("status", "answered")).Sort(ndb.Asc("call_id")).Limit(100).After(token).All(ctx)
```

A token works only with the same `Where` and `Sort`. Another query shape returns `ErrCursorMismatch`.

To walk every row, use `Iter`. It follows the cursor, with `Limit` as the batch size:

```go
for call, err := range calls.Where(ndb.Eq("status", "answered")).Sort(ndb.Asc("call_id")).Limit(500).Iter(ctx) {
	if err != nil {
		return err
	}

	fmt.Println(call.CallID)
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
for ev, err := range calls.Watch(ctx, "relay", ndb.WatchWhere(ndb.Eq("status", "answered"))) {
	if errors.Is(err, ndb.ErrWatchLagged) {
		break // call Watch again. It continues after the last Ack.
	}

	if errors.Is(err, ndb.ErrHistoryTruncated) {
		return err // the saved position is too old. Start over.
	}

	if err != nil {
		return err
	}

	handle(ev.Op, ev.ID, ev.Doc) // ev.Op is ndb.OpPut or ndb.OpDel

	if err := calls.Ack(ctx, ev); err != nil {
		return err
	}
}
```

- Delivery is at least once. After a restart an event can come again, so make the work safe to repeat.
- `Ack` stores the position of the event. An older `Ack` is ignored.
- Options: `ndb.FromNow()` (a new consumer starts at the end), `ndb.Limit(n)` (stop after n events), `ndb.WatchWhere(filters...)`.
- A delete event has no document (`ev.Doc` is the zero value). With `WatchWhere`, a delete is sent only if the same stream saw an earlier matching put of that id.
- `ErrWatchLagged` means the consumer was too slow. `ErrHistoryTruncated` means the WAL files for its position were deleted (`wal_retention`).
- The stream ends when `ctx` is cancelled.

## Blobs

Large values live in the blob store. The document keeps only a reference. The same content always gives the same reference.

```go
blob, err := db.PutBlob(ctx, file, "audio/wav") // file is an io.Reader

_, err = calls.Update(ctx, "c1", map[string]any{"recording": blob})

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

| Sentinel | Code |
|---|---|
| `ErrNotFound` | `not_found` |
| `ErrVersionConflict` | `version_conflict` |
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
| `ErrNotReady` | `not_ready` |
| `ErrClosed` | `closed` |
| `ErrWatchLagged` | `watch_lagged` |
| `ErrInvalidPosition` | `invalid_position` |
| `ErrHistoryTruncated` | `history_truncated` |

Other codes (for example `invalid_request`) are still `*ndb.Error`, but have no sentinel. A response that is not a daemon error has the code `http_status`. A failed connection returns the normal `net` error.

## Testing

The tests of this module build the daemon from the repository (`go build ./cmd/ndb`) in `TestMain`. They start it on a temp unix socket and a free TCP port, with a temp data dir, and stop it at the end:

```sh
ndb -config /nonexistent.yml -data-dir "$(mktemp -d)" -unix /tmp/ndb-test.sock -tcp 127.0.0.1:7070
```

Add `-token-file <path>` to ask for a bearer token on TCP. Use a short socket path: macOS allows 104 bytes. Use a new collection name for each test.

## License

MIT.
