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
	"iter"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thadeu/ndb/internal/blob"
	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/fs"
	"github.com/thadeu/ndb/internal/recovery"
	"github.com/thadeu/ndb/internal/wal"
	"github.com/thadeu/ndb/internal/writer"
)

const defaultMaxLine = 1 << 20

const defaultScanWorkers = 2

const defaultGroupLimit = 256 << 20

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

	// CompactMin is how many adjacent small segments make the sealer merge
	// them (default 4). OnCompact is called after each compaction with an
	// iterator over every live document (see compact.go).
	CompactMin int
	OnCompact  func(liveDocs iter.Seq[[]byte])

	// ScanWorkers bounds the units of one scan that run at once: the
	// memtable and one per segment (default 2).
	ScanWorkers int

	// GroupLimit is the memory an aggregate may use for its group tables, in
	// bytes (default 256 MiB). Over it the aggregate fails with
	// group_limit_exceeded.
	GroupLimit int64

	// DocCache is the number of decoded documents that find by id keeps
	// (default 1024). A negative value turns the cache off.
	DocCache int

	// WatchBuffer is how many live events one watcher may hold before it is
	// dropped as lagged (default 4096). WALSegmentSize overrides the WAL
	// segment size (default 64 MiB); tests use it to force rotation.
	WatchBuffer    int
	WALSegmentSize int64

	// Blobs receives the string values over Def.BlobThreshold. Without it
	// nothing is externalized.
	Blobs *blob.Store

	// Observer receives the commit and fsync timings of the WAL writer.
	Observer writer.Observer

	// FlushEvery is the background fsync interval in relaxed mode (default
	// 200 ms). Watch events of a relaxed collection wait for that fsync.
	FlushEvery time.Duration

	// SweepEvery is how often the TTL sweeper runs (default 10 s). Now is the
	// clock of TTL and retention reads (default time.Now); tests set it.
	SweepEvery time.Duration
	Now        func() time.Time
}

type Collection struct {
	def catalog.Def
	w   *writer.Writer

	head *state

	// unsynced holds applied records that are not fsync'd yet (relaxed
	// mode); watchers see them only after the fsync. Writer goroutine only.
	unsynced []wal.Record

	mu      sync.RWMutex
	durable *state

	rd *reader

	// memBytes is the size of the documents in the memtable. tombs holds the
	// deletes not yet written to a segment, by id and WAL tx. Both are
	// guarded by mu, and tombs is only used when seal is set.
	memBytes int64
	tombs    map[string]uint64

	seal    *sealer
	sweep   *sweeper
	onFatal func(error)
	now     func() time.Time

	scanWorkers int
	groupLimit  atomic.Int64
	docs        *docCache

	feed *engineFeed

	blobs *blob.Store
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
		def:         cfg.Def,
		head:        newState(cfg.Def.Unique),
		durable:     newState(cfg.Def.Unique),
		rd:          newReader(),
		onFatal:     cfg.OnFatal,
		now:         cfg.Now,
		scanWorkers: cfg.ScanWorkers,
		docs:        newDocCache(cfg.DocCache),
		blobs:       cfg.Blobs,
	}

	c.feed = &engineFeed{fs: cfg.FS, dir: cfg.Dir, buf: cfg.WatchBuffer}

	if c.feed.buf <= 0 {
		c.feed.buf = defaultWatchBuffer
	}

	if c.scanWorkers <= 0 {
		c.scanWorkers = defaultScanWorkers
	}

	c.SetGroupLimit(cfg.GroupLimit)

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

	c.feed.maxLine = maxLine
	c.feed.last = res.LastTx

	durability := writer.Strict

	if cfg.Def.Durability == catalog.Relaxed {
		durability = writer.Relaxed
	}

	c.w, err = writer.Open(writer.Config{
		Dir:         cfg.Dir,
		FS:          cfg.FS,
		Durability:  durability,
		MaxLine:     maxLine,
		SegmentSize: cfg.WALSegmentSize,
		StartTx:     res.LastTx,
		Prepare:     c.prepare,
		Applied:     c.applied,
		Synced:      c.synced,
		FlushEvery:  cfg.FlushEvery,
		Observer:    cfg.Observer,
		OnFatal:     cfg.OnFatal,
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

	if c.expires() {
		c.startSweeper(cfg.SweepEvery)
	}

	return c, nil
}

func (c *Collection) Close() error {
	c.stopSweeper()
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

	if !c.visible(raw) {
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
	case expireReq:
		ops, err = c.prepareExpire(v, r)
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

		doc, err = c.externalize(doc)
		if err != nil {
			return nil, err
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

	doc, err = c.externalize(doc)
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

	c.unsynced = append(c.unsynced, rec)
}

// synced runs on the writer goroutine after an fsync: the records applied
// so far are durable, so watchers may see them. In relaxed mode a crash
// before the fsync loses them, and no watcher ever saw them.
func (c *Collection) synced() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, rec := range c.unsynced {
		c.feed.publish(rec)
	}

	c.unsynced = c.unsynced[:0]
}

// externalize moves the large string values of doc to the blob store. Each
// blob is durable when Put returns, which is before the WAL line that holds
// the reference is written: a crash leaves an unused blob, never a dangling
// reference.
func (c *Collection) externalize(doc []byte) ([]byte, error) {
	if c.blobs == nil || c.def.BlobThreshold <= 0 {
		return doc, nil
	}

	return blob.Externalize(doc, c.def.BlobThreshold, func(v string) (blob.Ref, error) {
		return c.blobs.Put(strings.NewReader(v), "text/plain; charset=utf-8", 0)
	})
}

// MemBytes is the size of the documents in the memtable.
func (c *Collection) MemBytes() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.memBytes
}

// Subscribers is the number of open watch streams.
func (c *Collection) Subscribers() int {
	return c.feed.hub.Len()
}

// WALMark is a consistent end of the WAL: the segment, the byte offset in it
// and the last tx. Everything up to it is durable. Backup copies exactly
// that prefix while commits go on.
type WALMark struct {
	Seq    int
	Offset int64
	Tx     uint64
}

// WALMark runs in the writer goroutine between two commit batches, so the
// mark is on a record boundary.
func (c *Collection) WALMark() (WALMark, error) {
	var m WALMark

	err := c.w.Run(func(end writer.Pos) {
		m = WALMark{Seq: end.Seq, Offset: end.Offset, Tx: end.Tx}
	})

	return m, err
}
