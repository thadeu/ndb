package ndb

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
)

// ErrRollback ends a Tx block: nothing is written, the locks are released,
// and Tx returns nil.
var ErrRollback = errors.New("ndb: rollback")

// Tx collects the writes of a transaction block. Nothing is sent until the
// block returns nil; then every write goes in one call
// (POST /{coll}/_tx), all of them or none.
type Tx[T any] struct {
	c      *Coll[T]
	ops    []txOp
	before map[string]map[string]any
	err    error
}

type txOp struct {
	Op     string          `json:"op"`
	ID     string          `json:"id,omitempty"`
	Doc    json.RawMessage `json:"doc,omitempty"`
	Query  map[string]any  `json:"query,omitempty"`
	Update Ops             `json:"update,omitempty"`
}

// Tx runs fn as one transaction of this collection. With ids, it first
// locks those documents, all of them or none (POST /{coll}/_lock), and
// gives them to fn, read as they are under the lock. fn records the writes
// with tx.Insert, tx.Update and tx.Delete; they are written when fn returns
// nil, all of them or none, and the locks are released.
//
// An error from fn writes nothing; ErrRollback writes nothing and Tx returns
// nil. A refused commit writes nothing: ErrTxConflict when an update or a
// delete matches nothing (a missing document, or a stale version),
// ErrLockExpired when the locks outlived LockTTL (default 30 s: the time
// limit of the transaction). LockWait is how long to wait for a document
// that another caller holds (then ErrLocked). Reads inside fn see the
// stored data, not the writes of the block.
//
//	err := accounts.Tx(ctx, []string{"a1", "a2"}, func(tx *ndb.Tx[Account], docs map[string]*Account) error {
//		if docs["a1"].Balance < amount {
//			return ndb.ErrRollback
//		}
//
//		docs["a1"].Balance -= amount
//		docs["a2"].Balance += amount
//
//		tx.Update(docs["a1"])
//		tx.Update(docs["a2"])
//
//		return nil
//	}, ndb.LockTTL(5*time.Second))
func (c *Coll[T]) Tx(ctx context.Context, ids []string, fn func(tx *Tx[T], docs map[string]*T) error, opts ...LockOption) error {
	tx := &Tx[T]{c: c, before: map[string]map[string]any{}}
	docs := map[string]*T{}

	var tokens []string

	if len(ids) > 0 {
		token, err := tx.lock(ctx, ids, docs, opts)
		if err != nil {
			return err
		}

		tokens = []string{token}
	}

	if err := fn(tx, docs); err != nil {
		c.unlockSet(tokens)

		if errors.Is(err, ErrRollback) {
			return nil
		}

		return err
	}

	if tx.err != nil {
		c.unlockSet(tokens)

		return tx.err
	}

	if len(tx.ops) == 0 {
		c.unlockSet(tokens)

		return nil
	}

	body, err := json.Marshal(map[string]any{"ops": tx.ops, "locks": tokens, "unlock": true})
	if err != nil {
		c.unlockSet(tokens)

		return err
	}

	return c.call(ctx, http.MethodPost, "_tx", body, nil)
}

// lock takes the locks of ids and fills docs. It keeps each document as it
// was, so Update writes only the fields that the block changed.
func (tx *Tx[T]) lock(ctx context.Context, ids []string, docs map[string]*T, opts []LockOption) (string, error) {
	var o lockOptions

	for _, opt := range opts {
		opt(&o)
	}

	body, err := json.Marshal(map[string]any{"ids": ids, "ttl": o.ttl, "wait": o.wait})
	if err != nil {
		return "", err
	}

	var held struct {
		Token string            `json:"token"`
		Docs  []json.RawMessage `json:"docs"`
	}

	if err := tx.c.call(ctx, http.MethodPost, "_lock", body, &held); err != nil {
		return "", err
	}

	for _, raw := range held.Docs {
		doc, err := tx.c.decode(raw)
		if err != nil {
			tx.c.unlockSet([]string{held.Token})

			return "", err
		}

		fields, err := tx.c.fields(doc)
		if err != nil {
			tx.c.unlockSet([]string{held.Token})

			return "", err
		}

		id, _ := fields[idField].(string)

		tx.before[id] = fields
		docs[id] = &doc
	}

	return held.Token, nil
}

// Insert records the insert of doc. A doc without _id gets one from the
// daemon.
func (tx *Tx[T]) Insert(doc T) {
	raw, err := tx.c.schema.encode(doc)
	if err != nil {
		tx.fail(err)

		return
	}

	tx.ops = append(tx.ops, txOp{Op: "insert", Doc: raw})
}

// Update records the write of doc. A document that the transaction locked
// gets the fields that changed since the lock. Any other document gets all
// its fields, guarded by its version (the field tagged `version`) when it
// has one: a document that changed since it was read fails the
// transaction (ErrTxConflict).
func (tx *Tx[T]) Update(doc *T) {
	after, err := tx.c.fields(*doc)
	if err != nil {
		tx.fail(err)

		return
	}

	id, _ := after[idField].(string)
	if id == "" {
		tx.fail(errors.New("ndb: Tx.Update of a document without _id"))

		return
	}

	op := txOp{Op: "update", ID: id}

	if before, ok := tx.before[id]; ok {
		op.Update = changes(before, after)
		tx.before[id] = after
	} else {
		delete(after, idField)

		op.Update = Ops{"$set": after}

		if v := tx.c.versionOf(*doc); v > 0 {
			op.Query = map[string]any{"_v": v}
		}
	}

	if len(op.Update) == 0 {
		return
	}

	tx.ops = append(tx.ops, op)
}

// Delete records the delete of the document with this _id. A missing
// document fails the transaction (ErrTxConflict).
func (tx *Tx[T]) Delete(id string) {
	tx.ops = append(tx.ops, txOp{Op: "delete", ID: id})
}

func (tx *Tx[T]) fail(err error) {
	if tx.err == nil {
		tx.err = err
	}
}

// versionOf is the value of the field tagged `version`, or 0.
func (c *Coll[T]) versionOf(doc T) uint64 {
	if c.schema.version == "" {
		return 0
	}

	rv := reflect.ValueOf(doc)

	for _, f := range c.schema.fields {
		if f.name != c.schema.version {
			continue
		}

		if fv := rv.Field(f.index); fv.CanUint() {
			return fv.Uint()
		}
	}

	return 0
}

// unlockSet releases the locks of tokens. A lock that it cannot release
// ends with its ttl.
func (c *Coll[T]) unlockSet(tokens []string) {
	for _, token := range tokens {
		ctx, cancel := context.WithTimeout(context.Background(), unlockTimeout)

		body, _ := json.Marshal(map[string]string{"token": token})

		_ = c.call(ctx, http.MethodPost, "_unlock", body, nil)

		cancel()
	}
}
