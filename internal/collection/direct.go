package collection

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/thadeu/ndb/internal/agg"
	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/query"
	"github.com/thadeu/ndb/internal/wal"
)

// ErrFull: the memtable is over its limit. Nothing was applied; the caller
// waits and tries the same line again.
var ErrFull = errors.New("collection: memtable full")

type DirectConfig struct {
	Def catalog.Def

	// MaxBytes is the memtable limit in document bytes (0 = no limit).
	MaxBytes int64

	// WatchBuffer is how many live events one watcher may hold before it is
	// dropped as lagged (default 4096).
	WatchBuffer int
}

// Direct is a read-only direct-mode collection. The tailer feeds it with
// the lines the apps wrote; ndb never writes to it (plan §6.1). It shares
// the read core of Collection (durable state, reader, indexes), so queries,
// indexes and pagination behave the same in both modes.
type Direct struct {
	c *Collection

	// guarded by c.mu
	seq   uint64
	bytes int64
	max   int64

	feed *directFeed
}

func NewDirect(cfg DirectConfig) *Direct {
	c := &Collection{def: cfg.Def, durable: newState(cfg.Def.Unique), rd: newReader()}

	for _, f := range cfg.Def.Indexes {
		_ = c.EnsureIndex(f)
	}

	d := &Direct{c: c, max: cfg.MaxBytes}

	d.feed = &directFeed{d: d, buf: cfg.WatchBuffer, maxLine: int(cfg.Def.MaxLineSize), ends: map[string]dpos{}}

	if d.feed.buf <= 0 {
		d.feed.buf = defaultWatchBuffer
	}

	if d.feed.maxLine == 0 {
		d.feed.maxLine = defaultMaxLine
	}

	return d
}

func (d *Direct) Close() error {
	return d.c.Close()
}

func (d *Direct) SetMaxBytes(n int64) {
	d.c.mu.Lock()
	d.max = n
	d.c.mu.Unlock()
}

func (d *Direct) Len() int {
	d.c.mu.RLock()
	defer d.c.mu.RUnlock()

	return len(d.c.durable.docs)
}

func (d *Direct) Find(id string) (Doc, bool) {
	return d.c.Find(id)
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

	d.c.mu.Lock()
	defer d.c.mu.Unlock()

	if d.max > 0 && d.bytes >= d.max && hasPut(ops) {
		return ErrFull
	}

	for _, op := range ops {
		d.seq++
		op.V = d.seq

		if old, ok := d.c.durable.docs[op.ID]; ok {
			d.bytes -= int64(len(old.doc))
		}

		if op.Kind == wal.Put {
			d.bytes += int64(len(op.Doc))
		}

		d.c.applyDurable(op, 0)
	}

	d.feed.publish(file, off, ops)

	return nil
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

// parseItem reads the id (the fallback when the doc has none) and the
// tombstone flag. The doc keeps its own bytes.
func (d *Direct) parseItem(top map[string]json.RawMessage, raw []byte, fallback string) (wal.Op, error) {
	deleted := bytes.Equal(top["_deleted"], []byte("true"))
	id := fallback

	if rawID, ok := top[d.c.def.ID]; ok && d.c.def.ID != "" {
		if json.Unmarshal(rawID, &id) != nil || id == "" {
			return wal.Op{}, errf("invalid_id", "field %q must be a non-empty string", d.c.def.ID)
		}
	} else if deleted {
		return wal.Op{}, errf("missing_id", "a tombstone needs field %q", d.c.def.ID)
	}

	if deleted {
		return wal.Op{Kind: wal.Del, ID: id}, nil
	}

	return wal.Op{Kind: wal.Put, ID: id, Doc: bytes.Clone(raw)}, nil
}
