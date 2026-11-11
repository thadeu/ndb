package collection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/thadeu/ndb/internal/query"
	"github.com/thadeu/ndb/internal/wal"
)

// A transaction of one collection (POST /{coll}/_tx): inserts, updates and
// deletes that are written in one WAL record, so all of them or none. It
// runs in the writer goroutine, on one view: an op sees the ops before it
// (an insert, then an update of the same document).
//
// An op that fails fails the whole transaction, and nothing is written. An
// update or a delete that matches nothing (no such id, or its guard does
// not match) is tx_conflict. The locks of the transaction (its tokens) must
// all still hold (lock_expired otherwise); a document that another caller
// holds is *locks.Busy, and the engine waits as for any write.

// TxOp is one write of a transaction.
type TxOp struct {
	Op     string          `json:"op"`
	ID     string          `json:"id"`
	Doc    json.RawMessage `json:"doc"`
	Query  map[string]any  `json:"query"`
	Update map[string]any  `json:"update"`
}

// TxResult is the answer of one op: the id and its new version, or
// Deleted for a delete.
type TxResult struct {
	ID      string `json:"_id"`
	V       uint64 `json:"_v,omitempty"`
	Deleted int    `json:"deleted,omitempty"`
}

type txReq struct {
	ops    []TxOp
	tokens []string
	out    *[]TxResult
}

// txOpError is the error of one op, with its index in the transaction.
type txOpError struct {
	index int
	err   error
}

func (e *txOpError) Error() string {
	return fmt.Sprintf("op %d: %v", e.index, e.err)
}

func (e *txOpError) Unwrap() error {
	return e.err
}

// Code is the code of the error of the op.
func (e *txOpError) Code() string {
	var coded interface{ Code() string }

	if errors.As(e.err, &coded) {
		return coded.Code()
	}

	return "internal"
}

// Tx writes ops in one step, as the holder of tokens.
func (c *Collection) Tx(ctx context.Context, ops []TxOp, tokens []string) ([]TxResult, error) {
	if len(ops) == 0 {
		return nil, errf("invalid_request", "a transaction needs ops")
	}

	if err := c.waitRoom(ctx); err != nil {
		return nil, err
	}

	var out []TxResult

	_, _, err := c.w.Commit(ctx, txReq{ops: ops, tokens: tokens, out: &out})
	if err != nil {
		return nil, err
	}

	return out, nil
}

// prepareTx runs in the writer goroutine.
func (c *Collection) prepareTx(v *view, r txReq) ([]wal.Op, error) {
	vq, err := c.visibility(c.clock())
	if err != nil {
		return nil, err
	}

	var (
		ops []wal.Op
		out []TxResult
	)

	for i, op := range r.ops {
		written, res, err := c.prepareTxOp(v, op, vq)
		if err != nil {
			return nil, &txOpError{index: i, err: err}
		}

		ops = append(ops, written...)
		out = append(out, res)
	}

	ids := make([]string, 0, len(ops))

	for _, op := range ops {
		ids = append(ids, op.ID)
	}

	if err := c.locks.CheckTx(ids, r.tokens); err != nil {
		return nil, err
	}

	*r.out = out

	if len(ops) == 0 {
		return nil, errNoWrite
	}

	return ops, nil
}

func (c *Collection) prepareTxOp(v *view, op TxOp, vq *query.Query) ([]wal.Op, TxResult, error) {
	switch op.Op {
	case "insert":
		if len(op.Doc) == 0 {
			return nil, TxResult{}, errf("invalid_request", "insert needs doc")
		}

		ops, err := c.prepareInsert(v, insertReq{docs: [][]byte{op.Doc}})
		if err != nil {
			return nil, TxResult{}, err
		}

		return ops, TxResult{ID: ops[0].ID, V: ops[0].V}, nil
	case "update":
		return c.prepareTxUpdate(v, op, vq)
	case "delete":
		return c.prepareTxDelete(v, op, vq)
	}

	return nil, TxResult{}, errf("invalid_request", "op must be insert, update or delete, not %q", op.Op)
}

func (c *Collection) prepareTxUpdate(v *view, op TxOp, vq *query.Query) ([]wal.Op, TxResult, error) {
	if op.ID == "" {
		return nil, TxResult{}, errf("invalid_request", "update needs id")
	}

	if len(op.Update) == 0 {
		return nil, TxResult{}, errf("no_update", "update needs operators")
	}

	top, err := JoinUpdate(withID(op.Query, op.ID), op.Update)
	if err != nil {
		return nil, TxResult{}, err
	}

	filterMap, fops, err := splitBody(top)
	if err != nil {
		return nil, TxResult{}, err
	}

	filter, err := c.parseFilter(filterMap)
	if err != nil {
		return nil, TxResult{}, err
	}

	res := &UpdateResult{}

	ops, err := c.updateOne(v, updateReq{filterMap: filterMap, filter: filter, ops: fops, out: res}, vq)
	if err != nil {
		return nil, TxResult{}, err
	}

	if res.Matched == 0 {
		return nil, TxResult{}, errf("tx_conflict", "%s matched nothing (no such document, or the query does not match)", op.ID)
	}

	cur, _ := v.get(op.ID)

	return ops, TxResult{ID: op.ID, V: cur.v}, nil
}

func (c *Collection) prepareTxDelete(v *view, op TxOp, vq *query.Query) ([]wal.Op, TxResult, error) {
	if op.ID == "" {
		return nil, TxResult{}, errf("invalid_request", "delete needs id")
	}

	filter, err := c.parseFilter(withID(op.Query, op.ID))
	if err != nil {
		return nil, TxResult{}, err
	}

	ops, err := c.deleteOne(v, deleteReq{filter: filter}, vq)
	if err != nil {
		return nil, TxResult{}, err
	}

	if len(ops) == 0 {
		return nil, TxResult{}, errf("tx_conflict", "%s matched nothing (no such document, or the query does not match)", op.ID)
	}

	return ops, TxResult{ID: op.ID, Deleted: 1}, nil
}

// withID is the query of an op with its id, without changing the caller's
// map.
func withID(q map[string]any, id string) map[string]any {
	m := make(map[string]any, len(q)+1)

	for k, val := range q {
		m[k] = val
	}

	m[idField] = id

	return m
}
