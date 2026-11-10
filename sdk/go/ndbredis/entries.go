package ndbredis

import (
	"context"

	"github.com/redis/go-redis/v9"

	ndb "github.com/thadeu/ndb/sdk/go"
)

// rangePage is how many entries one request of a range without COUNT asks
// for.
const rangePage = 1000

// XAdd appends one entry (XADD). Values is a map or a list of field and
// value pairs.
func (c *Client) XAdd(ctx context.Context, a *redis.XAddArgs) *redis.StringCmd {
	cmd := redis.NewStringCmd(ctx, "xadd", a.Stream)

	id, err := c.add(ctx, a)

	if err != nil {
		cmd.SetErr(err)

		return cmd
	}

	cmd.SetVal(id)

	return cmd
}

func (c *Client) add(ctx context.Context, a *redis.XAddArgs) (string, error) {
	if a.ProducerID != "" || a.IdempotentID != "" || a.IdempotentAuto {
		return "", notSupported("XADD IDMP")
	}

	if err := keepRef("XADD", a.Mode); err != nil {
		return "", err
	}

	entry, err := fields(a.Values)

	if err != nil {
		return "", err
	}

	opts := []ndb.AppendOption{}

	if a.ID != "" && a.ID != "*" {
		opts = append(opts, ndb.WithEntryID(a.ID))
	}

	if a.NoMkStream {
		opts = append(opts, ndb.NoCreate())
	}

	switch {
	case a.MaxLen > 0:
		opts = append(opts, ndb.WithTrim(ndb.Trim{MaxLen: &a.MaxLen, Approx: a.Approx, Limit: trimLimit(a.Limit)}))
	case a.MinID != "":
		opts = append(opts, ndb.WithTrim(ndb.Trim{MinID: a.MinID, Approx: a.Approx, Limit: trimLimit(a.Limit)}))
	}

	s, err := ndb.StreamOf[map[string]any](c.db, a.Stream)

	if err != nil {
		return "", err
	}

	id, err := s.Append(ctx, entry, opts...)

	// NOMKSTREAM and no stream: Redis answers nil.
	if a.NoMkStream && noStream(err) {
		return "", redis.Nil
	}

	return id, redisError(err)
}

// XDel deletes entries by ID and gives how many it deleted (XDEL).
func (c *Client) XDel(ctx context.Context, stream string, ids ...string) *redis.IntCmd {
	cmd := redis.NewIntCmd(ctx, "xdel", stream)
	s := c.stream(stream)

	var deleted int64

	for _, id := range ids {
		n, err := s.Delete(ctx, id)

		if noStream(err) {
			break
		}

		if err != nil {
			cmd.SetErr(redisError(err))

			return cmd
		}

		deleted += int64(n)
	}

	cmd.SetVal(deleted)

	return cmd
}

// XLen gives the number of entries; 0 for a stream that does not exist
// (XLEN).
func (c *Client) XLen(ctx context.Context, stream string) *redis.IntCmd {
	cmd := redis.NewIntCmd(ctx, "xlen", stream)

	info, err := c.stream(stream).Info(ctx)

	if err != nil && !noStream(err) {
		cmd.SetErr(redisError(err))

		return cmd
	}

	cmd.SetVal(int64(info.Length))

	return cmd
}

// XRange gives every entry from start to stop, in ID order (XRANGE).
func (c *Client) XRange(ctx context.Context, stream, start, stop string) *redis.XMessageSliceCmd {
	return c.rangeCmd(ctx, "xrange", stream, start, stop, 0, false)
}

// XRangeN is XRange with at most count entries.
func (c *Client) XRangeN(ctx context.Context, stream, start, stop string, count int64) *redis.XMessageSliceCmd {
	if count <= 0 {
		return emptyRange(ctx, "xrange", stream)
	}

	return c.rangeCmd(ctx, "xrange", stream, start, stop, count, false)
}

// XRevRange gives every entry from start down to stop: start is the
// highest ID (XREVRANGE).
func (c *Client) XRevRange(ctx context.Context, stream, start, stop string) *redis.XMessageSliceCmd {
	return c.rangeCmd(ctx, "xrevrange", stream, stop, start, 0, true)
}

// XRevRangeN is XRevRange with at most count entries.
func (c *Client) XRevRangeN(ctx context.Context, stream, start, stop string, count int64) *redis.XMessageSliceCmd {
	if count <= 0 {
		return emptyRange(ctx, "xrevrange", stream)
	}

	return c.rangeCmd(ctx, "xrevrange", stream, stop, start, count, true)
}

// emptyRange is the answer of a range with COUNT 0.
func emptyRange(ctx context.Context, name, stream string) *redis.XMessageSliceCmd {
	cmd := redis.NewXMessageSliceCmd(ctx, name, stream)

	cmd.SetVal([]redis.XMessage{})

	return cmd
}

func (c *Client) rangeCmd(ctx context.Context, name, stream, low, high string, limit int64, rev bool) *redis.XMessageSliceCmd {
	cmd := redis.NewXMessageSliceCmd(ctx, name, stream)

	entries, err := c.entries(ctx, stream, low, high, int(limit), rev)

	if err != nil {
		cmd.SetErr(err)

		return cmd
	}

	cmd.SetVal(messages(entries))

	return cmd
}

// entries reads a range page by page: the entries from low to high, at
// most limit of them (0: all), from high down when rev is true. A stream
// that does not exist has no entries.
func (c *Client) entries(ctx context.Context, stream, low, high string, limit int, rev bool) ([]ndb.Entry[doc], error) {
	s := c.stream(stream)
	out := []ndb.Entry[doc]{}

	for {
		want := rangePage

		if limit > 0 {
			want = min(want, limit-len(out))
		}

		var (
			page []ndb.Entry[doc]
			err  error
		)

		if rev {
			page, err = s.RevRange(ctx, high, low, want)
		} else {
			page, err = s.Range(ctx, low, high, want)
		}

		if noStream(err) {
			return out, nil
		}

		if err != nil {
			return nil, redisError(err)
		}

		out = append(out, page...)

		if len(page) < want || (limit > 0 && len(out) >= limit) {
			return out, nil
		}

		// The next page starts after the last entry of this one.
		last := "(" + page[len(page)-1].ID

		if rev {
			high = last
		} else {
			low = last
		}
	}
}

// XTrimMaxLen keeps the newest maxLen entries and gives how many it
// deleted (XTRIM MAXLEN).
func (c *Client) XTrimMaxLen(ctx context.Context, key string, maxLen int64) *redis.IntCmd {
	return c.trim(ctx, key, "", ndb.Trim{MaxLen: &maxLen})
}

// XTrimMaxLenApprox is XTrimMaxLen with "~": ndb deletes only whole blocks
// of 1 024 entries, at most limit entries (0: no limit).
func (c *Client) XTrimMaxLenApprox(ctx context.Context, key string, maxLen, limit int64) *redis.IntCmd {
	return c.trim(ctx, key, "", ndb.Trim{MaxLen: &maxLen, Approx: true, Limit: trimLimit(limit)})
}

// XTrimMaxLenMode is XTrimMaxLen with a mode. Only KEEPREF is supported.
func (c *Client) XTrimMaxLenMode(ctx context.Context, key string, maxLen int64, mode string) *redis.IntCmd {
	return c.trim(ctx, key, mode, ndb.Trim{MaxLen: &maxLen})
}

// XTrimMaxLenApproxMode is XTrimMaxLenApprox with a mode. Only KEEPREF is
// supported.
func (c *Client) XTrimMaxLenApproxMode(ctx context.Context, key string, maxLen, limit int64, mode string) *redis.IntCmd {
	return c.trim(ctx, key, mode, ndb.Trim{MaxLen: &maxLen, Approx: true, Limit: trimLimit(limit)})
}

// XTrimMinID deletes the entries with an ID lower than minID (XTRIM
// MINID).
func (c *Client) XTrimMinID(ctx context.Context, key string, minID string) *redis.IntCmd {
	return c.trim(ctx, key, "", ndb.Trim{MinID: minID})
}

// XTrimMinIDApprox is XTrimMinID with "~".
func (c *Client) XTrimMinIDApprox(ctx context.Context, key string, minID string, limit int64) *redis.IntCmd {
	return c.trim(ctx, key, "", ndb.Trim{MinID: minID, Approx: true, Limit: trimLimit(limit)})
}

// XTrimMinIDMode is XTrimMinID with a mode. Only KEEPREF is supported.
func (c *Client) XTrimMinIDMode(ctx context.Context, key string, minID string, mode string) *redis.IntCmd {
	return c.trim(ctx, key, mode, ndb.Trim{MinID: minID})
}

// XTrimMinIDApproxMode is XTrimMinIDApprox with a mode. Only KEEPREF is
// supported.
func (c *Client) XTrimMinIDApproxMode(ctx context.Context, key string, minID string, limit int64, mode string) *redis.IntCmd {
	return c.trim(ctx, key, mode, ndb.Trim{MinID: minID, Approx: true, Limit: trimLimit(limit)})
}

func (c *Client) trim(ctx context.Context, key, mode string, t ndb.Trim) *redis.IntCmd {
	cmd := redis.NewIntCmd(ctx, "xtrim", key)

	if err := keepRef("XTRIM", mode); err != nil {
		cmd.SetErr(err)

		return cmd
	}

	n, err := c.stream(key).Trim(ctx, t)

	if err != nil && !noStream(err) {
		cmd.SetErr(redisError(err))

		return cmd
	}

	cmd.SetVal(int64(n))

	return cmd
}
