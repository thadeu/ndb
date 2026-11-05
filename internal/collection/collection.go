// Package collection is one engine-mode collection: WAL writer, recovery,
// and the in-memory state (memtable) with CRUD.
//
// Two states:
//   - head: owned by the writer goroutine; includes commits that are
//     prepared but not durable yet. Every check (versions, unique) uses it.
//   - durable: what readers see; changed only after fsync (Applied).
package collection

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/fs"
	"github.com/thadeu/ndb/internal/recovery"
	"github.com/thadeu/ndb/internal/wal"
	"github.com/thadeu/ndb/internal/writer"
)

const defaultMaxLine = 1 << 20

type Config struct {
	Dir string
	FS  fs.FS
	Def catalog.Def

	// OnFatal is passed to the writer (default: exit the process).
	OnFatal func(error)

	// Data is the collection root that holds segments/ and meta.bolt. Empty
	// means no sealing: the memtable keeps every row.
	Data string

	// SealBytes seals the memtable when its documents reach this size
	// (default 64 MiB). SealAge seals a non-empty memtable after this time
	// since the last seal (default 10 min). SealTick is how often the sealer
	// checks both (default 1 s).
	SealBytes int64
	SealAge   time.Duration
	SealTick  time.Duration
}

type Collection struct {
	def catalog.Def
	w   *writer.Writer

	head *state

	mu      sync.RWMutex
	durable *state

	rd *reader

	// memBytes is the size of the documents in the memtable. tombs holds the
	// deletes not yet written to a segment, by id and WAL tx. Both are
	// guarded by mu, and tombs is only used when seal is set.
	memBytes int64
	tombs    map[string]uint64

	seal    *sealer
	onFatal func(error)
}

// Doc is a document and its version.
type Doc struct {
	ID  string
	V   uint64
	Doc []byte
}

type Result struct {
	ID string
	V  uint64
}

// Change is an update: $set and $unset, with dot paths.
type Change struct {
	Set   map[string]any
	Unset []string
}

func Open(cfg Config) (*Collection, error) {
	c := &Collection{
		def:     cfg.Def,
		head:    newState(cfg.Def.Unique),
		durable: newState(cfg.Def.Unique),
		rd:      newReader(),
		onFatal: cfg.OnFatal,
	}

	maxLine := int(cfg.Def.MaxLineSize)

	if maxLine == 0 {
		maxLine = defaultMaxLine
	}

	var from recovery.Position

	if cfg.Data != "" {
		c.tombs = map[string]uint64{}

		pos, err := c.openSegments(cfg)
		if err != nil {
			return nil, err
		}

		from = pos
	}

	res, err := recovery.Replay(recovery.Config{FS: cfg.FS, Dir: cfg.Dir, MaxLine: maxLine, From: from}, func(rec wal.Record) {
		for _, op := range rec.Ops {
			c.head.apply(op)
			c.applyDurable(op, rec.Tx)
		}
	})
	if err != nil {
		c.closeSegments()

		return nil, err
	}

	durability := writer.Strict

	if cfg.Def.Durability == catalog.Relaxed {
		durability = writer.Relaxed
	}

	c.w, err = writer.Open(writer.Config{
		Dir:        cfg.Dir,
		FS:         cfg.FS,
		Durability: durability,
		MaxLine:    maxLine,
		StartTx:    res.LastTx,
		Prepare:    c.prepare,
		Applied:    c.applied,
		OnFatal:    cfg.OnFatal,
	})
	if err != nil {
		c.closeSegments()

		return nil, err
	}

	for _, f := range cfg.Def.Indexes {
		if err := c.EnsureIndex(f); err != nil {
			c.rd.close()
			c.w.Close()
			c.closeSegments()

			return nil, err
		}
	}

	if c.seal != nil {
		c.startSealer()
	}

	return c, nil
}

func (c *Collection) Close() error {
	c.rd.close()
	c.stopSealer()

	if c.w == nil {
		c.closeSegments()

		return nil
	}

	err := c.w.Close()

	c.closeSegments()

	return err
}

// fail reports a read error that makes the state wrong. The default exits
// the process, so the next start rebuilds from the WAL.
func (c *Collection) fail(err error) {
	if c.onFatal != nil {
		c.onFatal(err)

		return
	}

	log.Fatalf("ndb: segment read failed, exiting to recover: %v", err)
}

// Find returns the durable document with this id.
func (c *Collection) Find(id string) (Doc, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	e, ok := c.durable.docs[id]
	if !ok {
		return Doc{}, false
	}

	raw, err := e.raw()
	if err != nil {
		c.fail(err)

		return Doc{}, false
	}

	return Doc{ID: id, V: e.v, Doc: raw}, true
}

// Insert writes all docs in one transaction: all or nothing.
func (c *Collection) Insert(ctx context.Context, docs ...[]byte) ([]Result, error) {
	_, ops, err := c.w.Commit(ctx, insertReq{docs: docs})
	if err != nil {
		return nil, err
	}

	out := make([]Result, len(ops))

	for i, op := range ops {
		out[i] = Result{ID: op.ID, V: op.V}
	}

	return out, nil
}

// Update applies ch to the doc. ifVersion 0 means any version.
func (c *Collection) Update(ctx context.Context, id string, ch Change, ifVersion uint64) (uint64, error) {
	_, ops, err := c.w.Commit(ctx, updateReq{id: id, change: ch, ifVersion: ifVersion})
	if err != nil {
		return 0, err
	}

	return ops[0].V, nil
}

// Delete removes the doc. ifVersion 0 means any version.
func (c *Collection) Delete(ctx context.Context, id string, ifVersion uint64) error {
	_, _, err := c.w.Commit(ctx, deleteReq{id: id, ifVersion: ifVersion})

	return err
}

type insertReq struct {
	docs [][]byte
}

type updateReq struct {
	id        string
	change    Change
	ifVersion uint64
}

type deleteReq struct {
	id        string
	ifVersion uint64
}

// prepare runs in the writer goroutine.
func (c *Collection) prepare(req any) (writer.Prepared, error) {
	v := newView(c.head)

	var (
		ops []wal.Op
		err error
	)

	switch r := req.(type) {
	case insertReq:
		ops, err = c.prepareInsert(v, r)
	case updateReq:
		ops, err = c.prepareUpdate(v, r)
	case deleteReq:
		ops, err = c.prepareDelete(v, r)
	default:
		err = errf("internal", "unknown request %T", req)
	}

	if err != nil {
		return writer.Prepared{}, err
	}

	stage := func() {
		for _, op := range ops {
			c.head.apply(op)
		}
	}

	return writer.Prepared{Ops: ops, Stage: stage}, nil
}

func (c *Collection) prepareInsert(v *view, r insertReq) ([]wal.Op, error) {
	ops := make([]wal.Op, 0, len(r.docs))

	for _, raw := range r.docs {
		id, doc, err := c.withID(raw)
		if err != nil {
			return nil, err
		}

		if _, exists := v.get(id); exists {
			return nil, errf("duplicate_id", "%s already exists", id)
		}

		if err := v.checkUnique(id, doc); err != nil {
			return nil, err
		}

		op := wal.Op{Kind: wal.Put, ID: id, V: 1, Doc: doc}

		v.apply(op)
		ops = append(ops, op)
	}

	return ops, nil
}

func (c *Collection) prepareUpdate(v *view, r updateReq) ([]wal.Op, error) {
	cur, err := current(v, r.id, r.ifVersion)
	if err != nil {
		return nil, err
	}

	raw, err := cur.raw()
	if err != nil {
		return nil, err
	}

	doc, err := applyChange(raw, r.change, c.def.ID)
	if err != nil {
		return nil, err
	}

	if err := v.checkUnique(r.id, doc); err != nil {
		return nil, err
	}

	return []wal.Op{{Kind: wal.Put, ID: r.id, V: cur.v + 1, Doc: doc}}, nil
}

func (c *Collection) prepareDelete(v *view, r deleteReq) ([]wal.Op, error) {
	cur, err := current(v, r.id, r.ifVersion)
	if err != nil {
		return nil, err
	}

	return []wal.Op{{Kind: wal.Del, ID: r.id, V: cur.v + 1}}, nil
}

func current(v *view, id string, ifVersion uint64) (*entry, error) {
	cur, ok := v.get(id)
	if !ok {
		return nil, errf("not_found", "%s", id)
	}

	if ifVersion != 0 && cur.v != ifVersion {
		return nil, errf("version_conflict", "%s is at version %d, not %d", id, cur.v, ifVersion)
	}

	return cur, nil
}

// applied runs in the writer goroutine after fsync.
func (c *Collection) applied(rec wal.Record) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, op := range rec.Ops {
		c.applyDurable(op, rec.Tx)
	}
}
