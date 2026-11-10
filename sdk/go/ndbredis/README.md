# ndbredis: the stream commands of go-redis on ndb

`ndbredis` has the stream methods of [go-redis](https://github.com/redis/go-redis)
v9 (`XAdd`, `XReadGroup`, `XAck`, ...) on top of
[ndb streams](../../../docs/dev/streams.md). An app that uses Redis Streams
picks its backend in one place and keeps its calls.

It is a module of its own, because the methods return go-redis types
(`*redis.StringCmd`, `*redis.XStreamSliceCmd`, ...). The ndb SDK
(`github.com/thadeu/ndb/sdk/go`) stays without dependencies.

```sh
go get github.com/thadeu/ndb/sdk/go/ndbredis
```

It needs go-redis v9.23.0 or later.

## The factory of the app

Write the interface of the app with only the commands it uses. `*redis.Client`
and `*ndbredis.Client` both have them:

```go
import (
	"github.com/redis/go-redis/v9"

	ndb "github.com/thadeu/ndb/sdk/go"
	"github.com/thadeu/ndb/sdk/go/ndbredis"
)

// Streams is the interface of the app.
type Streams interface {
	XAdd(ctx context.Context, a *redis.XAddArgs) *redis.StringCmd
	XReadGroup(ctx context.Context, a *redis.XReadGroupArgs) *redis.XStreamSliceCmd
	XAck(ctx context.Context, stream, group string, ids ...string) *redis.IntCmd
}

func newStreams(cfg Config) (Streams, error) {
	if cfg.Backend != "ndb" {
		return redis.NewClient(&redis.Options{Addr: cfg.RedisAddr}), nil
	}

	db, err := ndb.Connect(cfg.NdbAddr)

	if err != nil {
		return nil, err
	}

	return ndbredis.New(db), nil
}
```

The calls of the app do not change:

```go
id, err := streams.XAdd(ctx, &redis.XAddArgs{
	Stream: "orders",
	Values: map[string]any{"order_id": 42},
}).Result()

res, err := streams.XReadGroup(ctx, &redis.XReadGroupArgs{
	Group:    "billing",
	Consumer: "w1",
	Streams:  []string{"orders", ">"},
	Count:    10,
	Block:    5 * time.Second,
}).Result()

if errors.Is(err, redis.Nil) {
	// No new entry in 5 seconds.
}

n, err := streams.XAck(ctx, "orders", "billing", id).Result()
```

`*ndbredis.Client` satisfies `redis.StreamCmdable` of go-redis v9.23.0, the
full list of stream commands. Prefer your own small interface: a later
go-redis can add a method to `redis.StreamCmdable`, and then the app builds
only after this module has the method too.

`ndbredis.New(db)` sends nothing. The app keeps `db` and closes it.

## Methods

| Command | Methods |
|---|---|
| `XADD` | `XAdd` (`ID`, `NoMkStream`, `MaxLen`, `MinID`, `Approx`, `Limit`) |
| `XLEN`, `XDEL` | `XLen`, `XDel` |
| `XRANGE`, `XREVRANGE` | `XRange`, `XRangeN`, `XRevRange`, `XRevRangeN` (`-`, `+`, `(id`) |
| `XREAD` | `XRead`, `XReadStreams` (an ID, `0`, `$`, `+`) |
| `XTRIM` | `XTrimMaxLen`, `XTrimMaxLenApprox`, `XTrimMinID`, `XTrimMinIDApprox`, and the four `...Mode` methods with `KEEPREF` |
| `XGROUP` | `XGroupCreate`, `XGroupCreateMkStream`, `XGroupSetID`, `XGroupDestroy`, `XGroupCreateConsumer`, `XGroupDelConsumer` |
| `XREADGROUP` | `XReadGroup` (`>`, or an ID for the pending entries; `Count`, `Block`, `NoAck`) |
| `XACK` | `XAck` |
| `XPENDING` | `XPending`, `XPendingExt` |
| `XCLAIM` | `XClaim`, `XClaimJustID` |
| `XAUTOCLAIM` | `XAutoClaim`, `XAutoClaimWithDeleted`, `XAutoClaimJustID` |
| `XINFO` | `XInfoStream`, `XInfoStreamFull`, `XInfoGroups`, `XInfoConsumers` |

Not supported (ndb has no such command or option). The command comes back with
an error that `errors.Is(err, ndbredis.ErrNotSupported)` finds:

- `XAckDel`, `XDelEx`, `XNack`, `XCfgSet`.
- `XAdd` with `ProducerID` (IDMP), or with a `Mode` that is not `KEEPREF`.
- The `XTrim...Mode` methods with `DELREF` or `ACKED`.
- `XRead` and `XReadGroup` with `MaxCount` or `MaxSize`.
- `XReadGroup` with `Claim` (use `XAutoClaim`).
- `XRead` and `XReadGroup` with `Block` on more than one stream.
- `XAdd` with `Values` that is a struct (use a map or a list of pairs).

## Errors

Errors have the text of Redis, and they are `redis.Error` values. `errors.Is`
also finds the ndb error below them.

| Case | Error |
|---|---|
| A read with no entry | `redis.Nil` |
| `XAdd` with `NoMkStream`, and no stream | `redis.Nil` |
| A group that exists | `BUSYGROUP Consumer Group name already exists` (`ndb.ErrGroupExists`) |
| A group or a stream that does not exist, in a group command | `NOGROUP No such key '...' or consumer group '...'` (`ndb.ErrUnknownGroup`) |
| `XGroupCreate` (no `MKSTREAM`) and no stream | `ERR The XGROUP subcommand requires the key to exist. ...` |
| `XInfoStream`, `XInfoGroups` and no stream | `ERR no such key` |
| An ID that is not greater than the last one | `ERR The ID specified in XADD is equal or smaller than the target stream top item` |
| A bad ID | `ERR Invalid stream ID specified as stream command argument` |
| The name is a collection that is not a stream | `WRONGTYPE Operation against a key holding the wrong kind of value` |

As in Redis, `XLen`, `XRange`, `XDel`, `XTrim...` and `XAck` give 0 or an empty
list for a stream (or a group) that does not exist.

## Differences from Redis

- **Stream names.** A stream is an ndb collection: its name has the letters
  `a-z`, the digits, `_` and `-`, at most 64 characters, and starts with a
  letter or a digit. `orders:eu` and `Orders` are not valid names.
- **Field names.** A name that starts with `_` is reserved (`_id`, `_ts`).
  `XAdd` with such a name returns an error that wraps `ErrNotSupported`, and
  appends nothing.
- **Values keep their JSON type in ndb.** `XAdd` writes a Go string, `[]byte`,
  `time.Time` and `encoding.BinaryMarshaler` as a JSON string, and every other
  value as JSON: a number is a number, a `bool` is a boolean, a map or a slice
  is an object or an array. So the other SDKs and the queries of ndb see JSON
  types.
- **Values come back as text**, as Redis gives them: `XMessage.Values` has a
  string for each field. A JSON string is its text; every other value is its
  JSON text (`"42"`, `"9.5"`, `` `{"name":"ana"}` ``); `null` is `""`.
  A float has no exponent, as go-redis sends it: `1e21` is
  `"1000000000000000000000"` and `1e-7` is `"0.0000001"`.
  One difference: a `bool` comes back as `"true"` or `"false"`; go-redis sends
  it to Redis as `1` or `0`.
- **Text only.** A value is JSON, so bytes that are not UTF-8 text do not
  survive. Put binary data in a blob.
- **A read without `Count`** gives at most 10 000 entries for each stream.
  Redis gives all of them. `XRange` without a count gives all of them (it
  reads page by page).
- **`Block` on one stream only.** A read of many streams without `Block`
  sends one request for each stream, one after the other; it is not one
  atomic read. `XReadGroup` first checks that the group exists on every
  stream, so a `NOGROUP` error delivers nothing, as in Redis.
- **`Block: 0`** (wait with no limit, the zero value of go-redis) waits until
  the context ends. The wait is a sequence of requests of 30 seconds.
- **`XRead` with `Block` on a stream that does not exist** looks for the
  stream every 100 ms.
- **`XRead` with `+`** gives the last entry and never waits.
- **Trims keep the pending entries** (`KEEPREF`, the default of Redis). With
  `Approx`, ndb deletes only whole blocks of 1 024 entries.
- **`XInfoStream` and `XInfoStreamFull`**: the fields about the memory of
  Redis (`RadixTreeKeys`, `RadixTreeNodes`) and about IDMP are 0. The delivery
  and seen times of `XInfoStreamFull` come from idle times, so they are
  correct to some milliseconds.
- **`XAutoClaim`** gives only the entries that are in the stream. The pending
  IDs whose entry was deleted leave the pending list; `XAutoClaimWithDeleted`
  gives them.
- No pipelines and no transactions: each method sends its requests at once.

## Tests

```sh
go test ./...                                    # on the ndb daemon (built from this repo)
NDB_REDIS_ADDR=127.0.0.1:6379 go test ./...      # each shared case on a real Redis too
```

Each shared case takes a `redis.StreamCmdable` and runs with the same
assertions on the adapter and on Redis. The Redis run uses its own key names
and deletes only them.

## Release

The module has its own tag, `sdk/go/ndbredis/vX.Y.Z`, next to the tag of the
SDK (`sdk/go/vX.Y.Z`).

1. In `go.mod`, set `require github.com/thadeu/ndb/sdk/go` to the SDK version
   of the release. It must be a version that has `Stream.Tail`,
   `Group.ClaimIDs` and `ndb.GroupNoCreate` (v1.16.0 or later).
2. Tag the commit `sdk/go/vX.Y.Z` and `sdk/go/ndbredis/vX.Y.Z`.

The `replace ../` line in `go.mod` is for work in this repo. Go ignores it in
a module that a program imports, so it can stay.
