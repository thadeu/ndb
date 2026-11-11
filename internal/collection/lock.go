package collection

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/thadeu/ndb/internal/locks"
	"github.com/thadeu/ndb/internal/wal"
)

// The pessimistic lock of a document (POST /{coll}/_lock/{id}). A lock is a
// lease with a token: until it is released or expires, only a write that
// carries the token changes the document.
//
// The lock is taken in the writer goroutine, between two writes. So the
// document it returns is the newest one, and every later write is checked
// against the lock: no write slips in between the read and the lock.
//
// A write of a locked document without the token gets *locks.Busy. The
// engine waits for the lease to end and sends the write again; that is
// safe because a refused write wrote nothing. A many write is the
// exception: its earlier records are written, so it stops with "locked".
// The expiry sweep (TTL) does not look at locks.

// Locked is a held lock and the documents as they are now, with "_v", in
// the order of the ids.
type Locked struct {
	Token   string
	Expires time.Time
	Docs    []json.RawMessage
}

type lockReq struct {
	ids []string
	ttl time.Duration
	out *Locked
}

// guarded is a write request with the lock token of its caller ("" for
// none). prepare checks its ops against the locks.
type guarded struct {
	req   any
	token string
}

// Lock takes the locks of the visible documents ids for ttl, all under one
// token, or none of them. An id that another caller holds is *locks.Busy;
// a missing one is not_found.
func (c *Collection) Lock(ctx context.Context, ids []string, ttl time.Duration) (Locked, error) {
	out := &Locked{}

	_, _, err := c.w.Commit(ctx, lockReq{ids: ids, ttl: ttl, out: out})
	if errors.Is(err, errNoWrite) {
		return *out, nil
	}

	return Locked{}, err
}

// prepareLock runs in the writer goroutine. It writes nothing.
func (c *Collection) prepareLock(v *view, r lockReq) error {
	ids := make([]string, 0, len(r.ids))
	docs := make([]json.RawMessage, 0, len(r.ids))

	for _, id := range r.ids {
		if slices.Contains(ids, id) {
			continue
		}

		cur, err := current(v, id)
		if err != nil {
			return err
		}

		raw, err := cur.raw()
		if err != nil {
			return err
		}

		if !c.visible(raw) {
			return errf("not_found", "%s", id)
		}

		ids = append(ids, id)
		docs = append(docs, withVersion(raw, cur.v))
	}

	lease, err := c.locks.TryAcquireSet(ids, r.ttl)
	if err != nil {
		return err
	}

	*r.out = Locked{Token: lease.Token, Expires: lease.Expires, Docs: docs}

	return errNoWrite
}

// commit sends a write to the writer with the lock token of ctx.
func (c *Collection) commit(ctx context.Context, req any) (uint64, []wal.Op, error) {
	return c.w.Commit(ctx, guarded{req: req, token: locks.TokenOf(ctx)})
}

// checkLocks refuses ops on a document that another caller holds, and ops
// of a caller whose token holds no lock on the document.
func (c *Collection) checkLocks(ops []wal.Op, token string) error {
	if token == "" && !c.locks.Any() {
		return nil
	}

	for _, op := range ops {
		if err := c.locks.Check(op.ID, token); err != nil {
			return err
		}
	}

	return nil
}

// manyLocked makes the *locks.Busy of a many write final: the records
// before it are written, so the write cannot wait and run again.
func manyLocked(err error, many bool) error {
	var busy *locks.Busy

	if many && errors.As(err, &busy) {
		return errf("locked", "%s is locked; the documents before it are written", busy.ID)
	}

	return err
}
