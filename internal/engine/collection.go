package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thadeu/ndb/internal/agg"
	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/diskusage"
	"github.com/thadeu/ndb/internal/metrics"
	"github.com/thadeu/ndb/internal/query"
	"github.com/thadeu/ndb/internal/tailer"
)

// ErrDirect: a direct-mode collection is written by its apps, never by ndb.
var ErrDirect = errors.New("direct_collection: writes go to the app files")

// ErrNotSupported: the operation does not exist for this write mode.
var ErrNotSupported = errors.New("not_supported: this operation needs an engine-mode collection")

// reader is the read side that both modes share.
type reader interface {
	ParseQuery(body []byte) (*query.Query, error)
	Query(ctx context.Context, q *query.Query) (collection.Page, error)
	ParseAggregate(body []byte) (*agg.Pipeline, error)
	Aggregate(ctx context.Context, p *agg.Pipeline) (collection.Page, error)
	SetGroupLimit(n int64)
	Reader() *collection.Collection
	EnsureIndex(field string) error
	DropIndex(field string)
	Indexes() []collection.IndexInfo
	Len() int
}

// Collection is one open collection. Only engine mode has writes.
type Collection struct {
	name  string
	write catalog.WriteMode

	// dir is the collection dir under the data dir.
	dir string

	eng *collection.Collection
	mem *collection.Direct
	rd  reader

	tail *tailer.Tailer
	stop context.CancelFunc
	done chan struct{}

	// wm holds the WAL writer metrics (engine mode only).
	wm *metrics.Writer

	// live guards eng, rd and wm against a truncate or a drop. Every call
	// holds it for reading; Truncate and Drop hold it for writing, so they
	// wait for the calls in flight, and new calls wait for the swap.
	live    sync.RWMutex
	dropped bool

	// closed is set by DB.Close (shut). A closed collection never wakes.
	closed bool

	// asleep is an engine collection closed for idle_close (idle.go): eng
	// and rd are closed, and rest is what Stats tells meanwhile. Guarded by
	// live; used is when a call last entered (unix ns).
	db     *DB
	asleep bool
	rest   resting
	used   atomic.Int64
}

// enter starts a call on the collection; leave ends it. A collection that
// was dropped while the caller held it is unknown; one that sleeps is
// opened again first.
func (c *Collection) enter() error {
	for {
		c.live.RLock()

		if c.closed {
			c.live.RUnlock()

			return ErrClosed
		}

		if c.dropped {
			c.live.RUnlock()

			return fmt.Errorf("%w: %s", catalog.ErrUnknown, c.name)
		}

		if !c.asleep {
			c.used.Store(time.Now().UnixNano())

			return nil
		}

		c.live.RUnlock()

		if err := c.wake(); err != nil {
			return err
		}
	}
}

func (c *Collection) leave() {
	c.live.RUnlock()
}

// Stats is the state of one collection.
type Stats struct {
	Name        string
	Write       catalog.WriteMode
	Rows        int
	Indexes     []collection.IndexInfo
	Quarantined int64

	// Metrics fields. Writer is nil in direct mode; TailerLag is 0 in
	// engine mode.
	MemBytes    int64
	Segments    int
	Subscribers int
	TailerLag   int64
	Writer      *metrics.Writer

	// Asleep: closed for idle_close; the next call opens it.
	Asleep bool
}

func (c *Collection) Find(ctx context.Context, body []byte) (collection.Page, error) {
	if err := c.enter(); err != nil {
		return collection.Page{}, err
	}

	defer c.leave()

	q, err := c.rd.ParseQuery(body)
	if err != nil {
		return collection.Page{}, err
	}

	return c.rd.Query(ctx, q)
}

// Aggregate runs the pipeline of an aggregate body (plan §10.2).
func (c *Collection) Aggregate(ctx context.Context, body []byte) (collection.Page, error) {
	if page, ok, err := collection.AggregateFind(ctx, body, c.Find); ok {
		return page, err
	}

	if err := c.enter(); err != nil {
		return collection.Page{}, err
	}

	defer c.leave()

	p, err := c.rd.ParseAggregate(body)
	if err != nil {
		return collection.Page{}, err
	}

	return c.rd.Aggregate(ctx, p)
}

func (c *Collection) Insert(ctx context.Context, docs ...[]byte) ([]collection.Result, error) {
	return write(ctx, c, func(eng *collection.Collection) ([]collection.Result, error) {
		return eng.Insert(ctx, docs...)
	})
}

// Update is the atomic update by filter, see collection.Update.
func (c *Collection) Update(ctx context.Context, body []byte, opts collection.UpdateOpts) (collection.UpdateResult, error) {
	return write(ctx, c, func(eng *collection.Collection) (collection.UpdateResult, error) {
		return eng.Update(ctx, body, opts)
	})
}

// UpdateID is the atomic update of one document by _id, see
// collection.UpdateID.
func (c *Collection) UpdateID(ctx context.Context, id string, body []byte, opts collection.UpdateOpts) (collection.UpdateResult, error) {
	return write(ctx, c, func(eng *collection.Collection) (collection.UpdateResult, error) {
		return eng.UpdateID(ctx, id, body, opts)
	})
}

// DeleteID removes one document by _id and returns how many (0 or 1).
func (c *Collection) DeleteID(ctx context.Context, id string) (int, error) {
	return write(ctx, c, func(eng *collection.Collection) (int, error) {
		return eng.DeleteID(ctx, id)
	})
}

// Get returns one document by _id with its "_v", in both write modes.
func (c *Collection) Get(id string) (collection.Doc, bool) {
	if c.enter() != nil {
		return collection.Doc{}, false
	}

	defer c.leave()

	if c.eng == nil {
		return c.mem.Get(id)
	}

	return c.eng.Get(id)
}

// InsertIn is Insert with every document expiring in d from now.
func (c *Collection) InsertIn(ctx context.Context, d time.Duration, docs ...[]byte) ([]collection.Result, error) {
	return write(ctx, c, func(eng *collection.Collection) ([]collection.Result, error) {
		return eng.InsertIn(ctx, d, docs...)
	})
}

// Upsert inserts docs, or merges each one whose _id exists into the stored
// document, in one transaction. d > 0 sets their expiry. See
// collection.Upsert.
func (c *Collection) Upsert(ctx context.Context, d time.Duration, docs ...[]byte) ([]collection.Result, error) {
	return write(ctx, c, func(eng *collection.Collection) ([]collection.Result, error) {
		return eng.Upsert(ctx, d, docs...)
	})
}

// Expire sets the expiry of a document (EXPIRE).
func (c *Collection) Expire(ctx context.Context, id string, e collection.Expiry) (uint64, time.Time, error) {
	type expiry struct {
		v  uint64
		at time.Time
	}

	out, err := write(ctx, c, func(eng *collection.Collection) (expiry, error) {
		v, at, err := eng.Expire(ctx, id, e)

		return expiry{v: v, at: at}, err
	})

	return out.v, out.at, err
}

// Persist removes the expiry of a document (PERSIST).
func (c *Collection) Persist(ctx context.Context, id string) (uint64, error) {
	return write(ctx, c, func(eng *collection.Collection) (uint64, error) {
		return eng.Persist(ctx, id)
	})
}

// TTL is the remaining life of a document. Direct mode is ErrNotSupported.
func (c *Collection) TTL(id string) (collection.TTLInfo, error) {
	if err := c.enter(); err != nil {
		return collection.TTLInfo{}, err
	}

	defer c.leave()

	if c.eng == nil {
		return collection.TTLInfo{}, ErrNotSupported
	}

	return c.eng.TTL(id)
}

// Delete removes the documents that the filter matches, and returns how many.
func (c *Collection) Delete(ctx context.Context, body []byte, many bool) (int, error) {
	return write(ctx, c, func(eng *collection.Collection) (int, error) {
		return eng.Delete(ctx, body, many)
	})
}

// Seal writes the memtable of an engine-mode collection to a segment now.
// The background sealer does this by size and age; direct mode does nothing.
func (c *Collection) Seal() error {
	if err := c.enter(); err != nil {
		return err
	}

	defer c.leave()

	if c.store() == nil {
		return nil
	}

	return c.store().Seal()
}

// store is the collection that holds the segments: the engine collection,
// or the one inside a direct collection.
func (c *Collection) store() *collection.Collection {
	if c.engine() {
		return c.eng
	}

	if c.mem != nil {
		return c.mem.Reader()
	}

	return nil
}

// TruncateWAL deletes the WAL files that wal_retention, the checkpoint and
// the stored watch cursors allow, and returns how many. The background
// sealer does this every tick; direct mode has no WAL.
func (c *Collection) TruncateWAL() (int, error) {
	if err := c.enter(); err != nil {
		return 0, err
	}

	defer c.leave()

	if !c.engine() {
		return 0, nil
	}

	return c.eng.TruncateWAL()
}

// Dir is the collection dir under the data dir: WAL files, segments/,
// meta.bolt and quarantine/.
func (c *Collection) Dir() string {
	return c.dir
}

// CompactSize is the live segments of a collection and their bytes on disk.
type CompactSize struct {
	Segments int   `json:"segments"`
	Bytes    int64 `json:"bytes"`
}

// CompactResult is what Compact changed.
type CompactResult struct {
	Before CompactSize `json:"before"`
	After  CompactSize `json:"after"`
}

// Compact seals the memtable, then merges every segment into one, now. The
// background sealer does this by size and age. Direct mode is ErrNotSupported.
func (c *Collection) Compact() (CompactResult, error) {
	if err := c.enter(); err != nil {
		return CompactResult{}, err
	}

	defer c.leave()

	st := c.store()

	if st == nil {
		return CompactResult{}, ErrNotSupported
	}

	if err := st.Seal(); err != nil {
		return CompactResult{}, err
	}

	res := CompactResult{Before: c.segmentSize()}

	if err := st.Compact(); err != nil {
		return res, err
	}

	res.After = c.segmentSize()

	return res, nil
}

func (c *Collection) segmentSize() CompactSize {
	bytes, _ := diskusage.TreeSize(filepath.Join(c.dir, "segments"))

	return CompactSize{Segments: c.store().Segments(), Bytes: bytes}
}

// Quarantine is the last limit lines that the tailer of a direct collection
// could not apply, oldest first, and how many distinct lines it holds. An
// engine-mode collection has none.
func (c *Collection) Quarantine(limit int) ([]tailer.QuarantineRow, int, error) {
	if err := c.enter(); err != nil {
		return nil, 0, err
	}

	defer c.leave()

	if c.tail == nil {
		return nil, 0, nil
	}

	return c.tail.QuarantineRows(limit)
}

func (c *Collection) Stats() Stats {
	c.live.RLock()
	defer c.live.RUnlock()

	if c.asleep {
		return Stats{Name: c.name, Write: c.write, Rows: c.rest.rows, Indexes: c.rest.indexes, Segments: c.rest.segments, Writer: c.wm, Asleep: true}
	}

	s := Stats{Name: c.name, Write: c.write, Rows: c.rd.Len(), Indexes: c.rd.Indexes()}

	if st := c.store(); st != nil {
		s.MemBytes = st.MemBytes()
		s.Segments = st.Segments()
	}

	if c.tail != nil {
		s.Quarantined = c.tail.Quarantined()
		s.TailerLag = c.tail.Lag()
		s.Subscribers = c.mem.Subscribers()
	}

	if c.engine() {
		s.Subscribers = c.eng.Subscribers()
		s.Writer = c.wm
	}

	return s
}

// shut is close for DB.Close: it waits for the calls in flight, and the
// calls after it give ErrClosed.
func (c *Collection) shut() error {
	c.live.Lock()
	defer c.live.Unlock()

	c.closed = true

	return c.close()
}

// close stops the tailer (if any) and closes the collection.
func (c *Collection) close() error {
	if c.asleep {
		return nil
	}

	if c.tail == nil {
		return c.eng.Close()
	}

	c.stop()
	<-c.done

	return errors.Join(c.tail.Close(), c.mem.Close())
}

// stopWork stops the seals and compactions of the collection (Stop).
func (c *Collection) stopWork() {
	c.live.RLock()
	defer c.live.RUnlock()

	if st := c.store(); st != nil && !c.asleep {
		st.StopWork()
	}
}

// engine tells an engine-mode collection (a WAL of its own) from a direct
// one.
func (c *Collection) engine() bool {
	return c.write != catalog.Direct
}

// Name is the collection name.
func (c *Collection) Name() string {
	return c.name
}
