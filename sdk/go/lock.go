package ndb

import (
	"bytes"
	"context"
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"reflect"
	"time"
)

// ErrStaleObject is the error of WithLock when every try lost the race: the
// document changed between the read and the write each time.
var ErrStaleObject = &Error{Code: "stale_object", Message: "the document changed on every try"}

const defaultLockRetries = 5

// LockOption changes WithLock.
type LockOption func(*lockOptions)

type lockOptions struct {
	retries int
}

// LockRetries sets how many times WithLock tries (default 5).
func LockRetries(n int) LockOption {
	return func(o *lockOptions) {
		o.retries = max(n, 1)
	}
}

// WithLock reads the document with this _id, calls fn with it, and writes
// the fields that fn changed, guarded by the version it read (optimistic
// lock). When another write came first, it reads the document again and
// calls fn again, up to LockRetries times; then it returns ErrStaleObject.
// An error from fn stops it and nothing is written. A missing document is
// ErrNotFound. It returns the document as written.
//
//	w, err := wallets.WithLock(ctx, "w1", func(w *Wallet) error {
//		if w.Balance < 500 {
//			return ErrInsufficientFunds
//		}
//
//		w.Balance -= 500
//
//		return nil
//	})
func (c *Coll[T]) WithLock(ctx context.Context, id string, fn func(doc *T) error, opts ...LockOption) (T, error) {
	o := lockOptions{retries: defaultLockRetries}

	for _, opt := range opts {
		opt(&o)
	}

	var zero T

	for try := range o.retries {
		if try > 0 {
			if err := lockBackoff(ctx, try); err != nil {
				return zero, err
			}
		}

		doc, written, err := c.lockOnce(ctx, id, fn)
		if err != nil || written {
			return doc, err
		}
	}

	return zero, ErrStaleObject
}

// lockOnce is one try: read, fn, guarded write. written is false when the
// guard failed (another write came first).
func (c *Coll[T]) lockOnce(ctx context.Context, id string, fn func(doc *T) error) (T, bool, error) {
	var zero T

	var raw json.RawMessage

	if err := c.call(ctx, http.MethodGet, docPath("_doc", id), nil, &raw); err != nil {
		return zero, false, err
	}

	var head struct {
		V uint64 `json:"_v"`
	}

	if err := json.Unmarshal(raw, &head); err != nil {
		return zero, false, err
	}

	doc, err := c.decode(raw)
	if err != nil {
		return zero, false, err
	}

	before, err := c.fields(doc)
	if err != nil {
		return zero, false, err
	}

	if err := fn(&doc); err != nil {
		return zero, false, err
	}

	after, err := c.fields(doc)
	if err != nil {
		return zero, false, err
	}

	ops := changes(before, after)
	if len(ops) == 0 {
		return doc, true, nil
	}

	res, err := c.Update(ctx, id, ops, Guard(Eq("_v", head.V)))
	if err != nil || res.Matched == 0 {
		return zero, false, err
	}

	return *res.Doc, true, nil
}

// fields is the document as the daemon would store it, by top-level field.
// Numbers stay json.Number, so a big integer is written back exactly.
func (c *Coll[T]) fields(doc T) (map[string]any, error) {
	raw, err := c.schema.encode(doc)
	if err != nil {
		return nil, err
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	var m map[string]any

	return m, dec.Decode(&m)
}

// changes are the $set and $unset of the top-level fields that differ.
func changes(before, after map[string]any) Ops {
	set, unset := map[string]any{}, map[string]any{}

	for k, v := range after {
		if k == "_id" {
			continue
		}

		if old, ok := before[k]; !ok || !reflect.DeepEqual(old, v) {
			set[k] = v
		}
	}

	for k := range before {
		if _, ok := after[k]; !ok && k != "_id" {
			unset[k] = ""
		}
	}

	ops := Ops{}

	if len(set) > 0 {
		ops["$set"] = set
	}

	if len(unset) > 0 {
		ops["$unset"] = unset
	}

	return ops
}

// lockBackoff waits a few ms with jitter, longer on each try, so racing
// writers do not collide again at once.
func lockBackoff(ctx context.Context, try int) error {
	d := time.Duration(try*5+rand.IntN(10)) * time.Millisecond

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
