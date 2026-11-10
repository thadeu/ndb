package ndbredis

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestAddRangeLenDel(t *testing.T) {
	each(t, func(t *testing.T, ctx context.Context, rdb redis.StreamCmdable, key string) {
		if n, err := rdb.XLen(ctx, key).Result(); err != nil || n != 0 {
			t.Fatalf("XLen of no stream = %d, %v", n, err)
		}

		if got, err := rdb.XRange(ctx, key, "-", "+").Result(); err != nil || len(got) != 0 {
			t.Fatalf("XRange of no stream = %v, %v", got, err)
		}

		first, err := rdb.XAdd(ctx, &redis.XAddArgs{Stream: key, Values: map[string]any{"order_id": "42", "n": 7, "total": 9.5}}).Result()

		if err != nil || !strings.Contains(first, "-") {
			t.Fatalf("XAdd = %q, %v", first, err)
		}

		second, err := rdb.XAdd(ctx, &redis.XAddArgs{Stream: key, Values: []string{"order_id", "43", "name", "ana"}}).Result()

		if err != nil {
			t.Fatal(err)
		}

		third, err := rdb.XAdd(ctx, &redis.XAddArgs{Stream: key, Values: []any{"order_id", "44", "n", 9}}).Result()

		if err != nil {
			t.Fatal(err)
		}

		if n := rdb.XLen(ctx, key).Val(); n != 3 {
			t.Fatalf("XLen = %d", n)
		}

		all, err := rdb.XRange(ctx, key, "-", "+").Result()

		if err != nil || !slices.Equal(ids(all), []string{first, second, third}) {
			t.Fatalf("XRange = %v, %v", all, err)
		}

		// Redis keeps text: every value comes back as a string.
		if v := all[0].Values; v["order_id"] != "42" || v["n"] != "7" || v["total"] != "9.5" || len(v) != 3 {
			t.Fatalf("values = %#v", v)
		}

		if v := all[1].Values; v["order_id"] != "43" || v["name"] != "ana" {
			t.Fatalf("values = %#v", v)
		}

		if v := all[2].Values; v["order_id"] != "44" || v["n"] != "9" {
			t.Fatalf("values = %#v", v)
		}

		if got := rdb.XRangeN(ctx, key, "-", "+", 2).Val(); !slices.Equal(ids(got), []string{first, second}) {
			t.Fatalf("XRangeN = %v", got)
		}

		if got := rdb.XRange(ctx, key, "("+first, "("+third).Val(); !slices.Equal(ids(got), []string{second}) {
			t.Fatalf("XRange without its ends = %v", got)
		}

		if got := rdb.XRevRange(ctx, key, "+", "-").Val(); !slices.Equal(ids(got), []string{third, second, first}) {
			t.Fatalf("XRevRange = %v", got)
		}

		if got := rdb.XRevRangeN(ctx, key, third, first, 1).Val(); !slices.Equal(ids(got), []string{third}) {
			t.Fatalf("XRevRangeN = %v", got)
		}

		if n, err := rdb.XDel(ctx, key, first, third).Result(); err != nil || n != 2 {
			t.Fatalf("XDel = %d, %v", n, err)
		}

		if n, err := rdb.XDel(ctx, key, first).Result(); err != nil || n != 0 {
			t.Fatalf("XDel again = %d, %v", n, err)
		}

		if n := rdb.XLen(ctx, key).Val(); n != 1 {
			t.Fatalf("XLen after XDel = %d", n)
		}
	})
}

func TestAddOptions(t *testing.T) {
	each(t, func(t *testing.T, ctx context.Context, rdb redis.StreamCmdable, key string) {
		values := map[string]any{"n": 1}

		if _, err := rdb.XAdd(ctx, &redis.XAddArgs{Stream: key, NoMkStream: true, Values: values}).Result(); err != redis.Nil {
			t.Fatalf("XAdd NOMKSTREAM = %v, want redis.Nil", err)
		}

		if n := rdb.XLen(ctx, key).Val(); n != 0 {
			t.Fatalf("XLen = %d", n)
		}

		if id, err := rdb.XAdd(ctx, &redis.XAddArgs{Stream: key, ID: "5-1", Values: values}).Result(); err != nil || id != "5-1" {
			t.Fatalf("XAdd with an ID = %q, %v", id, err)
		}

		_, err := rdb.XAdd(ctx, &redis.XAddArgs{Stream: key, ID: "5-1", Values: values}).Result()

		if err == nil || !strings.Contains(err.Error(), "equal or smaller") {
			t.Fatalf("XAdd with a small ID = %v", err)
		}

		for range 5 {
			if err := rdb.XAdd(ctx, &redis.XAddArgs{Stream: key, MaxLen: 3, Values: values}).Err(); err != nil {
				t.Fatal(err)
			}
		}

		if n := rdb.XLen(ctx, key).Val(); n != 3 {
			t.Fatalf("XLen after MAXLEN 3 = %d", n)
		}
	})
}

func TestTrim(t *testing.T) {
	each(t, func(t *testing.T, ctx context.Context, rdb redis.StreamCmdable, key string) {
		if n, err := rdb.XTrimMaxLen(ctx, key, 1).Result(); err != nil || n != 0 {
			t.Fatalf("XTrimMaxLen of no stream = %d, %v", n, err)
		}

		all := add(t, ctx, rdb, key, 10)

		if n, err := rdb.XTrimMaxLen(ctx, key, 6).Result(); err != nil || n != 4 {
			t.Fatalf("XTrimMaxLen = %d, %v", n, err)
		}

		if n, err := rdb.XTrimMinID(ctx, key, all[7]).Result(); err != nil || n != 3 {
			t.Fatalf("XTrimMinID = %d, %v", n, err)
		}

		if got := rdb.XRange(ctx, key, "-", "+").Val(); !slices.Equal(ids(got), all[7:]) {
			t.Fatalf("after the trims = %v", ids(got))
		}

		// "~" deletes only whole blocks: with 3 entries, nothing.
		if n, err := rdb.XTrimMaxLenApprox(ctx, key, 1, 0).Result(); err != nil || n != 0 {
			t.Fatalf("XTrimMaxLenApprox = %d, %v", n, err)
		}

		if n, err := rdb.XTrimMinIDApprox(ctx, key, all[9], 0).Result(); err != nil || n != 0 {
			t.Fatalf("XTrimMinIDApprox = %d, %v", n, err)
		}
	})
}

func TestRead(t *testing.T) {
	each(t, func(t *testing.T, ctx context.Context, rdb redis.StreamCmdable, key string) {
		if _, err := rdb.XReadStreams(ctx, key, "0").Result(); err != redis.Nil {
			t.Fatalf("XRead of no stream = %v, want redis.Nil", err)
		}

		all := add(t, ctx, rdb, key, 3)

		got, err := rdb.XReadStreams(ctx, key, "0").Result()

		if err != nil || len(got) != 1 || got[0].Stream != key || !slices.Equal(ids(got[0].Messages), all) {
			t.Fatalf("XReadStreams = %v, %v", got, err)
		}

		if v := got[0].Messages[0].Values; v["n"] != "1" || v["name"] != "e1" {
			t.Fatalf("values = %#v", v)
		}

		got, err = rdb.XRead(ctx, &redis.XReadArgs{Streams: []string{key, all[0]}, Count: 1, Block: -1}).Result()

		if err != nil || !slices.Equal(ids(got[0].Messages), all[1:2]) {
			t.Fatalf("XRead with COUNT = %v, %v", got, err)
		}

		got, err = rdb.XRead(ctx, &redis.XReadArgs{Streams: []string{key}, ID: all[1], Block: -1}).Result()

		if err != nil || !slices.Equal(ids(got[0].Messages), all[2:]) {
			t.Fatalf("XRead with ID = %v, %v", got, err)
		}

		if _, err := rdb.XRead(ctx, &redis.XReadArgs{Streams: []string{key, "$"}, Block: -1}).Result(); err != redis.Nil {
			t.Fatalf("XRead $ = %v, want redis.Nil", err)
		}

		if _, err := rdb.XRead(ctx, &redis.XReadArgs{Streams: []string{key}, Block: -1}).Result(); err == nil || err == redis.Nil {
			t.Fatalf("XRead without an ID = %v, want an error", err)
		}
	})
}

func TestReadManyStreams(t *testing.T) {
	each(t, func(t *testing.T, ctx context.Context, rdb redis.StreamCmdable, key string) {
		other := newKey()

		t.Cleanup(func() {
			for _, b := range backends {
				b.forget(other)
			}
		})

		one := add(t, ctx, rdb, key, 2)
		two := add(t, ctx, rdb, other, 1)

		got, err := rdb.XRead(ctx, &redis.XReadArgs{Streams: []string{key, other, "0", "0"}, Block: -1}).Result()

		if err != nil || len(got) != 2 {
			t.Fatalf("XRead = %v, %v", got, err)
		}

		if got[0].Stream != key || !slices.Equal(ids(got[0].Messages), one) {
			t.Fatalf("first stream = %v", got[0])
		}

		if got[1].Stream != other || !slices.Equal(ids(got[1].Messages), two) {
			t.Fatalf("second stream = %v", got[1])
		}

		// A stream with no entry after the ID is not in the answer.
		got, err = rdb.XRead(ctx, &redis.XReadArgs{Streams: []string{key, other, one[1], "0"}, Block: -1}).Result()

		if err != nil || len(got) != 1 || got[0].Stream != other {
			t.Fatalf("XRead = %v, %v", got, err)
		}
	})
}

func TestReadBlocks(t *testing.T) {
	each(t, func(t *testing.T, ctx context.Context, rdb redis.StreamCmdable, key string) {
		add(t, ctx, rdb, key, 1)

		start := time.Now()

		_, err := rdb.XRead(ctx, &redis.XReadArgs{Streams: []string{key, "$"}, Block: 150 * time.Millisecond}).Result()

		if err != redis.Nil || time.Since(start) < 100*time.Millisecond {
			t.Fatalf("empty XRead with BLOCK = %v after %v, want redis.Nil", err, time.Since(start))
		}

		added := make(chan string, 1)

		go func() {
			time.Sleep(200 * time.Millisecond)

			added <- rdb.XAdd(ctx, &redis.XAddArgs{Stream: key, Values: map[string]any{"n": 2}}).Val()
		}()

		start = time.Now()

		got, err := rdb.XRead(ctx, &redis.XReadArgs{Streams: []string{key, "$"}, Block: 5 * time.Second}).Result()

		if err != nil || len(got) != 1 || len(got[0].Messages) != 1 || time.Since(start) > 4*time.Second {
			t.Fatalf("XRead with BLOCK = %v, %v after %v", got, err, time.Since(start))
		}

		if id := <-added; got[0].Messages[0].ID != id || got[0].Messages[0].Values["n"] != "2" {
			t.Fatalf("message = %v, want the ID %s", got[0].Messages[0], id)
		}
	})
}

func TestGroupReadAckPending(t *testing.T) {
	each(t, func(t *testing.T, ctx context.Context, rdb redis.StreamCmdable, key string) {
		err := rdb.XGroupCreate(ctx, key, "g", "$").Err()

		if err == nil || !strings.Contains(err.Error(), "requires the key to exist") {
			t.Fatalf("XGroupCreate of no stream = %v", err)
		}

		if got, err := rdb.XGroupCreateMkStream(ctx, key, "g", "$").Result(); err != nil || got != "OK" {
			t.Fatalf("XGroupCreateMkStream = %q, %v", got, err)
		}

		if err := rdb.XGroupCreateMkStream(ctx, key, "g", "$").Err(); err == nil || !strings.HasPrefix(err.Error(), "BUSYGROUP") {
			t.Fatalf("XGroupCreateMkStream again = %v, want BUSYGROUP", err)
		}

		if err := rdb.XGroupCreate(ctx, key, "g", "$").Err(); err == nil || !strings.HasPrefix(err.Error(), "BUSYGROUP") {
			t.Fatalf("XGroupCreate again = %v, want BUSYGROUP", err)
		}

		read := func(group, consumer, id string, count int64, block time.Duration) ([]redis.XStream, error) {
			return rdb.XReadGroup(ctx, &redis.XReadGroupArgs{Group: group, Consumer: consumer, Streams: []string{key, id}, Count: count, Block: block}).Result()
		}

		if _, err := read("nogroup", "w1", ">", 1, -1); err == nil || !strings.HasPrefix(err.Error(), "NOGROUP") {
			t.Fatalf("XReadGroup of no group = %v, want NOGROUP", err)
		}

		if _, err := read("g", "w1", ">", 1, -1); err != redis.Nil {
			t.Fatalf("empty XReadGroup = %v, want redis.Nil", err)
		}

		start := time.Now()

		if _, err := read("g", "w1", ">", 1, 150*time.Millisecond); err != redis.Nil || time.Since(start) < 100*time.Millisecond {
			t.Fatalf("empty XReadGroup with BLOCK = %v after %v, want redis.Nil", err, time.Since(start))
		}

		all := add(t, ctx, rdb, key, 3)

		got, err := read("g", "w1", ">", 2, -1)

		if err != nil || len(got) != 1 || got[0].Stream != key || !slices.Equal(ids(got[0].Messages), all[:2]) {
			t.Fatalf("XReadGroup = %v, %v", got, err)
		}

		if v := got[0].Messages[0].Values; v["n"] != "1" || v["name"] != "e1" {
			t.Fatalf("values = %#v", v)
		}

		sum, err := rdb.XPending(ctx, key, "g").Result()

		if err != nil || sum.Count != 2 || sum.Lower != all[0] || sum.Higher != all[1] || sum.Consumers["w1"] != 2 {
			t.Fatalf("XPending = %+v, %v", sum, err)
		}

		list, err := rdb.XPendingExt(ctx, &redis.XPendingExtArgs{Stream: key, Group: "g", Start: "-", End: "+", Count: 10}).Result()

		if err != nil || len(list) != 2 || list[0].ID != all[0] || list[0].Consumer != "w1" || list[0].RetryCount != 1 {
			t.Fatalf("XPendingExt = %+v, %v", list, err)
		}

		list, err = rdb.XPendingExt(ctx, &redis.XPendingExtArgs{Stream: key, Group: "g", Start: "-", End: "+", Count: 10, Consumer: "w2"}).Result()

		if err != nil || len(list) != 0 {
			t.Fatalf("XPendingExt of w2 = %+v, %v", list, err)
		}

		// An ID that is not ">" gives the pending entries of the consumer.
		got, err = read("g", "w1", "0", 10, -1)

		if err != nil || len(got) != 1 || !slices.Equal(ids(got[0].Messages), all[:2]) {
			t.Fatalf("pending XReadGroup = %v, %v", got, err)
		}

		got, err = read("g", "w1", all[0], 10, -1)

		if err != nil || len(got) != 1 || !slices.Equal(ids(got[0].Messages), all[1:2]) {
			t.Fatalf("pending XReadGroup after an ID = %v, %v", got, err)
		}

		// A consumer with no pending entry gets the stream with no message.
		got, err = read("g", "w2", "0", 10, -1)

		if err != nil || len(got) != 1 || got[0].Stream != key || len(got[0].Messages) != 0 {
			t.Fatalf("pending XReadGroup of w2 = %v, %v", got, err)
		}

		if n, err := rdb.XAck(ctx, key, "g", all[0]).Result(); err != nil || n != 1 {
			t.Fatalf("XAck = %d, %v", n, err)
		}

		if n, err := rdb.XAck(ctx, key, "g", all[0]).Result(); err != nil || n != 0 {
			t.Fatalf("XAck again = %d, %v", n, err)
		}

		if n, err := rdb.XAck(ctx, key, "nogroup", all[0]).Result(); err != nil || n != 0 {
			t.Fatalf("XAck of no group = %d, %v", n, err)
		}

		got, err = read("g", "w2", ">", 0, -1)

		if err != nil || !slices.Equal(ids(got[0].Messages), all[2:]) {
			t.Fatalf("XReadGroup of w2 = %v, %v", got, err)
		}

		if sum := rdb.XPending(ctx, key, "g").Val(); sum.Count != 2 || sum.Consumers["w1"] != 1 || sum.Consumers["w2"] != 1 {
			t.Fatalf("XPending = %+v", sum)
		}

		if got, err := rdb.XGroupSetID(ctx, key, "g", "0").Result(); err != nil || got != "OK" {
			t.Fatalf("XGroupSetID = %q, %v", got, err)
		}

		got, err = read("g", "w3", ">", 0, -1)

		if err != nil || !slices.Equal(ids(got[0].Messages), all) {
			t.Fatalf("XReadGroup after XGroupSetID = %v, %v", got, err)
		}

		if n, err := rdb.XGroupDestroy(ctx, key, "g").Result(); err != nil || n != 1 {
			t.Fatalf("XGroupDestroy = %d, %v", n, err)
		}

		if n, err := rdb.XGroupDestroy(ctx, key, "g").Result(); err != nil || n != 0 {
			t.Fatalf("XGroupDestroy again = %d, %v", n, err)
		}

		if err := rdb.XPending(ctx, key, "g").Err(); err == nil || !strings.HasPrefix(err.Error(), "NOGROUP") {
			t.Fatalf("XPending of no group = %v, want NOGROUP", err)
		}
	})
}

func TestGroupReadBlocksUntilAnAdd(t *testing.T) {
	each(t, func(t *testing.T, ctx context.Context, rdb redis.StreamCmdable, key string) {
		if err := rdb.XGroupCreateMkStream(ctx, key, "g", "$").Err(); err != nil {
			t.Fatal(err)
		}

		go func() {
			time.Sleep(200 * time.Millisecond)

			rdb.XAdd(ctx, &redis.XAddArgs{Stream: key, Values: map[string]any{"n": 7}})
		}()

		start := time.Now()

		got, err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{Group: "g", Consumer: "w1", Streams: []string{key, ">"}, Block: 5 * time.Second}).Result()

		if err != nil || len(got) != 1 || len(got[0].Messages) != 1 || time.Since(start) > 4*time.Second {
			t.Fatalf("XReadGroup with BLOCK = %v, %v after %v", got, err, time.Since(start))
		}

		if v := got[0].Messages[0].Values; v["n"] != "7" {
			t.Fatalf("values = %#v", v)
		}

		// NOACK: the entry does not go to the pending list.
		rdb.XAdd(ctx, &redis.XAddArgs{Stream: key, Values: map[string]any{"n": 8}})

		got, err = rdb.XReadGroup(ctx, &redis.XReadGroupArgs{Group: "g", Consumer: "w1", Streams: []string{key, ">"}, Block: -1, NoAck: true}).Result()

		if err != nil || len(got[0].Messages) != 1 {
			t.Fatalf("XReadGroup with NOACK = %v, %v", got, err)
		}

		if sum := rdb.XPending(ctx, key, "g").Val(); sum.Count != 1 {
			t.Fatalf("XPending = %+v", sum)
		}
	})
}

func TestClaim(t *testing.T) {
	each(t, func(t *testing.T, ctx context.Context, rdb redis.StreamCmdable, key string) {
		all := add(t, ctx, rdb, key, 3)

		if err := rdb.XGroupCreate(ctx, key, "g", "0").Err(); err != nil {
			t.Fatal(err)
		}

		if err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{Group: "g", Consumer: "w1", Streams: []string{key, ">"}, Block: -1}).Err(); err != nil {
			t.Fatal(err)
		}

		// An entry that is not idle for long enough does not move.
		idle, err := rdb.XClaim(ctx, &redis.XClaimArgs{Stream: key, Group: "g", Consumer: "w2", MinIdle: time.Hour, Messages: all}).Result()

		if err != nil || len(idle) != 0 {
			t.Fatalf("XClaim with a long idle time = %v, %v", idle, err)
		}

		got, err := rdb.XClaim(ctx, &redis.XClaimArgs{Stream: key, Group: "g", Consumer: "w2", Messages: all[:1]}).Result()

		if err != nil || !slices.Equal(ids(got), all[:1]) || got[0].Values["n"] != "1" {
			t.Fatalf("XClaim = %v, %v", got, err)
		}

		just, err := rdb.XClaimJustID(ctx, &redis.XClaimArgs{Stream: key, Group: "g", Consumer: "w2", Messages: all[1:2]}).Result()

		if err != nil || !slices.Equal(just, all[1:2]) {
			t.Fatalf("XClaimJustID = %v, %v", just, err)
		}

		list, err := rdb.XPendingExt(ctx, &redis.XPendingExtArgs{Stream: key, Group: "g", Start: "-", End: "+", Count: 10}).Result()

		if err != nil || len(list) != 3 {
			t.Fatalf("XPendingExt = %+v, %v", list, err)
		}

		// XCLAIM counts as a delivery; JUSTID does not.
		if list[0].Consumer != "w2" || list[0].RetryCount != 2 || list[1].Consumer != "w2" || list[1].RetryCount != 1 || list[2].Consumer != "w1" {
			t.Fatalf("XPendingExt = %+v", list)
		}

		msgs, next, err := rdb.XAutoClaim(ctx, &redis.XAutoClaimArgs{Stream: key, Group: "g", Consumer: "w3", Start: "0-0", Count: 2}).Result()

		if err != nil || !slices.Equal(ids(msgs), all[:2]) || next != all[2] || msgs[1].Values["name"] != "e2" {
			t.Fatalf("XAutoClaim = %v, %q, %v", msgs, next, err)
		}

		msgs, next, err = rdb.XAutoClaim(ctx, &redis.XAutoClaimArgs{Stream: key, Group: "g", Consumer: "w3", Start: next, Count: 2}).Result()

		if err != nil || !slices.Equal(ids(msgs), all[2:]) || next != "0-0" {
			t.Fatalf("XAutoClaim to the end = %v, %q, %v", msgs, next, err)
		}

		justIDs, next, err := rdb.XAutoClaimJustID(ctx, &redis.XAutoClaimArgs{Stream: key, Group: "g", Consumer: "w4", Start: "0-0"}).Result()

		if err != nil || !slices.Equal(justIDs, all) || next != "0-0" {
			t.Fatalf("XAutoClaimJustID = %v, %q, %v", justIDs, next, err)
		}

		if sum := rdb.XPending(ctx, key, "g").Val(); sum.Count != 3 || sum.Consumers["w4"] != 3 {
			t.Fatalf("XPending = %+v", sum)
		}

		// A pending entry that is no longer in the stream leaves the
		// pending list.
		if err := rdb.XDel(ctx, key, all[0]).Err(); err != nil {
			t.Fatal(err)
		}

		msgs, next, gone, err := rdb.XAutoClaimWithDeleted(ctx, &redis.XAutoClaimArgs{Stream: key, Group: "g", Consumer: "w5", Start: "0-0"}).Result()

		if err != nil || !slices.Equal(ids(msgs), all[1:]) || next != "0-0" || !slices.Equal(gone, all[:1]) {
			t.Fatalf("XAutoClaimWithDeleted = %v, %q, %v, %v", msgs, next, gone, err)
		}

		if sum := rdb.XPending(ctx, key, "g").Val(); sum.Count != 2 {
			t.Fatalf("XPending = %+v", sum)
		}

		err = rdb.XAutoClaim(ctx, &redis.XAutoClaimArgs{Stream: key, Group: "nogroup", Consumer: "w1", Start: "0-0"}).Err()

		if err == nil || !strings.HasPrefix(err.Error(), "NOGROUP") {
			t.Fatalf("XAutoClaim of no group = %v, want NOGROUP", err)
		}
	})
}

func TestConsumersAndInfo(t *testing.T) {
	each(t, func(t *testing.T, ctx context.Context, rdb redis.StreamCmdable, key string) {
		if err := rdb.XInfoStream(ctx, key).Err(); err == nil || err.Error() != "ERR no such key" {
			t.Fatalf("XInfoStream of no stream = %v", err)
		}

		if err := rdb.XInfoGroups(ctx, key).Err(); err == nil || err.Error() != "ERR no such key" {
			t.Fatalf("XInfoGroups of no stream = %v", err)
		}

		all := add(t, ctx, rdb, key, 3)

		if err := rdb.XGroupCreate(ctx, key, "g", "0").Err(); err != nil {
			t.Fatal(err)
		}

		if n, err := rdb.XGroupCreateConsumer(ctx, key, "g", "w1").Result(); err != nil || n != 1 {
			t.Fatalf("XGroupCreateConsumer = %d, %v", n, err)
		}

		if n, err := rdb.XGroupCreateConsumer(ctx, key, "g", "w1").Result(); err != nil || n != 0 {
			t.Fatalf("XGroupCreateConsumer again = %d, %v", n, err)
		}

		if err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{Group: "g", Consumer: "w1", Streams: []string{key, ">"}, Count: 2, Block: -1}).Err(); err != nil {
			t.Fatal(err)
		}

		consumers, err := rdb.XInfoConsumers(ctx, key, "g").Result()

		if err != nil || len(consumers) != 1 || consumers[0].Name != "w1" || consumers[0].Pending != 2 || consumers[0].Idle < 0 {
			t.Fatalf("XInfoConsumers = %+v, %v", consumers, err)
		}

		if err := rdb.XInfoConsumers(ctx, key, "nogroup").Err(); err == nil || !strings.HasPrefix(err.Error(), "NOGROUP") {
			t.Fatalf("XInfoConsumers of no group = %v, want NOGROUP", err)
		}

		groups, err := rdb.XInfoGroups(ctx, key).Result()

		if err != nil || len(groups) != 1 {
			t.Fatalf("XInfoGroups = %+v, %v", groups, err)
		}

		if g := groups[0]; g.Name != "g" || g.Consumers != 1 || g.Pending != 2 || g.LastDeliveredID != all[1] || g.EntriesRead != 2 || g.Lag != 1 {
			t.Fatalf("XInfoGroups = %+v", g)
		}

		info, err := rdb.XInfoStream(ctx, key).Result()

		if err != nil {
			t.Fatal(err)
		}

		if info.Length != 3 || info.Groups != 1 || info.LastGeneratedID != all[2] || info.EntriesAdded != 3 || info.MaxDeletedEntryID != "0-0" || info.RecordedFirstEntryID != all[0] {
			t.Fatalf("XInfoStream = %+v", info)
		}

		if info.FirstEntry.ID != all[0] || info.FirstEntry.Values["n"] != "1" || info.LastEntry.ID != all[2] || info.LastEntry.Values["n"] != "3" {
			t.Fatalf("XInfoStream entries = %+v, %+v", info.FirstEntry, info.LastEntry)
		}

		full, err := rdb.XInfoStreamFull(ctx, key, 2).Result()

		if err != nil {
			t.Fatal(err)
		}

		if full.Length != 3 || full.LastGeneratedID != all[2] || full.EntriesAdded != 3 || !slices.Equal(ids(full.Entries), all[:2]) || len(full.Groups) != 1 {
			t.Fatalf("XInfoStreamFull = %+v", full)
		}

		g := full.Groups[0]

		if g.Name != "g" || g.LastDeliveredID != all[1] || g.PelCount != 2 || g.Lag != 1 || len(g.Pending) != 2 || len(g.Consumers) != 1 {
			t.Fatalf("XInfoStreamFull group = %+v", g)
		}

		if p := g.Pending[0]; p.ID != all[0] || p.Consumer != "w1" || p.DeliveryCount != 1 || time.Since(p.DeliveryTime) > time.Minute {
			t.Fatalf("XInfoStreamFull pending = %+v", p)
		}

		if c := g.Consumers[0]; c.Name != "w1" || c.PelCount != 2 || len(c.Pending) != 2 || c.Pending[1].ID != all[1] || time.Since(c.SeenTime) > time.Minute {
			t.Fatalf("XInfoStreamFull consumer = %+v", c)
		}

		if n, err := rdb.XGroupDelConsumer(ctx, key, "g", "w1").Result(); err != nil || n != 2 {
			t.Fatalf("XGroupDelConsumer = %d, %v", n, err)
		}

		if sum := rdb.XPending(ctx, key, "g").Val(); sum.Count != 0 || sum.Lower != "" || len(sum.Consumers) != 0 {
			t.Fatalf("XPending = %+v", sum)
		}
	})
}

func TestBadStreamsList(t *testing.T) {
	each(t, func(t *testing.T, ctx context.Context, rdb redis.StreamCmdable, key string) {
		err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{Group: "g", Consumer: "w1", Streams: []string{key}, Block: -1}).Err()

		if err == nil || errors.Is(err, redis.Nil) {
			t.Fatalf("XReadGroup without an ID = %v, want an error", err)
		}
	})
}
