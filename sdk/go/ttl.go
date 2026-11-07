package ndb

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// Expire makes the document expire d from now, like EXPIRE in Redis, and
// returns the new version (POST /{coll}/_expire/{id}). A missing or expired
// document is ErrNotFound. The collection needs a field tagged `expires`
// (ErrTTLNotEnabled), and an expiry that comes from ttl + time cannot be set
// (ErrTTLDerived).
func (c *Coll[T]) Expire(ctx context.Context, id string, d time.Duration) (uint64, error) {
	return c.writeVersion(ctx, "_expire", id, expireBody{In: ttlSecs(d.Seconds()).seconds()})
}

// ExpireAt makes the document expire at t.
func (c *Coll[T]) ExpireAt(ctx context.Context, id string, t time.Time) (uint64, error) {
	return c.writeVersion(ctx, "_expire", id, expireBody{At: t.UTC().Format(time.RFC3339Nano)})
}

// Persist removes the expiry of the document, like PERSIST in Redis, and
// returns the new version (POST /{coll}/_persist/{id}).
func (c *Coll[T]) Persist(ctx context.Context, id string) (uint64, error) {
	return c.writeVersion(ctx, "_persist", id, nil)
}

func (c *Coll[T]) writeVersion(ctx context.Context, action, id string, body any) (uint64, error) {
	var raw []byte

	if body != nil {
		var err error

		if raw, err = json.Marshal(body); err != nil {
			return 0, err
		}
	}

	var res Result

	return res.V, c.call(ctx, http.MethodPost, docPath(action, id), raw, &res)
}

// TTL is the time left of the document (GET /{coll}/_ttl/{id}). The bool is
// false when the document never expires. A missing or expired document is
// ErrNotFound. The time is rounded up to whole seconds.
func (c *Coll[T]) TTL(ctx context.Context, id string) (time.Duration, bool, error) {
	var out struct {
		TTL int64 `json:"ttl"`
	}

	if err := c.call(ctx, http.MethodGet, docPath("_ttl", id), nil, &out); err != nil {
		return 0, false, err
	}

	if out.TTL < 0 {
		return 0, false, nil
	}

	return time.Duration(out.TTL) * time.Second, true, nil
}

type expireBody struct {
	In *float64 `json:"in,omitempty"`
	At string   `json:"at,omitempty"`
}
