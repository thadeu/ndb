package ndbredis

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/redis/go-redis/v9"

	ndb "github.com/thadeu/ndb/sdk/go"
)

// addAt appends one entry for each ID, in order.
func addAt(t *testing.T, ctx context.Context, rdb redis.StreamCmdable, key string, ids ...string) {
	t.Helper()

	for _, id := range ids {
		if err := rdb.XAdd(ctx, &redis.XAddArgs{Stream: key, ID: id, Values: map[string]any{"n": 1}}).Err(); err != nil {
			t.Fatal(err)
		}
	}
}

// An ID with only the ms is <ms>-0 as a start and <ms>-<max> as an end,
// also with "(".
func TestRangeWithAnIDOfOnlyTheMs(t *testing.T) {
	each(t, func(t *testing.T, ctx context.Context, rdb redis.StreamCmdable, key string) {
		addAt(t, ctx, rdb, key, "5-0", "5-1", "6-0", "7-3")

		if got := ids(rdb.XRange(ctx, key, "(5", "+").Val()); !slices.Equal(got, []string{"5-1", "6-0", "7-3"}) {
			t.Fatalf("XRange (5 + = %v", got)
		}

		if got := ids(rdb.XRange(ctx, key, "-", "(6").Val()); !slices.Equal(got, []string{"5-0", "5-1", "6-0"}) {
			t.Fatalf("XRange - (6 = %v", got)
		}

		if got := ids(rdb.XRevRangeN(ctx, key, "+", "(5", 2).Val()); !slices.Equal(got, []string{"7-3", "6-0"}) {
			t.Fatalf("XRevRangeN + (5 2 = %v", got)
		}

		if got := ids(rdb.XRevRange(ctx, key, "(7", "6").Val()); !slices.Equal(got, []string{"7-3", "6-0"}) {
			t.Fatalf("XRevRange (7 6 = %v", got)
		}
	})
}

// A bad ID is the Redis error, not an empty answer. XDEL with one bad ID
// deletes nothing.
func TestBadIDs(t *testing.T) {
	each(t, func(t *testing.T, ctx context.Context, rdb redis.StreamCmdable, key string) {
		if n, err := rdb.XDel(ctx, key, "abc").Result(); err != nil || n != 0 {
			t.Fatalf("XDel abc of no stream = %d, %v", n, err)
		}

		if err := rdb.XRange(ctx, key, "abc", "+").Err(); err == nil || err.Error() != textBadID {
			t.Fatalf("XRange abc of no stream = %v", err)
		}

		addAt(t, ctx, rdb, key, "5-0", "6-0")

		for _, id := range []string{"abc", "5-", "-5", "(-", "5-x"} {
			if err := rdb.XRange(ctx, key, id, "+").Err(); err == nil || err.Error() != textBadID {
				t.Fatalf("XRange %q = %v", id, err)
			}

			if err := rdb.XRevRange(ctx, key, "+", id).Err(); err == nil || err.Error() != textBadID {
				t.Fatalf("XRevRange %q = %v", id, err)
			}
		}

		if err := rdb.XDel(ctx, key, "5-0", "abc").Err(); err == nil || err.Error() != textBadID {
			t.Fatalf("XDel 5-0 abc = %v", err)
		}

		if n := rdb.XLen(ctx, key).Val(); n != 2 {
			t.Fatalf("XLen after a bad XDel = %d", n)
		}

		if n, err := rdb.XDel(ctx, key, "9", "5").Result(); err != nil || n != 1 {
			t.Fatalf("XDel 9 5 = %d, %v", n, err)
		}

		if err := rdb.XGroupCreate(ctx, key, "g", "0").Err(); err != nil {
			t.Fatal(err)
		}

		err := rdb.XPendingExt(ctx, &redis.XPendingExtArgs{Stream: key, Group: "g", Start: "(abc", End: "+", Count: 10}).Err()

		if err == nil || err.Error() != textBadID {
			t.Fatalf("XPendingExt (abc = %v", err)
		}
	})
}

// "(id" in XPENDING leaves that ID out (Redis 6.2).
func TestPendingWithoutItsEnds(t *testing.T) {
	each(t, func(t *testing.T, ctx context.Context, rdb redis.StreamCmdable, key string) {
		addAt(t, ctx, rdb, key, "5-0", "5-1", "6-0", "7-3")

		if err := rdb.XGroupCreate(ctx, key, "g", "0").Err(); err != nil {
			t.Fatal(err)
		}

		if err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{Group: "g", Consumer: "c", Streams: []string{key, ">"}, Block: -1}).Err(); err != nil {
			t.Fatal(err)
		}

		pendingIDs := func(start, end string) []string {
			t.Helper()

			list, err := rdb.XPendingExt(ctx, &redis.XPendingExtArgs{Stream: key, Group: "g", Start: start, End: end, Count: 10}).Result()

			if err != nil {
				t.Fatalf("XPendingExt %s %s: %v", start, end, err)
			}

			out := []string{}

			for _, p := range list {
				out = append(out, p.ID)
			}

			return out
		}

		if got := pendingIDs("(5-0", "(7-3"); !slices.Equal(got, []string{"5-1", "6-0"}) {
			t.Fatalf("(5-0 (7-3 = %v", got)
		}

		if got := pendingIDs("(5", "(7"); !slices.Equal(got, []string{"5-1", "6-0", "7-3"}) {
			t.Fatalf("(5 (7 = %v", got)
		}

		if got := pendingIDs("-", "(6-0"); !slices.Equal(got, []string{"5-0", "5-1"}) {
			t.Fatalf("- (6-0 = %v", got)
		}
	})
}

// XREADGROUP over many streams delivers nothing when one group is missing.
func TestReadGroupManyStreamsWithAMissingGroup(t *testing.T) {
	each(t, func(t *testing.T, ctx context.Context, rdb redis.StreamCmdable, key string) {
		other := key + "_b"

		if b := backendOf(rdb); b != nil {
			t.Cleanup(func() { b.forget(other) })
		}

		add(t, ctx, rdb, key, 2)
		add(t, ctx, rdb, other, 1)

		if err := rdb.XGroupCreate(ctx, key, "g", "0").Err(); err != nil {
			t.Fatal(err)
		}

		err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{Group: "g", Consumer: "c", Streams: []string{key, other, ">", ">"}, Block: -1}).Err()

		if err == nil || !redis.HasErrorPrefix(err, "NOGROUP") {
			t.Fatalf("XReadGroup = %v", err)
		}

		sum, err := rdb.XPending(ctx, key, "g").Result()

		if err != nil || sum.Count != 0 {
			t.Fatalf("pending of the first stream = %+v, %v", sum, err)
		}
	})
}

// backendOf finds the backend of rdb, to forget a second key.
func backendOf(rdb redis.StreamCmdable) *backend {
	for i := range backends {
		if backends[i].rdb == rdb {
			return &backends[i]
		}
	}

	return nil
}

// A float comes back as go-redis sends it to Redis: with no exponent.
func TestFloatText(t *testing.T) {
	each(t, func(t *testing.T, ctx context.Context, rdb redis.StreamCmdable, key string) {
		values := map[string]any{"big": 1e21, "small": 1e-7, "neg": -2.5e-8, "f32": float32(1.1), "plain": 9.5}

		if err := rdb.XAdd(ctx, &redis.XAddArgs{Stream: key, Values: values}).Err(); err != nil {
			t.Fatal(err)
		}

		got, err := rdb.XRange(ctx, key, "-", "+").Result()

		if err != nil || len(got) != 1 {
			t.Fatalf("XRange = %v, %v", got, err)
		}

		want := map[string]any{"big": "1000000000000000000000", "small": "0.0000001", "neg": "-0.000000025", "f32": "1.100000023841858", "plain": "9.5"}

		for k, v := range want {
			if got[0].Values[k] != v {
				t.Fatalf("%s = %#v; want %#v", k, got[0].Values[k], v)
			}
		}
	})
}

// A field name that starts with "_" is reserved in ndb.
func TestReservedFieldName(t *testing.T) {
	c, ctx, key := adapter(t)

	for _, values := range []any{map[string]any{"_x": 1}, []string{"a", "1", "_id", "2"}} {
		err := c.XAdd(ctx, &redis.XAddArgs{Stream: key, Values: values}).Err()

		if !errors.Is(err, ErrNotSupported) {
			t.Fatalf("XAdd %v = %v", values, err)
		}
	}

	if _, err := ndb.StreamOf[map[string]any](testDB, key); err != nil {
		t.Fatal(err)
	}

	if n := c.XLen(ctx, key).Val(); n != 0 {
		t.Fatalf("XLen = %d", n)
	}
}
