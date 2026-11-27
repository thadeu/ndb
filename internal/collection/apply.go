package collection

import (
	"context"
	"errors"

	"github.com/thadeu/ndb/internal/wal"
)

// Apply writes the ops of a record of another node (replication) as they
// are: the same _id, _v and document bytes, with no encryption, blob or
// unique step (the owner did them). It goes through the writer, so the ops
// are in the WAL and fsynced before Apply returns, and in one record, so
// a reader sees all of them or none.
//
// An op that the collection already has is skipped, so a record applied
// twice (a replay after a crash) gives the same state: a put whose _id is
// live with the same or a newer _v, a del of an _id that is not live or is
// newer. It returns how many ops it wrote.
func (c *Collection) Apply(ctx context.Context, ops []wal.Op) (int, error) {
	if len(ops) == 0 {
		return 0, nil
	}

	_, written, err := c.w.Commit(ctx, applyReq{ops: ops})

	if errors.Is(err, errNoWrite) {
		return 0, nil
	}

	return len(written), err
}

type applyReq struct {
	ops []wal.Op
}

// prepareApply runs in the writer goroutine.
func (c *Collection) prepareApply(v *view, r applyReq) ([]wal.Op, error) {
	var out []wal.Op

	for _, op := range r.ops {
		cur, live := v.get(op.ID)

		switch op.Kind {
		case wal.Put:
			if live && cur.v >= op.V {
				continue
			}
		case wal.Del:
			if !live || cur.v >= op.V {
				continue
			}
		default:
			continue
		}

		v.apply(op)
		out = append(out, op)
	}

	if len(out) == 0 {
		return nil, errNoWrite
	}

	return out, nil
}
