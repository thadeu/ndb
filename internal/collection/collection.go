// Package collection is one engine-mode collection: WAL writer, recovery,
// and the in-memory state (memtable) with CRUD.
//
// Two states:
//   - head: owned by the writer goroutine; includes commits that are
//     prepared but not durable yet. Every check (versions, unique) uses it.
//   - durable: what readers see; changed only after fsync (Applied).
package collection

import (
	"container/heap"
	"context"
	"encoding/json"
	"errors"
	"iter"
	"log"
	"maps"
	"sort"
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

	// Cursors gives the stored watch positions of the consumers of this
	// collection. WAL truncation never deletes a file a consumer still
	// needs. Nil means no stored consumers. It is a func, not a store, so
	// this package does not import the cursor store.
	Cursors func() ([]string, error)

	// Blobs receives the string values over Def.BlobThreshold. Without it
	// nothing is externalized.
	Blobs *blob.Store

	// Observer receives the commit and fsync timings of the WAL writer.
	Observer writer.Observer

	// FlushEvery is the background fsync interval in relaxed mode (default
	// 200 ms). Watch events of a relaxed collection wait for that fsync.
	FlushEvery time.Duration

	// Shared is the write buffer budget and the heavy work slot of the
	// daemon (shared.go). Nil gives this collection its own.
	Shared *Shared

	// SweepEvery is how often the TTL sweeper runs (default 10 s). Now is the
	// clock of TTL and retention reads (default time.Now); tests set it.
	SweepEvery time.Duration
	Now        func() time.Time
}

type Collection struct {
	def catalog.Def
	w   *writer.Writer

	head *state

	// staged counts the records staged in head that are not applied to
	// durable yet. Writer goroutine only.
	staged int

	// maxLine is max_line_size: a WAL record is never longer.
	maxLine int

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
	shared  *Shared
	sweep   *job
	onFatal func(error)
	now     func() time.Time

	scanWorkers int
	groupLimit  atomic.Int64
	docs        *docCache

	feed *engineFeed

	blobs *blob.Store

	// walKeep is Def.WALRetention (0 = keep every WAL file). walDir, walFS,
	// maxLine and cursors are what TruncateWAL needs.
	walKeep time.Duration
	cursors func() ([]string, error)

	// applyMu: a direct collection has no writer goroutine. Its tailer
	// applies under applyMu, and the seal and compaction steps that run on
	// the writer goroutine of an engine collection take it instead
	// (exclusive).
	applyMu sync.Mutex

	// sources gives the read positions of a direct collection in its files,
	// saved with each checkpoint; savedSources is what the checkpoint held
	// at open. Engine collections have none.
	sources      func() json.RawMessage
	savedSources json.RawMessage
}

// exclusive runs fn where no write is applied at the same time: on the
// writer goroutine (with the WAL end), or under applyMu for a direct
// collection (no WAL).
func (c *Collection) exclusive(fn func(writer.Pos)) error {
	if c.w != nil {
		return c.w.Run(fn)
	}

	c.applyMu.Lock()
	defer c.applyMu.Unlock()

	fn(writer.Pos{})

	return nil
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

// Change is an internal $set and $unset, with dot paths: the expiry stamp.
type Change struct {
	Set   map[string]any
	Unset []string

	// ExpireIn, when positive, sets the expires_field to now + ExpireIn.
	ExpireIn time.Duration
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
		shared:      cfg.Shared,
	}

	c.feed = &engineFeed{fs: cfg.FS, dir: cfg.Dir, buf: cfg.WatchBuffer}
	c.cursors = cfg.Cursors

	if cfg.Def.WALRetention != "" {
		keep, err := catalog.ParseDuration(cfg.Def.WALRetention)
		if err != nil {
			return nil, err
		}

		c.walKeep = keep
	}

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

	c.maxLine = maxLine

	var from recovery.Position

	if cfg.Data != "" {
		c.tombs = map[string]uint64{}

		pos, err := c.openSegments(cfg)
		if err != nil {
			return nil, err
		}

		from = pos
		c.feed.base = pos.Tx
	}

	res, err := recovery.Replay(recovery.Config{FS: cfg.FS, Dir: cfg.Dir, MaxLine: maxLine, From: from}, func(rec wal.Record) {
		if rec.IsTruncate() {
			c.feed.trunc = rec.Tx

			return
		}

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
	if c.feed != nil {
		defer c.feed.hub.CloseAll()
	}

	c.stopSweeper()
	c.rd.close()
	c.stopSealer()
	c.shared.forget(c)

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

	e, ok := c.durable.get(id)
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

// Get is Find for the _id routes. A document answers only when its own _id
// is the key. A direct-mode line without _id is stored under a fallback
// key, has no _id field, and is therefore never returned here.
func (c *Collection) Get(id string) (Doc, bool) {
	d, ok := c.Find(id)
	if !ok {
		return Doc{}, false
	}

	var own struct {
		ID string `json:"_id"`
	}

	if json.Unmarshal(d.Doc, &own) != nil || own.ID != id {
		return Doc{}, false
	}

	d.Doc = withVersion(d.Doc, d.V)

	return d, true
}

// Insert writes all docs in one transaction: all or nothing.
func (c *Collection) Insert(ctx context.Context, docs ...[]byte) ([]Result, error) {
	return c.insert(ctx, insertReq{docs: docs})
}

func (c *Collection) insert(ctx context.Context, req insertReq) ([]Result, error) {
	if err := c.waitRoom(ctx); err != nil {
		return nil, err
	}

	_, ops, err := c.w.Commit(ctx, req)
	if err != nil {
		return nil, err
	}

	out := make([]Result, len(ops))

	for i, op := range ops {
		out[i] = Result{ID: op.ID, V: op.V}
	}

	return out, nil
}

type insertReq struct {
	docs     [][]byte
	expireIn time.Duration

	// upsert merges a document whose _id exists into the stored one,
	// instead of failing with duplicate_id.
	upsert bool
}

// Upsert writes all docs in one transaction, all or nothing: a document whose
// _id exists is merged into the stored one (its fields replace the fields of
// the same name; the other fields stay), any other document is inserted.
func (c *Collection) Upsert(ctx context.Context, expireIn time.Duration, docs ...[]byte) ([]Result, error) {
	if expireIn > 0 {
		if _, err := c.expiryField(); err != nil {
			return nil, err
		}
	}

	return c.insert(ctx, insertReq{docs: docs, expireIn: expireIn, upsert: true})
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
	case setExpiryReq:
		ops, err = c.prepareSetExpiry(v, r)
	default:
		err = errf("internal", "unknown request %T", req)
	}

	if err != nil {
		return writer.Prepared{}, err
	}

	stage := func() {
		c.staged++

		for _, op := range ops {
			c.head.apply(op)
		}
	}

	return writer.Prepared{Ops: ops, Stage: stage}, nil
}

func (c *Collection) prepareInsert(v *view, r insertReq) ([]wal.Op, error) {
	ops := make([]wal.Op, 0, len(r.docs))

	var at time.Time

	if r.expireIn > 0 {
		at = c.clock().Add(r.expireIn)
	}

	for _, raw := range r.docs {
		id, doc, err := c.withID(raw)
		if err != nil {
			return nil, err
		}

		if !at.IsZero() {
			doc, err = c.stampDoc(doc, at)
			if err != nil {
				return nil, err
			}
		}

		prior, err := c.priorVersion(v, id)

		if r.upsert && isCode(err, "duplicate_id") {
			prior, doc, err = c.mergeInto(v, id, doc)
		}

		if err != nil {
			return nil, err
		}

		doc, err = c.externalize(doc)
		if err != nil {
			return nil, err
		}

		if err := v.checkUnique(id, doc); err != nil {
			return nil, err
		}

		op := wal.Op{Kind: wal.Put, ID: id, V: prior + 1, Doc: doc}

		v.apply(op)
		ops = append(ops, op)
	}

	return ops, nil
}

// mergeInto merges doc into the stored document with this id: the top-level
// fields of doc replace the fields of the same name. It returns the version
// of the stored document.
func (c *Collection) mergeInto(v *view, id string, doc json.RawMessage) (uint64, json.RawMessage, error) {
	cur, err := current(v, id)
	if err != nil {
		return 0, nil, err
	}

	raw, err := cur.raw()
	if err != nil {
		return 0, nil, err
	}

	var stored, fields map[string]json.RawMessage

	if err := json.Unmarshal(raw, &stored); err != nil {
		return 0, nil, err
	}

	if err := json.Unmarshal(doc, &fields); err != nil {
		return 0, nil, errf("invalid_doc", "%v", err)
	}

	maps.Copy(stored, fields)

	merged, err := json.Marshal(stored)

	return cur.v, merged, err
}

// priorVersion is the version that a new document with this id continues
// from. It is 0 when the id is free. A document that is expired (or past the
// retention) but not swept yet counts as absent: the new document replaces it
// and continues its version. A visible document is duplicate_id.
func (c *Collection) priorVersion(v *view, id string) (uint64, error) {
	cur, exists := v.get(id)
	if !exists {
		return 0, nil
	}

	raw, err := cur.raw()
	if err != nil {
		return 0, err
	}

	if c.visible(raw) {
		return 0, errf("duplicate_id", "%s already exists", id)
	}

	return cur.v, nil
}

func current(v *view, id string) (*entry, error) {
	cur, ok := v.get(id)
	if !ok {
		return nil, errf("not_found", "%s", id)
	}

	return cur, nil
}

// applied runs in the writer goroutine after fsync.
func (c *Collection) applied(rec wal.Record) {
	c.staged--

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

// SnapshotDoc is one live document at the tx of a snapshot.
type SnapshotDoc struct {
	ID  string
	V   uint64
	Tx  uint64
	Seq uint64

	e *entry
}

// Raw is the document as stored.
func (d SnapshotDoc) Raw() ([]byte, error) {
	return d.e.raw()
}

// Snapshot is the live documents of a collection at one WAL mark: the
// memtable entries and a copy of the live bits of each segment, which it
// holds open until Close. It reads the documents when Each runs, so its
// memory does not grow with the collection.
type Snapshot struct {
	cands []cand
	segs  []segSnap
	held  []*segRef
}

// WALMarkSnapshot is WALMark plus the live documents at exactly that mark.
// Both are taken in the writer goroutine between two commits, so the
// documents are the state after the mark's tx. A backup uses it when the WAL
// no longer starts at tx 1 and cannot rebuild the state. Close the snapshot.
func (c *Collection) WALMarkSnapshot() (WALMark, *Snapshot, error) {
	var (
		m    WALMark
		snap = &Snapshot{}
	)

	err := c.w.Run(func(end writer.Pos) {
		m = WALMark{Seq: end.Seq, Offset: end.Offset, Tx: end.Tx}

		c.mu.RLock()

		defer c.mu.RUnlock()

		src := c.scanSource()

		snap.cands, snap.segs, snap.held = src.cands, src.segs, c.holdSegments()
	})

	sort.Slice(snap.cands, func(a, b int) bool { return snap.cands[a].e.seq < snap.cands[b].e.seq })

	return m, snap, err
}

// Close releases the segments of the snapshot.
func (s *Snapshot) Close() {
	releaseAll(s.held)
	s.held = nil
}

// Each calls fn with every document in insertion order: the memtable and
// the rows of each segment (in order inside a segment) are merged by their
// position.
func (s *Snapshot) Each(fn func(SnapshotDoc) error) error {
	var (
		h    docHeap
		errs = make([]error, len(s.segs)+1)
		stop []func()
	)

	defer func() {
		for _, f := range stop {
			f()
		}
	}()

	sources := []iter.Seq[SnapshotDoc]{func(yield func(SnapshotDoc) bool) {
		for _, cd := range s.cands {
			if !yield(SnapshotDoc{ID: cd.id, V: cd.e.v, Tx: cd.e.tx, Seq: cd.e.seq, e: cd.e}) {
				return
			}
		}
	}}

	for i := range s.segs {
		sources = append(sources, func(yield func(SnapshotDoc) bool) {
			errs[i+1] = eachLive(s.segs[i:i+1], func(id string, e *entry) error {
				if !yield(SnapshotDoc{ID: id, V: e.v, Seq: e.seq, e: e}) {
					return errStopped
				}

				return nil
			})
		})
	}

	for _, src := range sources {
		next, done := iter.Pull(src)
		stop = append(stop, done)

		if d, ok := next(); ok {
			heap.Push(&h, docSource{d, next})
		}
	}

	for h.Len() > 0 {
		top := h[0]

		if err := fn(top.doc); err != nil {
			return err
		}

		if d, ok := top.next(); ok {
			h[0].doc = d
			heap.Fix(&h, 0)
		} else {
			heap.Pop(&h)
		}
	}

	for _, err := range errs {
		if err != nil && !errors.Is(err, errStopped) {
			return err
		}
	}

	return nil
}

type docSource struct {
	doc  SnapshotDoc
	next func() (SnapshotDoc, bool)
}

type docHeap []docSource

func (h docHeap) Len() int           { return len(h) }
func (h docHeap) Less(i, j int) bool { return h[i].doc.Seq < h[j].doc.Seq }
func (h docHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *docHeap) Push(x any)        { *h = append(*h, x.(docSource)) }

func (h *docHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]

	return last
}
