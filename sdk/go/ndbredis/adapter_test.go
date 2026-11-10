package ndbredis

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	ndb "github.com/thadeu/ndb/sdk/go"
)

// streams is the interface of an app: only the commands it uses.
type streams interface {
	XAdd(ctx context.Context, a *redis.XAddArgs) *redis.StringCmd
	XReadGroup(ctx context.Context, a *redis.XReadGroupArgs) *redis.XStreamSliceCmd
	XAck(ctx context.Context, stream, group string, ids ...string) *redis.IntCmd
}

func TestTheFactoryOfAnApp(t *testing.T) {
	c, ctx, key := adapter(t)

	// The app picks its backend in one place.
	pick := func(backend string) streams {
		if backend == "ndb" {
			return c
		}

		return redis.NewClient(&redis.Options{})
	}

	s := pick("ndb")

	if err := c.XGroupCreateMkStream(ctx, key, "billing", "$").Err(); err != nil {
		t.Fatal(err)
	}

	id, err := s.XAdd(ctx, &redis.XAddArgs{Stream: key, Values: map[string]any{"order_id": 42}}).Result()

	if err != nil {
		t.Fatal(err)
	}

	got, err := s.XReadGroup(ctx, &redis.XReadGroupArgs{Group: "billing", Consumer: "w1", Streams: []string{key, ">"}, Block: -1}).Result()

	if err != nil || got[0].Messages[0].ID != id || got[0].Messages[0].Values["order_id"] != "42" {
		t.Fatalf("XReadGroup = %v, %v", got, err)
	}

	if n, err := s.XAck(ctx, key, "billing", id).Result(); err != nil || n != 1 {
		t.Fatalf("XAck = %d, %v", n, err)
	}
}

func TestValuesKeepTheirJSONType(t *testing.T) {
	c, ctx, key := adapter(t)
	at := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

	id, err := c.XAdd(ctx, &redis.XAddArgs{Stream: key, Values: map[string]any{
		"text":   "007",
		"count":  42,
		"price":  9.5,
		"paid":   true,
		"bytes":  []byte("raw"),
		"at":     at,
		"wait":   2 * time.Second,
		"items":  []any{"a", 1},
		"buyer":  map[string]any{"name": "ana"},
		"raw":    json.RawMessage(`{"k":[1,2]}`),
		"absent": nil,
	}}).Result()

	if err != nil {
		t.Fatal(err)
	}

	// Through the commands, every value is text: a JSON string as it is,
	// every other value as its JSON text.
	msgs, err := c.XRange(ctx, key, "-", "+").Result()

	if err != nil || len(msgs) != 1 || msgs[0].ID != id {
		t.Fatalf("XRange = %v, %v", msgs, err)
	}

	want := map[string]string{
		"text":   "007",
		"count":  "42",
		"price":  "9.5",
		"paid":   "true",
		"bytes":  "raw",
		"at":     "2026-11-10T12:00:00Z",
		"wait":   "2000000000",
		"items":  `["a",1]`,
		"buyer":  `{"name":"ana"}`,
		"raw":    `{"k":[1,2]}`,
		"absent": "",
	}

	if len(msgs[0].Values) != len(want) {
		t.Fatalf("values = %#v", msgs[0].Values)
	}

	for k, v := range want {
		if msgs[0].Values[k] != v {
			t.Errorf("%s = %#v, want %q", k, msgs[0].Values[k], v)
		}
	}

	// In ndb, a number is a number and an object is an object: the other
	// SDKs and a query see JSON.
	s, err := ndb.StreamOf[map[string]any](testDB, key)

	if err != nil {
		t.Fatal(err)
	}

	rows, err := s.Range(ctx, "-", "+", 1)

	if err != nil || len(rows) != 1 {
		t.Fatalf("Range = %v, %v", rows, err)
	}

	doc := rows[0].Doc

	if doc["text"] != "007" || doc["count"] != float64(42) || doc["paid"] != true || doc["absent"] != nil {
		t.Fatalf("doc = %#v", doc)
	}

	if buyer, ok := doc["buyer"].(map[string]any); !ok || buyer["name"] != "ana" {
		t.Fatalf("buyer = %#v", doc["buyer"])
	}
}

func TestBadValues(t *testing.T) {
	c, ctx, key := adapter(t)

	for name, values := range map[string]any{
		"an odd list":      []string{"a", "1", "b"},
		"no field":         map[string]any{},
		"a struct":         struct{ A int }{1},
		"bad JSON":         map[string]any{"a": json.RawMessage(`{`)},
		"a reserved field": map[string]any{"_id": "1"},
	} {
		if err := c.XAdd(ctx, &redis.XAddArgs{Stream: key, Values: values}).Err(); err == nil {
			t.Errorf("XAdd with %s: want an error", name)
		}
	}

	if n := c.XLen(ctx, key).Val(); n != 0 {
		t.Fatalf("XLen = %d", n)
	}
}

func TestNotSupported(t *testing.T) {
	c, ctx, key := adapter(t)
	other := newKey()
	values := map[string]any{"n": 1}

	for name, err := range map[string]error{
		"XAckDel":               c.XAckDel(ctx, key, "g", "KEEPREF", "1-0").Err(),
		"XDelEx":                c.XDelEx(ctx, key, "KEEPREF", "1-0").Err(),
		"XNack":                 c.XNack(ctx, &redis.XNackArgs{Stream: key, Group: "g", Mode: redis.XNackModeFail, IDs: []string{"1-0"}}).Err(),
		"XCfgSet":               c.XCfgSet(ctx, &redis.XCfgSetArgs{Stream: key}).Err(),
		"XAdd IDMP":             c.XAdd(ctx, &redis.XAddArgs{Stream: key, ProducerID: "p", IdempotentAuto: true, Values: values}).Err(),
		"XAdd ACKED":            c.XAdd(ctx, &redis.XAddArgs{Stream: key, MaxLen: 1, Mode: "ACKED", Values: values}).Err(),
		"XTrimMaxLenMode":       c.XTrimMaxLenMode(ctx, key, 1, "DELREF").Err(),
		"XTrimMinIDApproxMode":  c.XTrimMinIDApproxMode(ctx, key, "1-0", 0, "ACKED").Err(),
		"XRead MAXCOUNT":        c.XRead(ctx, &redis.XReadArgs{Streams: []string{key, "0"}, MaxCount: 5, Block: -1}).Err(),
		"XRead BLOCK, 2":        c.XRead(ctx, &redis.XReadArgs{Streams: []string{key, other, "0", "0"}, Block: time.Second}).Err(),
		"XReadGroup BLOCK, 2":   c.XReadGroup(ctx, &redis.XReadGroupArgs{Group: "g", Consumer: "w", Streams: []string{key, other, ">", ">"}}).Err(),
		"XReadGroup CLAIM":      c.XReadGroup(ctx, &redis.XReadGroupArgs{Group: "g", Consumer: "w", Streams: []string{key, ">"}, Claim: time.Second, Block: -1}).Err(),
		"XReadGroup with a cap": c.XReadGroup(ctx, &redis.XReadGroupArgs{Group: "g", Consumer: "w", Streams: []string{key, ">"}, MaxSize: 1, Block: -1}).Err(),
	} {
		if !errors.Is(err, ErrNotSupported) {
			t.Errorf("%s = %v, want ErrNotSupported", name, err)
		}
	}

	// KEEPREF is what ndb does.
	if err := c.XTrimMaxLenMode(ctx, key, 1, "keepref").Err(); err != nil {
		t.Fatalf("XTrimMaxLenMode KEEPREF = %v", err)
	}

	if err := c.XTrimMinIDMode(ctx, key, "1-0", "KEEPREF").Err(); err != nil {
		t.Fatalf("XTrimMinIDMode KEEPREF = %v", err)
	}

	if err := c.XTrimMaxLenApproxMode(ctx, key, 1, redis.XTrimLimitDisabled, "KEEPREF").Err(); err != nil {
		t.Fatalf("XTrimMaxLenApproxMode KEEPREF = %v", err)
	}
}

func TestErrorsKeepTheirCause(t *testing.T) {
	c, ctx, key := adapter(t)

	if err := c.XGroupCreateMkStream(ctx, key, "g", "$").Err(); err != nil {
		t.Fatal(err)
	}

	busy := c.XGroupCreateMkStream(ctx, key, "g", "$").Err()

	var reply redis.Error

	if !errors.As(busy, &reply) || !errors.Is(busy, ndb.ErrGroupExists) || !redis.HasErrorPrefix(busy, "BUSYGROUP") {
		t.Fatalf("BUSYGROUP = %#v", busy)
	}

	none := c.XPending(ctx, key, "nogroup").Err()

	if !errors.Is(none, ndb.ErrUnknownGroup) || none.Error() != "NOGROUP No such key '"+key+"' or consumer group 'nogroup'" {
		t.Fatalf("NOGROUP = %v", none)
	}

	if err := c.XAdd(ctx, &redis.XAddArgs{Stream: key, ID: "bad", Values: map[string]any{"n": 1}}).Err(); !errors.Is(err, ndb.ErrInvalidStreamID) {
		t.Fatalf("XAdd with a bad ID = %v", err)
	}

	// A name that ndb does not allow for a collection.
	if err := c.XAdd(ctx, &redis.XAddArgs{Stream: "Orders:EU", Values: map[string]any{"n": 1}}).Err(); err == nil {
		t.Fatal("XAdd on a bad name: want an error")
	}
}

func TestRangeReadsEveryPage(t *testing.T) {
	c, ctx, key := adapter(t)

	s, err := ndb.StreamOf[map[string]any](testDB, key)

	if err != nil {
		t.Fatal(err)
	}

	docs := []map[string]any{}

	for i := range 2*rangePage + 5 {
		docs = append(docs, map[string]any{"n": i})
	}

	all, err := s.AppendMany(ctx, docs)

	if err != nil {
		t.Fatal(err)
	}

	up, err := c.XRange(ctx, key, "-", "+").Result()

	if err != nil || len(up) != len(all) || up[0].ID != all[0] || up[len(up)-1].ID != all[len(all)-1] {
		t.Fatalf("XRange = %d entries, %v", len(up), err)
	}

	down, err := c.XRevRangeN(ctx, key, "+", "-", int64(rangePage+3)).Result()

	if err != nil || len(down) != rangePage+3 || down[0].ID != all[len(all)-1] || down[len(down)-1].ID != all[len(all)-rangePage-3] {
		t.Fatalf("XRevRangeN = %d entries, %v", len(down), err)
	}

	// A read without COUNT gives every entry, up to 10 000.
	read, err := c.XReadStreams(ctx, key, "0").Result()

	if err != nil || len(read[0].Messages) != len(all) {
		t.Fatalf("XReadStreams = %v", err)
	}

	if msgs := c.XRangeN(ctx, key, "-", "+", 0).Val(); len(msgs) != 0 {
		t.Fatalf("XRangeN with COUNT 0 = %d entries", len(msgs))
	}
}

func TestReadWaitsForAStreamThatDoesNotExist(t *testing.T) {
	c, ctx, key := adapter(t)

	go func() {
		time.Sleep(250 * time.Millisecond)

		c.XAdd(ctx, &redis.XAddArgs{Stream: key, Values: map[string]any{"n": 1}})
	}()

	got, err := c.XRead(ctx, &redis.XReadArgs{Streams: []string{key, "$"}, Block: 5 * time.Second}).Result()

	if err != nil || len(got) != 1 || got[0].Messages[0].Values["n"] != "1" {
		t.Fatalf("XRead = %v, %v", got, err)
	}
}

func TestBlockZeroWaitsUntilTheContextEnds(t *testing.T) {
	c, ctx, key := adapter(t)

	if err := c.XGroupCreateMkStream(ctx, key, "g", "$").Err(); err != nil {
		t.Fatal(err)
	}

	short, cancel := context.WithTimeout(ctx, 200*time.Millisecond)

	defer cancel()

	start := time.Now()

	err := c.XReadGroup(short, &redis.XReadGroupArgs{Group: "g", Consumer: "w1", Streams: []string{key, ">"}}).Err()

	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) < 150*time.Millisecond {
		t.Fatalf("XReadGroup BLOCK 0 = %v after %v", err, time.Since(start))
	}

	go func() {
		time.Sleep(200 * time.Millisecond)

		c.XAdd(ctx, &redis.XAddArgs{Stream: key, Values: map[string]any{"n": 1}})
	}()

	got, err := c.XRead(ctx, &redis.XReadArgs{Streams: []string{key, "$"}}).Result()

	if err != nil || len(got) != 1 || len(got[0].Messages) != 1 {
		t.Fatalf("XRead BLOCK 0 = %v, %v", got, err)
	}
}

func TestReadTheLastEntry(t *testing.T) {
	c, ctx, key := adapter(t)

	if err := c.XRead(ctx, &redis.XReadArgs{Streams: []string{key, "+"}, Block: -1}).Err(); err != redis.Nil {
		t.Fatalf("XRead + of no stream = %v, want redis.Nil", err)
	}

	all := add(t, ctx, c, key, 3)

	got, err := c.XRead(ctx, &redis.XReadArgs{Streams: []string{key, "+"}, Block: -1}).Result()

	if err != nil || len(got[0].Messages) != 1 || got[0].Messages[0].ID != all[2] {
		t.Fatalf("XRead + = %v, %v", got, err)
	}
}
