package collection

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/wal"
)

// A check runs every test of an insert or an upsert (the ids, the unique
// values, the expiry field, the document size) and writes nothing. A
// partitioned _bulk checks each partition first, so it writes all of its
// documents or none (internal/server/partition.go). A write that runs
// between the check and the real call can still make the real call fail.

// errChecked ends a check in the writer goroutine: prepare found no error.
var errChecked = errors.New("collection: checked")

// checkReq is an insertReq that the writer prepares and does not write.
type checkReq struct {
	insertReq
}

// CheckInsert tells whether Insert (Upsert with upsert, InsertIn with
// expireIn) of docs would pass now. It runs in the writer goroutine, after
// the writes queued before it.
func (c *Collection) CheckInsert(ctx context.Context, expireIn time.Duration, upsert bool, docs ...[]byte) error {
	if expireIn > 0 {
		if _, err := c.expiryField(); err != nil {
			return err
		}
	}

	req := checkReq{insertReq{docs: docs, expireIn: expireIn, upsert: upsert, dry: true}}

	_, _, err := c.w.Commit(ctx, req)

	if errors.Is(err, errChecked) {
		return nil
	}

	return err
}

// CheckNew is CheckInsert on a new, empty collection of def.
func CheckNew(def catalog.Def, expireIn time.Duration, upsert bool, docs ...[]byte) error {
	c := &Collection{def: def, head: newState(def.Unique), maxLine: maxLineOf(def)}

	if expireIn > 0 {
		if _, err := c.expiryField(); err != nil {
			return err
		}
	}

	ops, err := c.prepareInsert(newView(c.head), insertReq{docs: docs, expireIn: expireIn, upsert: upsert, dry: true})

	if err != nil {
		return err
	}

	return c.checkSize(ops, true)
}

// UniqueKey is the value of a unique field as the unique check compares it.
// ok is false for a missing or null value, which is never a duplicate.
func UniqueKey(doc json.RawMessage, path string) (string, bool) {
	return uniqueKey(doc, path)
}

// maxLineOf is max_line_size, or the default.
func maxLineOf(def catalog.Def) int {
	if def.MaxLineSize == 0 {
		return defaultMaxLine
	}

	return int(def.MaxLineSize)
}

// walLineOf is the limit of one WAL line: room for a document of
// max_line_size and the JSON of its record (tx, time, op, id), and for the
// many small documents of a _bulk body of max_line_size.
func walLineOf(maxLine int) int {
	return 2*maxLine + 64<<10
}

// checkSize fails when a written document is over max_line_size. In a
// dry run with blob_threshold the large strings are still in the document,
// so the check waits for the real write.
func (c *Collection) checkSize(ops []wal.Op, dry bool) error {
	if dry && c.def.BlobThreshold > 0 {
		return nil
	}

	for _, op := range ops {
		if op.Kind == wal.Put && len(op.Doc) > c.maxLine {
			return errf("line_too_large", "the document %s is %d bytes, over max_line_size (%d)", op.ID, len(op.Doc), c.maxLine)
		}
	}

	return nil
}
