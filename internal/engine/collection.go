package engine

import (
	"context"
	"errors"

	"github.com/thadeu/ndb/internal/agg"
	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/metrics"
	"github.com/thadeu/ndb/internal/query"
	"github.com/thadeu/ndb/internal/tailer"
)

// ErrDirect: a direct-mode collection is written by its apps, never by ndb.
var ErrDirect = errors.New("direct_collection: writes go to the app files")

// reader is the read side that both modes share.
type reader interface {
	ParseQuery(body []byte) (*query.Query, error)
	Query(ctx context.Context, q *query.Query) (collection.Page, error)
	ParseAggregate(body []byte) (*agg.Pipeline, error)
	Aggregate(ctx context.Context, p *agg.Pipeline) (collection.Page, error)
	SetGroupLimit(n int64)
	EnsureIndex(field string) error
	DropIndex(field string)
	Indexes() []collection.IndexInfo
	Len() int
}

// Collection is one open collection. Only engine mode has writes.
type Collection struct {
	name  string
	write catalog.WriteMode

	eng *collection.Collection
	mem *collection.Direct
	rd  reader

	tail *tailer.Tailer
	stop context.CancelFunc
	done chan struct{}

	// wm holds the WAL writer metrics (engine mode only).
	wm *metrics.Writer
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
}

func (c *Collection) Find(ctx context.Context, body []byte) (collection.Page, error) {
	q, err := c.rd.ParseQuery(body)
	if err != nil {
		return collection.Page{}, err
	}

	return c.rd.Query(ctx, q)
}

// Aggregate runs the pipeline of an aggregate body (plan §10.2).
func (c *Collection) Aggregate(ctx context.Context, body []byte) (collection.Page, error) {
	p, err := c.rd.ParseAggregate(body)
	if err != nil {
		return collection.Page{}, err
	}

	return c.rd.Aggregate(ctx, p)
}

func (c *Collection) Insert(ctx context.Context, docs ...[]byte) ([]collection.Result, error) {
	if c.eng == nil {
		return nil, ErrDirect
	}

	return c.eng.Insert(ctx, docs...)
}

func (c *Collection) Update(ctx context.Context, id string, ch collection.Change, ifVersion uint64) (uint64, error) {
	if c.eng == nil {
		return 0, ErrDirect
	}

	return c.eng.Update(ctx, id, ch, ifVersion)
}

func (c *Collection) Delete(ctx context.Context, id string, ifVersion uint64) error {
	if c.eng == nil {
		return ErrDirect
	}

	return c.eng.Delete(ctx, id, ifVersion)
}

// Seal writes the memtable of an engine-mode collection to a segment now.
// The background sealer does this by size and age; direct mode does nothing.
func (c *Collection) Seal() error {
	if c.eng == nil {
		return nil
	}

	return c.eng.Seal()
}

// TruncateWAL deletes the WAL files that wal_retention, the checkpoint and
// the stored watch cursors allow, and returns how many. The background
// sealer does this every tick; direct mode has no WAL.
func (c *Collection) TruncateWAL() (int, error) {
	if c.eng == nil {
		return 0, nil
	}

	return c.eng.TruncateWAL()
}

func (c *Collection) Stats() Stats {
	s := Stats{Name: c.name, Write: c.write, Rows: c.rd.Len(), Indexes: c.rd.Indexes()}

	if c.tail != nil {
		s.Quarantined = c.tail.Quarantined()
		s.TailerLag = c.tail.Lag()
		s.Subscribers = c.mem.Subscribers()
	}

	if c.eng != nil {
		s.MemBytes = c.eng.MemBytes()
		s.Segments = c.eng.Segments()
		s.Subscribers = c.eng.Subscribers()
		s.Writer = c.wm
	}

	return s
}

// close stops the tailer (if any) and closes the collection.
func (c *Collection) close() error {
	if c.tail == nil {
		return c.eng.Close()
	}

	c.stop()
	<-c.done

	return errors.Join(c.tail.Close(), c.mem.Close())
}
