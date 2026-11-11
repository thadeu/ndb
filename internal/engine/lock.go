package engine

import (
	"context"
	"errors"
	"time"

	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/locks"
)

// write runs one write of an engine collection. When the document is
// locked by another caller (*locks.Busy), nothing was written: write waits
// for that lease to end, with the collection not held, and runs fn again.
// The wait ends with the lease (its ttl bounds it) or with ctx.
func write[T any](ctx context.Context, c *Collection, fn func(eng *collection.Collection) (T, error)) (T, error) {
	for {
		out, err := writeOnce(c, fn)

		var busy *locks.Busy

		if !errors.As(err, &busy) {
			return out, err
		}

		if err := busy.Wait(ctx); err != nil {
			var zero T

			return zero, err
		}
	}
}

func writeOnce[T any](c *Collection, fn func(eng *collection.Collection) (T, error)) (T, error) {
	if err := c.enter(); err != nil {
		var zero T

		return zero, err
	}

	defer c.leave()

	if c.eng == nil {
		var zero T

		return zero, ErrDirect
	}

	return fn(c.eng)
}

// Lock takes the pessimistic locks of the documents ids for ttl, all under
// one token, and returns them with the documents. When another caller holds
// one of them, Lock takes none and waits up to wait for that lease to end;
// then it is *locks.Busy ("locked").
func (c *Collection) Lock(ctx context.Context, ids []string, ttl, wait time.Duration) (collection.Locked, error) {
	deadline := time.Now().Add(wait)

	for {
		out, err := writeOnce(c, func(eng *collection.Collection) (collection.Locked, error) {
			return eng.Lock(ctx, ids, ttl)
		})

		var busy *locks.Busy

		if !errors.As(err, &busy) {
			return out, err
		}

		if !time.Now().Before(deadline) {
			return collection.Locked{}, err
		}

		wctx, cancel := context.WithDeadline(ctx, deadline)
		werr := busy.Wait(wctx)

		cancel()

		if werr != nil && ctx.Err() != nil {
			return collection.Locked{}, ctx.Err()
		}
	}
}

// Tx writes ops in one WAL record, as the holder of tokens (see
// collection.Tx). It waits, as a plain write does, for a document that
// another caller holds. With unlock, the locks of tokens are released after
// the call, whether it committed or not.
func (c *Collection) Tx(ctx context.Context, ops []collection.TxOp, tokens []string, unlock bool) ([]collection.TxResult, error) {
	if unlock {
		defer c.db.locks.Scope(c.name).ReleaseAll(tokens)
	}

	return write(ctx, c, func(eng *collection.Collection) ([]collection.TxResult, error) {
		return eng.Tx(ctx, ops, tokens)
	})
}

// UnlockToken releases every lock of token and gives how many ids it held.
func (c *Collection) UnlockToken(token string) (int, error) {
	if !c.engine() {
		return 0, ErrDirect
	}

	return c.db.locks.Scope(c.name).ReleaseToken(token)
}

// Unlock releases the lock that token holds on id (locks.ErrExpired when it
// holds none). It does not wake a sleeping collection.
func (c *Collection) Unlock(id, token string) error {
	if !c.engine() {
		return ErrDirect
	}

	return c.db.locks.Scope(c.name).Release(id, token)
}
