package collection

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"strconv"
	"sync"
	"time"

	"github.com/thadeu/ndb/internal/agg"
	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/fs"
	"github.com/thadeu/ndb/internal/query"
	"github.com/thadeu/ndb/internal/wal"
)

// ErrFull: the memtable is over its limit. Nothing was applied; the caller
// waits and tries the same line again.
var ErrFull = errors.New("collection: memtable full")

type DirectConfig struct {
	Def catalog.Def

	// MaxBytes is the memtable limit in document bytes (0 = no limit), for
	// a collection with no Data. With Data the seal size limits it.
	MaxBytes int64

	// WatchBuffer is how many live events one watcher may hold before it is
	// dropped as lagged (default 4096).
	WatchBuffer int

	// Data is the dir of the segments and meta.bolt. Empty keeps every row
	// in memory (tests, tools). FS, Shared and the Seal and Compact settings
	// are as in Config.
	Data       string
	FS         fs.FS
	Shared     *Shared
	SealBytes  int64
	SealAge    time.Duration
	SealTick   time.Duration
	CompactMin int
}

// Direct is a read-only direct-mode collection. The tailer feeds it with
// the lines the apps wrote; ndb never writes to it (plan §6.1). It shares
// the read core of Collection (state, reader, indexes), so queries, indexes
// and pagination behave the same in both modes.
//
// With Data it is disk first, like an engine collection (M16), with the
// files of the apps in place of a WAL: the memtable is sealed to segments,
// and each checkpoint saves the read position of every file (sources). After
// a restart the tailer goes on from the positions of the last checkpoint; the
// lines after them are read again, which is safe because the last line of an
// _id wins.
type Direct struct {
	c *Collection

	// guarded by c.mu
	bytes int64
	max   int64

	srcMu   sync.Mutex
	sources map[string]json.RawMessage

	feed *directFeed
}

// NewDirect is a direct collection that keeps every row in memory.
func NewDirect(cfg DirectConfig) *Direct {
	cfg.Data = ""

	d, _ := OpenDirect(cfg)

	return d
}

// OpenDirect opens a direct collection; with cfg.Data, its segments and the
// read positions of its files.
func OpenDirect(cfg DirectConfig) (*Direct, error) {
	c := &Collection{def: cfg.Def, head: newState(cfg.Def.Unique), durable: newState(cfg.Def.Unique), rd: newReader(), shared: cfg.Shared}

	d := &Direct{c: c, max: cfg.MaxBytes, sources: map[string]json.RawMessage{}}

	if cfg.Data != "" {
		c.tombs = map[string]uint64{}

		_, err := c.openSegments(Config{
			Data: cfg.Data, FS: fsOrOS(cfg.FS), Def: cfg.Def, Shared: cfg.Shared,
			SealBytes: cfg.SealBytes, SealAge: cfg.SealAge, SealTick: cfg.SealTick, CompactMin: cfg.CompactMin,
		})
		if err != nil {
			c.closeSegments()

			return nil, err
		}

		if len(c.savedSources) > 0 {
			if err := json.Unmarshal(c.savedSources, &d.sources); err != nil {
				c.closeSegments()

				return nil, err
			}
		}

		c.sources = d.sourcesJSON
	}

	for _, f := range cfg.Def.Indexes {
		_ = c.EnsureIndex(f)
	}

	d.feed = &directFeed{d: d, buf: cfg.WatchBuffer, maxLine: int(cfg.Def.MaxLineSize), ends: map[string]dpos{}}

	if d.feed.buf <= 0 {
		d.feed.buf = defaultWatchBuffer
	}

	if d.feed.maxLine == 0 {
		d.feed.maxLine = defaultMaxLine
	}

	if c.seal != nil {
		c.startSealer()
	}

	return d, nil
}

func fsOrOS(f fs.FS) fs.FS {
	if f == nil {
		return fs.OS{}
	}

	return f
}

func (d *Direct) Close() error {
	return d.c.Close()
}

// Sources are the read positions saved by the last checkpoint, by key (the
// tailer uses the inode). Without Data there are none.
func (d *Direct) Sources() map[string]json.RawMessage {
	d.srcMu.Lock()
	defer d.srcMu.Unlock()

	return maps.Clone(d.sources)
}

// MarkSource records the read position of one file: every line before it is
// applied. The next checkpoint saves it.
func (d *Direct) MarkSource(key string, pos json.RawMessage) {
	d.srcMu.Lock()
	d.sources[key] = pos
	d.srcMu.Unlock()
}

// ForgetSource drops the position of a file that is gone.
func (d *Direct) ForgetSource(key string) {
	d.srcMu.Lock()
	delete(d.sources, key)
	d.srcMu.Unlock()
}

func (d *Direct) sourcesJSON() json.RawMessage {
	d.srcMu.Lock()
	defer d.srcMu.Unlock()

	raw, _ := json.Marshal(d.sources)

	return raw
}

// Seal writes the memtable to a segment now (with Data).
func (d *Direct) Seal() error {
	return d.c.Seal()
}

// Segments is the number of live segments.
func (d *Direct) Segments() int {
	return d.c.Segments()
}

func (d *Direct) SetMaxBytes(n int64) {
	d.c.mu.Lock()
	d.max = n
	d.c.mu.Unlock()
}

func (d *Direct) Len() int {
	return d.c.Len()
}

func (d *Direct) Find(id string) (Doc, bool) {
	return d.c.Find(id)
}

// Get is Find for the _id routes: a line that had no _id is stored under a
// fallback key, and that key never answers.
func (d *Direct) Get(id string) (Doc, bool) {
	return d.c.Get(id)
}

// Reader is the collection that answers the reads.
func (d *Direct) Reader() *Collection {
	return d.c
}

func (d *Direct) ParseQuery(body []byte) (*query.Query, error) {
	return d.c.ParseQuery(body)
}

func (d *Direct) Query(ctx context.Context, q *query.Query) (Page, error) {
	return d.c.Query(ctx, q)
}

func (d *Direct) ParseAggregate(body []byte) (*agg.Pipeline, error) {
	return d.c.ParseAggregate(body)
}

func (d *Direct) Aggregate(ctx context.Context, p *agg.Pipeline) (Page, error) {
	return d.c.Aggregate(ctx, p)
}

func (d *Direct) SetGroupLimit(n int64) {
	d.c.SetGroupLimit(n)
}

func (d *Direct) EnsureIndex(field string) error {
	return d.c.EnsureIndex(field)
}

func (d *Direct) DropIndex(field string) {
	d.c.DropIndex(field)
}

func (d *Direct) Indexes() []IndexInfo {
	return d.c.Indexes()
}

// Apply applies one line that starts at offset off of file. A batch line
// is all or nothing. An invalid line returns an error and changes nothing.
func (d *Direct) Apply(file string, off int64, line []byte) error {
	ops, err := d.parse(file, off, line)
	if err != nil {
		return err
	}

	c := d.c

	c.applyMu.Lock()
	defer c.applyMu.Unlock()

	if hasPut(ops) && d.full() {
		return ErrFull
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for i, op := range ops {
		if old, ok := c.durable.get(op.ID); ok {
			ops[i].V = old.v + 1
		} else {
			ops[i].V = 1
		}

		if old, ok := c.durable.docs[op.ID]; ok {
			d.bytes -= int64(len(old.doc))
		}

		if op.Kind == wal.Put {
			d.bytes += int64(len(op.Doc))
		}

		c.applyDurable(ops[i], 0)
	}

	d.feed.publish(file, off, ops)

	return nil
}

// full tells if the memtable has no room: over MaxBytes without Data, or
// over writeHold times its seal size with Data (it wakes the sealer). The
// tailer waits and tries the line again.
func (d *Direct) full() bool {
	c := d.c

	c.mu.RLock()
	mem, max, bytes := c.memBytes, d.max, d.bytes
	c.mu.RUnlock()

	s := c.seal

	if s == nil {
		return max > 0 && bytes >= max
	}

	if mem < writeHold*min(s.bytes, c.shared.budget()) {
		return false
	}

	select {
	case s.wake <- struct{}{}:
	default:
	}

	return true
}

func hasPut(ops []wal.Op) bool {
	for _, op := range ops {
		if op.Kind == wal.Put {
			return true
		}
	}

	return false
}

func (d *Direct) parse(file string, off int64, line []byte) ([]wal.Op, error) {
	top, err := decodeTop(line)
	if err != nil {
		return nil, err
	}

	rawBatch, isBatch := top["_batch"]

	if !isBatch {
		op, err := d.parseItem(top, line, file+":"+strconv.FormatInt(off, 10))

		return []wal.Op{op}, err
	}

	var items []json.RawMessage

	if err := json.Unmarshal(rawBatch, &items); err != nil {
		return nil, errf("invalid_batch", "_batch must be an array")
	}

	ops := make([]wal.Op, 0, len(items))

	for i, raw := range items {
		item, err := decodeTop(raw)
		if err != nil {
			return nil, errf("invalid_batch", "item %d: %v", i, err)
		}

		if _, nested := item["_batch"]; nested {
			return nil, errf("invalid_batch", "item %d: nested _batch", i)
		}

		fallback := file + ":" + strconv.FormatInt(off, 10) + "#" + strconv.Itoa(i)

		op, err := d.parseItem(item, raw, fallback)
		if err != nil {
			return nil, errf("invalid_batch", "item %d: %v", i, err)
		}

		ops = append(ops, op)
	}

	return ops, nil
}

func decodeTop(raw []byte) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage

	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return nil, errf("invalid_doc", "a line must be a JSON object")
	}

	return m, nil
}

// parseItem reads the _id (the fallback key when the line has none) and
// the tombstone flag. The doc keeps its own bytes: a line without _id gets
// no _id injected, so it is found by queries but not addressable by id.
func (d *Direct) parseItem(top map[string]json.RawMessage, raw []byte, fallback string) (wal.Op, error) {
	deleted := bytes.Equal(top["_deleted"], []byte("true"))
	id := fallback

	if rawID, ok := top[idField]; ok {
		if json.Unmarshal(rawID, &id) != nil || id == "" {
			return wal.Op{}, errf("invalid_id", "field %q must be a non-empty string", idField)
		}
	} else if deleted {
		return wal.Op{}, errf("missing_id", "a tombstone needs field %q", idField)
	}

	if deleted {
		return wal.Op{Kind: wal.Del, ID: id}, nil
	}

	return wal.Op{Kind: wal.Put, ID: id, Doc: bytes.Clone(raw)}, nil
}
