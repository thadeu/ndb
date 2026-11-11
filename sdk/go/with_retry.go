package ndb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"net/http"
	"reflect"
	"time"
)

// ErrStaleObject is the error of WithRetry when every try lost the race: the
// document changed between the read and the write each time.
var ErrStaleObject = &Error{Code: "stale_object", Message: "the document changed on every try"}

const defaultRetries = 5

// RetryOption changes WithRetry.
type RetryOption func(*retryOptions)

type retryOptions struct {
	retries int
}

// Retries sets how many times WithRetry tries (default 5).
func Retries(n int) RetryOption {
	return func(o *retryOptions) {
		o.retries = max(n, 1)
	}
}

// WithRetry reads the document with this _id, calls fn with it, and writes
// the fields that fn changed, guarded by the version it read (optimistic
// lock). When another write came first, it reads the document again and
// calls fn again, up to Retries times; then it returns ErrStaleObject.
// An error from fn stops it and nothing is written. A missing document is
// ErrNotFound. It returns the document as written.
//
//	w, err := wallets.WithRetry(ctx, "w1", func(w *Wallet) error {
//		if w.Balance < 500 {
//			return ErrInsufficientFunds
//		}
//
//		w.Balance -= 500
//
//		return nil
//	})
func (c *Coll[T]) WithRetry(ctx context.Context, id string, fn func(doc *T) error, opts ...RetryOption) (T, error) {
	o := retryOptions{retries: defaultRetries}

	for _, opt := range opts {
		opt(&o)
	}

	var zero T

	for try := range o.retries {
		if try > 0 {
			if err := retryBackoff(ctx, try); err != nil {
				return zero, err
			}
		}

		doc, written, err := c.retryOnce(ctx, id, fn)
		if err != nil || written {
			return doc, err
		}
	}

	return zero, ErrStaleObject
}

// retryOnce is one try: read, fn, guarded write. written is false when the
// guard failed (another write came first).
func (c *Coll[T]) retryOnce(ctx context.Context, id string, fn func(doc *T) error) (T, bool, error) {
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

	out, err := c.change(ctx, id, doc, fn, Guard(Eq("_v", head.V)))
	if errors.Is(err, errUnmatched) {
		return zero, false, nil
	}

	return out, err == nil, err
}

// errUnmatched is a write of change that matched nothing: its guard failed.
var errUnmatched = errors.New("ndb: the write matched nothing")

// change calls fn with doc and writes the top-level fields that fn changed,
// in one Update with opt (a guard, or the token of a lock). It returns the
// document as written, or doc when fn changed nothing.
func (c *Coll[T]) change(ctx context.Context, id string, doc T, fn func(doc *T) error, opt Option) (T, error) {
	var zero T

	before, err := c.fields(doc)
	if err != nil {
		return zero, err
	}

	if err := fn(&doc); err != nil {
		return zero, err
	}

	after, err := c.fields(doc)
	if err != nil {
		return zero, err
	}

	ops := changes(before, after)
	if len(ops) == 0 {
		return doc, nil
	}

	res, err := c.Update(ctx, id, ops, opt)
	if err != nil {
		return zero, err
	}

	if res.Matched == 0 {
		return zero, errUnmatched
	}

	return *res.Doc, nil
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

// retryBackoff waits a few ms with jitter, longer on each try, so racing
// writers do not collide again at once.
func retryBackoff(ctx context.Context, try int) error {
	d := time.Duration(try*5+rand.IntN(10)) * time.Millisecond

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
