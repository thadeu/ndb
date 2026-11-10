package ndbredis

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"

	ndb "github.com/thadeu/ndb/sdk/go"
)

// zeroID is the ID that Redis gives for "none".
const zeroID = "0-0"

// XInfoGroups gives the groups of a stream (XINFO GROUPS). Lag is -1 when
// it cannot be known.
func (c *Client) XInfoGroups(ctx context.Context, key string) *redis.XInfoGroupsCmd {
	cmd := redis.NewXInfoGroupsCmd(ctx, key)

	groups, err := c.stream(key).Groups(ctx)

	if err != nil {
		cmd.SetErr(keyError(err))

		return cmd
	}

	val := make([]redis.XInfoGroup, 0, len(groups))

	for _, g := range groups {
		val = append(val, redis.XInfoGroup{
			Name:            g.Name,
			Consumers:       int64(g.Consumers),
			Pending:         int64(g.Pending),
			LastDeliveredID: g.LastDeliveredID,
			EntriesRead:     orZero(g.EntriesRead),
			Lag:             lag(g.Lag),
		})
	}

	cmd.SetVal(val)

	return cmd
}

// XInfoConsumers gives the consumers of a group (XINFO CONSUMERS).
func (c *Client) XInfoConsumers(ctx context.Context, key string, group string) *redis.XInfoConsumersCmd {
	cmd := redis.NewXInfoConsumersCmd(ctx, key, group)

	consumers, err := c.stream(key).Group(group).Consumers(ctx)

	if err != nil {
		cmd.SetErr(groupError(err, key, group))

		return cmd
	}

	val := make([]redis.XInfoConsumer, 0, len(consumers))

	for _, cons := range consumers {
		val = append(val, redis.XInfoConsumer{
			Name:     cons.Name,
			Pending:  int64(cons.Pending),
			Idle:     time.Duration(cons.IdleMs) * time.Millisecond,
			Inactive: time.Duration(cons.InactiveMs) * time.Millisecond,
		})
	}

	cmd.SetVal(val)

	return cmd
}

// XInfoStream gives the state of a stream (XINFO STREAM). The fields of
// the Redis memory (the radix tree) and of IDMP stay 0.
func (c *Client) XInfoStream(ctx context.Context, key string) *redis.XInfoStreamCmd {
	cmd := redis.NewXInfoStreamCmd(ctx, key)

	val, err := c.infoStream(ctx, key)

	if err != nil {
		cmd.SetErr(err)

		return cmd
	}

	cmd.SetVal(val)

	return cmd
}

func (c *Client) infoStream(ctx context.Context, key string) (*redis.XInfoStream, error) {
	s := c.stream(key)

	info, err := s.Info(ctx)

	if err != nil {
		return nil, keyError(err)
	}

	val := &redis.XInfoStream{
		Length:               int64(info.Length),
		Groups:               int64(info.Groups),
		LastGeneratedID:      orZeroID(info.LastID),
		MaxDeletedEntryID:    orZeroID(info.MaxDeletedID),
		EntriesAdded:         info.EntriesAdded,
		RecordedFirstEntryID: orZeroID(info.FirstID),
	}

	first, err := s.Range(ctx, "-", "+", 1)

	if err != nil {
		return nil, keyError(err)
	}

	last, err := s.RevRange(ctx, "+", "-", 1)

	if err != nil {
		return nil, keyError(err)
	}

	if len(first) > 0 {
		val.FirstEntry = message(first[0])
	}

	if len(last) > 0 {
		val.LastEntry = message(last[0])
	}

	return val, nil
}

// XInfoStreamFull gives the state of a stream with its entries, its groups
// and their pending lists (XINFO STREAM ... FULL). count is the most
// entries of each list; 0 is 10, as in Redis.
func (c *Client) XInfoStreamFull(ctx context.Context, key string, count int) *redis.XInfoStreamFullCmd {
	cmd := redis.NewXInfoStreamFullCmd(ctx, "xinfo", "stream", key, "full")

	val, err := c.infoFull(ctx, key, count)

	if err != nil {
		cmd.SetErr(err)

		return cmd
	}

	cmd.SetVal(val)

	return cmd
}

func (c *Client) infoFull(ctx context.Context, key string, count int) (*redis.XInfoStreamFull, error) {
	if count <= 0 {
		count = 10
	}

	s := c.stream(key)

	info, err := s.Info(ctx)

	if err != nil {
		return nil, keyError(err)
	}

	entries, err := c.entries(ctx, key, "-", "+", count, false)

	if err != nil {
		return nil, err
	}

	groups, err := s.Groups(ctx)

	if err != nil {
		return nil, keyError(err)
	}

	val := &redis.XInfoStreamFull{
		Length:               int64(info.Length),
		LastGeneratedID:      orZeroID(info.LastID),
		MaxDeletedEntryID:    orZeroID(info.MaxDeletedID),
		EntriesAdded:         info.EntriesAdded,
		Entries:              messages(entries),
		Groups:               make([]redis.XInfoStreamGroup, 0, len(groups)),
		RecordedFirstEntryID: orZeroID(info.FirstID),
	}

	for _, g := range groups {
		full, err := c.groupFull(ctx, s.Group(g.Name), g, count)

		if err != nil {
			return nil, groupError(err, key, g.Name)
		}

		val.Groups = append(val.Groups, full)
	}

	return val, nil
}

// groupFull gives one group of XInfoStreamFull: its pending list and its
// consumers, each one with its own pending entries.
func (c *Client) groupFull(ctx context.Context, g *ndb.Group[doc], info ndb.GroupInfo, count int) (redis.XInfoStreamGroup, error) {
	now := time.Now()

	out := redis.XInfoStreamGroup{
		Name:            info.Name,
		LastDeliveredID: info.LastDeliveredID,
		EntriesRead:     orZero(info.EntriesRead),
		Lag:             lag(info.Lag),
		PelCount:        int64(info.Pending),
		Pending:         []redis.XInfoStreamGroupPending{},
		Consumers:       []redis.XInfoStreamConsumer{},
	}

	pending, err := g.PendingList(ctx, ndb.PendingQuery{Count: count})

	if err != nil {
		return out, err
	}

	for _, p := range pending {
		out.Pending = append(out.Pending, redis.XInfoStreamGroupPending{
			ID:            p.ID,
			Consumer:      p.Consumer,
			DeliveryTime:  ago(now, p.IdleMs),
			DeliveryCount: p.Deliveries,
		})
	}

	consumers, err := g.Consumers(ctx)

	if err != nil {
		return out, err
	}

	for _, cons := range consumers {
		full := redis.XInfoStreamConsumer{
			Name:       cons.Name,
			SeenTime:   ago(now, cons.IdleMs),
			ActiveTime: ago(now, cons.InactiveMs),
			PelCount:   int64(cons.Pending),
			Pending:    []redis.XInfoStreamConsumerPending{},
		}

		own, err := g.PendingList(ctx, ndb.PendingQuery{Count: count, Consumer: cons.Name})

		if err != nil {
			return out, err
		}

		for _, p := range own {
			full.Pending = append(full.Pending, redis.XInfoStreamConsumerPending{
				ID:            p.ID,
				DeliveryTime:  ago(now, p.IdleMs),
				DeliveryCount: p.Deliveries,
			})
		}

		out.Consumers = append(out.Consumers, full)
	}

	return out, nil
}

// keyError is redisError for an XINFO: a stream that does not exist is "no
// such key".
func keyError(err error) error {
	if noStream(err) {
		return &replyError{text: textNoKey, cause: err}
	}

	return redisError(err)
}

func orZero(n *int64) int64 {
	if n == nil {
		return 0
	}

	return *n
}

// lag is the lag of a group; -1 when ndb cannot know it, as go-redis
// gives.
func lag(n *int64) int64 {
	if n == nil {
		return -1
	}

	return *n
}

func orZeroID(id string) string {
	if id == "" {
		return zeroID
	}

	return id
}

// ago is the time ms milliseconds before now.
func ago(now time.Time, ms int64) time.Time {
	return now.Add(-time.Duration(ms) * time.Millisecond)
}
