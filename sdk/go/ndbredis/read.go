package ndbredis

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/redis/go-redis/v9"

	ndb "github.com/thadeu/ndb/sdk/go"
)

const (
	// maxCount is the most entries one ndb read gives. A read without
	// COUNT asks for this many.
	maxCount = 10_000

	// maxWait is the longest one request waits. A longer BLOCK is many
	// requests, one after the other.
	maxWait = 30 * time.Second

	// noStreamWait is the time between two looks at a stream that does
	// not exist, in a read that waits.
	noStreamWait = 100 * time.Millisecond
)

// XRead gives the entries after an ID, without a group (XREAD). With no
// entry, the error is redis.Nil.
func (c *Client) XRead(ctx context.Context, a *redis.XReadArgs) *redis.XStreamSliceCmd {
	cmd := redis.NewXStreamSliceCmd(ctx, "xread")

	out, err := c.read(ctx, a)

	if err != nil {
		cmd.SetErr(err)

		return cmd
	}

	cmd.SetVal(out)

	return cmd
}

// XReadStreams is XRead without COUNT and without BLOCK.
func (c *Client) XReadStreams(ctx context.Context, streams ...string) *redis.XStreamSliceCmd {
	return c.XRead(ctx, &redis.XReadArgs{Streams: streams, Block: -1})
}

func (c *Client) read(ctx context.Context, a *redis.XReadArgs) ([]redis.XStream, error) {
	if a.MaxCount > 0 || a.MaxSize > 0 {
		return nil, notSupported("XREAD MAXCOUNT and MAXSIZE")
	}

	keys, ids := a.Streams, []string{}

	// XReadArgs.ID is the ID of every stream; without it, Streams has the
	// keys and then the IDs.
	if a.ID != "" {
		for range keys {
			ids = append(ids, a.ID)
		}
	} else {
		var err error

		if keys, ids, err = split("xread", a.Streams); err != nil {
			return nil, err
		}
	}

	if a.Block >= 0 && len(keys) > 1 {
		return nil, notSupported("XREAD with BLOCK on more than one stream")
	}

	out := []redis.XStream{}

	for i, key := range keys {
		entries, err := c.tail(ctx, key, ids[i], count(a.Count), a.Block)

		if err != nil {
			return nil, err
		}

		if len(entries) > 0 {
			out = append(out, redis.XStream{Stream: key, Messages: messages(entries)})
		}
	}

	if len(out) == 0 {
		return nil, redis.Nil
	}

	return out, nil
}

// tail reads one stream after an ID. block is the BLOCK of go-redis: less
// than 0 does not wait, 0 waits with no limit.
func (c *Client) tail(ctx context.Context, key, after string, count int, block time.Duration) ([]ndb.Entry[doc], error) {
	s := c.stream(key)

	// "+" is the last entry of the stream (Redis 7.4).
	if after == "+" {
		last, err := s.RevRange(ctx, "+", "-", 1)

		if noStream(err) {
			return nil, nil
		}

		return last, redisError(err)
	}

	if after == ndb.StartNew {
		// Nothing is after the last ID when the call does not wait.
		if block < 0 {
			return nil, nil
		}

		// A long wait is many requests: "$" becomes an ID one time, so
		// no entry is lost between two requests.
		info, err := s.Info(ctx)

		if err != nil && !noStream(err) {
			return nil, redisError(err)
		}

		after = info.LastID

		if after == "" {
			after = ndb.StartAll
		}
	}

	return waitFor(ctx, block, func(wait time.Duration) ([]ndb.Entry[doc], error) {
		entries, err := s.Tail(ctx, ndb.Tail{After: after, Count: count, Block: wait})

		if !noStream(err) {
			return entries, redisError(err)
		}

		// The stream is not there yet. Redis waits for its first entry.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(min(wait, noStreamWait)):
			return nil, nil
		}
	})
}

// XReadGroup gives entries to a consumer of a group (XREADGROUP). The ID
// ">" gives new entries; another ID gives the pending entries of the
// consumer after it. With no new entry, the error is redis.Nil.
func (c *Client) XReadGroup(ctx context.Context, a *redis.XReadGroupArgs) *redis.XStreamSliceCmd {
	cmd := redis.NewXStreamSliceCmd(ctx, "xreadgroup", "group", a.Group, a.Consumer)

	out, err := c.readGroup(ctx, a)

	if err != nil {
		cmd.SetErr(err)

		return cmd
	}

	cmd.SetVal(out)

	return cmd
}

func (c *Client) readGroup(ctx context.Context, a *redis.XReadGroupArgs) ([]redis.XStream, error) {
	if a.Claim > 0 {
		return nil, notSupported("XREADGROUP CLAIM (use XAutoClaim)")
	}

	if a.MaxCount > 0 || a.MaxSize > 0 {
		return nil, notSupported("XREADGROUP MAXCOUNT and MAXSIZE")
	}

	keys, ids, err := split("xreadgroup", a.Streams)

	if err != nil {
		return nil, err
	}

	if a.Block >= 0 && len(keys) > 1 {
		return nil, notSupported("XREADGROUP with BLOCK on more than one stream")
	}

	// Many streams are many reads. Redis delivers nothing when one group
	// is missing, so every group is checked before the first read.
	if len(keys) > 1 {
		if err := c.groupsExist(ctx, keys, a.Group); err != nil {
			return nil, err
		}
	}

	out := []redis.XStream{}

	for i, key := range keys {
		g := c.stream(key).Group(a.Group)

		// The pending entries of the consumer: Redis gives the stream
		// also when it has none, and does not wait.
		if ids[i] != ">" {
			entries, err := g.Read(ctx, a.Consumer, ndb.Read{Pending: true, After: ids[i], Count: count(a.Count)})

			if err != nil {
				return nil, groupError(err, key, a.Group)
			}

			out = append(out, redis.XStream{Stream: key, Messages: messages(entries)})

			continue
		}

		entries, err := waitFor(ctx, a.Block, func(wait time.Duration) ([]ndb.Entry[doc], error) {
			return g.Read(ctx, a.Consumer, ndb.Read{Count: count(a.Count), Block: wait, NoAck: a.NoAck})
		})

		if err != nil {
			return nil, groupError(err, key, a.Group)
		}

		if len(entries) > 0 {
			out = append(out, redis.XStream{Stream: key, Messages: messages(entries)})
		}
	}

	if len(out) == 0 {
		return nil, redis.Nil
	}

	return out, nil
}

// groupsExist gives NOGROUP for the first stream that has no group named
// group, or that does not exist.
func (c *Client) groupsExist(ctx context.Context, keys []string, group string) error {
	for _, key := range keys {
		groups, err := c.stream(key).Groups(ctx)

		if err != nil {
			return groupError(err, key, group)
		}

		if !slices.ContainsFunc(groups, func(g ndb.GroupInfo) bool { return g.Name == group }) {
			return groupError(ndb.ErrUnknownGroup, key, group)
		}
	}

	return nil
}

// split gives the keys and the IDs of a Streams list: the keys first, then
// one ID for each key.
func split(command string, streams []string) (keys, ids []string, err error) {
	if len(streams) == 0 || len(streams)%2 != 0 {
		last := "$"

		if command == "xreadgroup" {
			last = ">"
		}

		return nil, nil, reply(fmt.Sprintf(textUnbalanced, command, last))
	}

	half := len(streams) / 2

	return streams[:half], streams[half:], nil
}

// count is the COUNT of a read. Without COUNT, Redis gives every entry;
// ndb gives at most maxCount in one read.
func count(n int64) int {
	if n <= 0 || n > maxCount {
		return maxCount
	}

	return int(n)
}

// waitFor runs read until it gives entries, or the block time ends, or ctx
// ends. block is the BLOCK of go-redis: less than 0 does not wait, 0 waits
// with no limit. read gets the time that one request can wait.
func waitFor(ctx context.Context, block time.Duration, read func(wait time.Duration) ([]ndb.Entry[doc], error)) ([]ndb.Entry[doc], error) {
	if block < 0 {
		return read(0)
	}

	end := time.Now().Add(block)

	for {
		wait := maxWait

		if block > 0 {
			wait = min(wait, time.Until(end))
		}

		// ndb counts the wait in ms; less than 1 ms is the last look.
		last := wait < time.Millisecond

		if last {
			wait = 0
		}

		entries, err := read(wait)

		if err != nil || len(entries) > 0 || last {
			return entries, err
		}

		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
}
