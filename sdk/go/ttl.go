package ndb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Expire makes the document expire d from now, like EXPIRE in Redis, and
// returns the new version. A missing or expired document is ErrNotFound.
// The collection needs a field tagged `expires` (ErrTTLNotEnabled), and an
// expiry that comes from ttl + time cannot be set (ErrTTLDerived).
func (c *Coll[T]) Expire(ctx context.Context, id string, d time.Duration) (uint64, error) {
	return c.writeVersion(ctx, "expire", expireBody{ID: id, In: ttlSecs(d.Seconds()).seconds()})
}

// ExpireAt makes the document expire at t.
func (c *Coll[T]) ExpireAt(ctx context.Context, id string, t time.Time) (uint64, error) {
	return c.writeVersion(ctx, "expire", expireBody{ID: id, At: t.UTC().Format(time.RFC3339Nano)})
}

// Persist removes the expiry of the document, like PERSIST in Redis, and
// returns the new version.
func (c *Coll[T]) Persist(ctx context.Context, id string) (uint64, error) {
	return c.writeVersion(ctx, "persist", map[string]string{"_id": id})
}

func (c *Coll[T]) writeVersion(ctx context.Context, op string, body any) (uint64, error) {
	resp, err := c.post(ctx, op, body)
	if err != nil {
		return 0, err
	}

	defer resp.Body.Close()

	var res writeResult

	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return 0, fmt.Errorf("ndb: %s response: %w", op, err)
	}

	return res.V, nil
}

// TTL is the time left of the document. The bool is false when the document
// never expires. A missing or expired document is ErrNotFound. The time is
// rounded up to whole seconds.
func (c *Coll[T]) TTL(ctx context.Context, id string) (time.Duration, bool, error) {
	resp, err := c.db.do(ctx, http.MethodGet, c.idPath(id)+"/ttl", nil)
	if err != nil {
		return 0, false, err
	}

	defer resp.Body.Close()

	var out struct {
		TTL int64 `json:"ttl"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, false, fmt.Errorf("ndb: ttl response: %w", err)
	}

	if out.TTL < 0 {
		return 0, false, nil
	}

	return time.Duration(out.TTL) * time.Second, true, nil
}

type expireBody struct {
	ID string   `json:"_id"`
	In *float64 `json:"in,omitempty"`
	At string   `json:"at,omitempty"`
}
