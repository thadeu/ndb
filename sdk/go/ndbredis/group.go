package ndbredis

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	ndb "github.com/thadeu/ndb/sdk/go"
)

// XGroupCreate makes a group on a stream that exists (XGROUP CREATE).
// start is "$", "0" or an ID.
func (c *Client) XGroupCreate(ctx context.Context, stream, group, start string) *redis.StatusCmd {
	cmd := redis.NewStatusCmd(ctx, "xgroup", "create", stream, group, start)

	err := c.stream(stream).Group(group).Create(ctx, start, ndb.GroupNoCreate())

	if noStream(err) {
		err = &replyError{text: textKeyNeeded, cause: err}
	}

	return status(cmd, err)
}

// XGroupCreateMkStream makes a group, and the stream when it does not
// exist (XGROUP CREATE ... MKSTREAM).
func (c *Client) XGroupCreateMkStream(ctx context.Context, stream, group, start string) *redis.StatusCmd {
	cmd := redis.NewStatusCmd(ctx, "xgroup", "create", stream, group, start, "mkstream")

	err := c.stream(stream).Group(group).Create(ctx, start)

	return status(cmd, err)
}

// XGroupSetID moves the last delivered ID of a group (XGROUP SETID).
func (c *Client) XGroupSetID(ctx context.Context, stream, group, start string) *redis.StatusCmd {
	cmd := redis.NewStatusCmd(ctx, "xgroup", "setid", stream, group, start)

	err := c.stream(stream).Group(group).SetID(ctx, start)

	return status(cmd, groupError(err, stream, group))
}

// status ends a command that answers OK.
func status(cmd *redis.StatusCmd, err error) *redis.StatusCmd {
	if err != nil {
		cmd.SetErr(redisError(err))

		return cmd
	}

	cmd.SetVal("OK")

	return cmd
}

// XGroupDestroy deletes a group and gives 1, or 0 when it does not exist
// (XGROUP DESTROY).
func (c *Client) XGroupDestroy(ctx context.Context, stream, group string) *redis.IntCmd {
	cmd := redis.NewIntCmd(ctx, "xgroup", "destroy", stream, group)

	err := c.stream(stream).Group(group).Destroy(ctx)

	if noGroup(err) {
		cmd.SetVal(0)

		return cmd
	}

	return number(cmd, 1, err)
}

// XGroupCreateConsumer makes a consumer and gives 1, or 0 when it exists
// (XGROUP CREATECONSUMER).
func (c *Client) XGroupCreateConsumer(ctx context.Context, stream, group, consumer string) *redis.IntCmd {
	cmd := redis.NewIntCmd(ctx, "xgroup", "createconsumer", stream, group, consumer)

	created, err := c.stream(stream).Group(group).CreateConsumer(ctx, consumer)

	if created {
		return number(cmd, 1, groupError(err, stream, group))
	}

	return number(cmd, 0, groupError(err, stream, group))
}

// XGroupDelConsumer deletes a consumer and gives the number of pending
// entries it had (XGROUP DELCONSUMER).
func (c *Client) XGroupDelConsumer(ctx context.Context, stream, group, consumer string) *redis.IntCmd {
	cmd := redis.NewIntCmd(ctx, "xgroup", "delconsumer", stream, group, consumer)

	n, err := c.stream(stream).Group(group).DeleteConsumer(ctx, consumer)

	return number(cmd, n, groupError(err, stream, group))
}

// XAck ends the work on entries and gives how many were pending (XACK). A
// group that does not exist gives 0, as in Redis.
func (c *Client) XAck(ctx context.Context, stream, group string, ids ...string) *redis.IntCmd {
	cmd := redis.NewIntCmd(ctx, "xack", stream, group)

	n, err := c.stream(stream).Group(group).Ack(ctx, ids...)

	if noGroup(err) {
		cmd.SetVal(0)

		return cmd
	}

	return number(cmd, n, err)
}

// number ends a command that answers an integer.
func number(cmd *redis.IntCmd, n int, err error) *redis.IntCmd {
	if err != nil {
		cmd.SetErr(redisError(err))

		return cmd
	}

	cmd.SetVal(int64(n))

	return cmd
}

// XPending gives the pending list of a group in short (XPENDING).
func (c *Client) XPending(ctx context.Context, stream, group string) *redis.XPendingCmd {
	cmd := redis.NewXPendingCmd(ctx, "xpending", stream, group)

	sum, err := c.stream(stream).Group(group).Pending(ctx)

	if err != nil {
		cmd.SetErr(groupError(err, stream, group))

		return cmd
	}

	val := &redis.XPending{Count: int64(sum.Count), Consumers: make(map[string]int64, len(sum.Consumers))}

	if sum.Lowest != nil {
		val.Lower = *sum.Lowest
	}

	if sum.Highest != nil {
		val.Higher = *sum.Highest
	}

	for _, cons := range sum.Consumers {
		val.Consumers[cons.Name] = int64(cons.Count)
	}

	cmd.SetVal(val)

	return cmd
}

// XPendingExt gives pending entries one by one (XPENDING with a range).
func (c *Client) XPendingExt(ctx context.Context, a *redis.XPendingExtArgs) *redis.XPendingExtCmd {
	cmd := redis.NewXPendingExtCmd(ctx, "xpending", a.Stream, a.Group)

	if a.Count <= 0 {
		cmd.SetVal([]redis.XPendingExt{})

		return cmd
	}

	list, err := c.stream(a.Stream).Group(a.Group).PendingList(ctx, ndb.PendingQuery{
		Start:    a.Start,
		End:      a.End,
		Count:    int(min(a.Count, maxCount)),
		Consumer: a.Consumer,
		Idle:     a.Idle,
	})

	if err != nil {
		cmd.SetErr(groupError(err, a.Stream, a.Group))

		return cmd
	}

	val := make([]redis.XPendingExt, 0, len(list))

	for _, p := range list {
		val = append(val, redis.XPendingExt{
			ID:         p.ID,
			Consumer:   p.Consumer,
			Idle:       time.Duration(p.IdleMs) * time.Millisecond,
			RetryCount: p.Deliveries,
		})
	}

	cmd.SetVal(val)

	return cmd
}

// XClaim moves pending entries to a consumer and gives them (XCLAIM).
func (c *Client) XClaim(ctx context.Context, a *redis.XClaimArgs) *redis.XMessageSliceCmd {
	cmd := redis.NewXMessageSliceCmd(ctx, "xclaim", a.Stream, a.Group, a.Consumer)

	if len(a.Messages) == 0 {
		cmd.SetErr(reply(fmt.Sprintf(textArgs, "xclaim")))

		return cmd
	}

	got, err := c.stream(a.Stream).Group(a.Group).Claim(ctx, a.Consumer, a.MinIdle, a.Messages...)

	if err != nil {
		cmd.SetErr(groupError(err, a.Stream, a.Group))

		return cmd
	}

	cmd.SetVal(messages(got.Entries))

	return cmd
}

// XClaimJustID is XClaim that gives only the IDs (JUSTID).
func (c *Client) XClaimJustID(ctx context.Context, a *redis.XClaimArgs) *redis.StringSliceCmd {
	cmd := redis.NewStringSliceCmd(ctx, "xclaim", a.Stream, a.Group, a.Consumer, "justid")

	if len(a.Messages) == 0 {
		cmd.SetErr(reply(fmt.Sprintf(textArgs, "xclaim")))

		return cmd
	}

	got, err := c.stream(a.Stream).Group(a.Group).ClaimIDs(ctx, a.Consumer, a.MinIdle, a.Messages...)

	if err != nil {
		cmd.SetErr(groupError(err, a.Stream, a.Group))

		return cmd
	}

	cmd.SetVal(got.IDs)

	return cmd
}

// XAutoClaim scans the pending list from a.Start and moves the idle
// entries to a consumer. It gives them and the start of the next scan,
// "0-0" at the end (XAUTOCLAIM).
func (c *Client) XAutoClaim(ctx context.Context, a *redis.XAutoClaimArgs) *redis.XAutoClaimCmd {
	cmd := redis.NewXAutoClaimCmd(ctx, "xautoclaim", a.Stream, a.Group, a.Consumer)

	got, err := c.stream(a.Stream).Group(a.Group).AutoClaim(ctx, a.Consumer, a.MinIdle, a.Start, int(a.Count))

	if err != nil {
		cmd.SetErr(groupError(err, a.Stream, a.Group))

		return cmd
	}

	cmd.SetVal(messages(got.Entries), got.Cursor)

	return cmd
}

// XAutoClaimWithDeleted is XAutoClaim that gives also the pending IDs
// whose entry is no longer in the stream. They left the pending list.
func (c *Client) XAutoClaimWithDeleted(ctx context.Context, a *redis.XAutoClaimArgs) *redis.XAutoClaimWithDeletedCmd {
	cmd := redis.NewXAutoClaimWithDeletedCmd(ctx, "xautoclaim", a.Stream, a.Group, a.Consumer)

	got, err := c.stream(a.Stream).Group(a.Group).AutoClaim(ctx, a.Consumer, a.MinIdle, a.Start, int(a.Count))

	if err != nil {
		cmd.SetErr(groupError(err, a.Stream, a.Group))

		return cmd
	}

	cmd.SetVal(messages(got.Entries), got.Cursor, got.Deleted)

	return cmd
}

// XAutoClaimJustID is XAutoClaim that gives only the IDs (JUSTID).
func (c *Client) XAutoClaimJustID(ctx context.Context, a *redis.XAutoClaimArgs) *redis.XAutoClaimJustIDCmd {
	cmd := redis.NewXAutoClaimJustIDCmd(ctx, "xautoclaim", a.Stream, a.Group, a.Consumer, "justid")

	got, err := c.stream(a.Stream).Group(a.Group).AutoClaimIDs(ctx, a.Consumer, a.MinIdle, a.Start, int(a.Count))

	if err != nil {
		cmd.SetErr(groupError(err, a.Stream, a.Group))

		return cmd
	}

	cmd.SetVal(got.IDs, got.Cursor)

	return cmd
}
