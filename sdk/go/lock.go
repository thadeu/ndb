package ndb

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"
)

// LockOption changes Lock.
type LockOption func(*lockOptions)

type lockOptions struct {
	ttl  *float64
	wait *float64
}

// LockTTL is how long the daemon keeps the lock when it is not released
// (default 30 s, up to 5 min). Set it over the longest time fn can take.
func LockTTL(d time.Duration) LockOption {
	return func(o *lockOptions) {
		secs := d.Seconds()
		o.ttl = &secs
	}
}

// LockWait is how long Lock waits for another holder (default 10 s, up to
// 1 min). Zero does not wait: a held document is ErrLocked at once.
func LockWait(d time.Duration) LockOption {
	return func(o *lockOptions) {
		secs := d.Seconds()
		o.wait = &secs
	}
}

// Lock takes the pessimistic lock of the document with this _id
// (POST /{coll}/_lock/{id}), calls fn with the document, writes the fields
// that fn changed, and releases the lock. While fn runs, no other caller
// holds the lock and no other write changes the document: another Lock
// waits (LockWait, then ErrLocked), and a plain write waits for the release.
// So fn runs once: use Lock when fn has an effect outside ndb (a payment, an
// email). When fn only changes the document, WithRetry is cheaper.
//
// An error from fn writes nothing and releases the lock. A lock that fn
// held for longer than LockTTL is gone: the write is refused with
// ErrLockExpired. A missing document is ErrNotFound.
//
//	p, err := payments.Lock(ctx, "p1", func(p *Payment) error {
//		if p.Status == "refunded" {
//			return nil
//		}
//
//		if err := gateway.Refund(p.ChargeID); err != nil {
//			return err
//		}
//
//		p.Status = "refunded"
//
//		return nil
//	})
func (c *Coll[T]) Lock(ctx context.Context, id string, fn func(doc *T) error, opts ...LockOption) (T, error) {
	var (
		o    lockOptions
		zero T
	)

	for _, opt := range opts {
		opt(&o)
	}

	body, err := json.Marshal(map[string]*float64{"ttl": o.ttl, "wait": o.wait})
	if err != nil {
		return zero, err
	}

	var held struct {
		Token string          `json:"token"`
		Doc   json.RawMessage `json:"doc"`
	}

	if err := c.call(ctx, http.MethodPost, docPath("_lock", id), body, &held); err != nil {
		return zero, err
	}

	defer c.unlock(id, held.Token)

	doc, err := c.decode(held.Doc)
	if err != nil {
		return zero, err
	}

	out, err := c.change(ctx, id, doc, fn, heldBy(held.Token))
	if errors.Is(err, errUnmatched) {
		return zero, ErrNotFound
	}

	return out, err
}

// unlock releases the lock. It runs after ctx may be done, so it has its
// own short deadline; a lock that it cannot release ends with its ttl.
func (c *Coll[T]) unlock(id, token string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	body, _ := json.Marshal(map[string]string{"token": token})

	_ = c.call(ctx, http.MethodPost, docPath("_unlock", id), body, nil)
}

// heldBy sends an Update as the holder of a lock (?lock=<token>).
func heldBy(token string) Option {
	return func(o *options) {
		o.lock = token
	}
}

// lockQuery is "?lock=<token>" for the write of a holder, or "".
func lockQuery(token string) string {
	if token == "" {
		return ""
	}

	return "?lock=" + url.QueryEscape(token)
}
