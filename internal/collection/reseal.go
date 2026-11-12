package collection

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"regexp"
	"slices"
	"sync/atomic"

	"github.com/thadeu/ndb/internal/fs"
	"github.com/thadeu/ndb/internal/query"
	"github.com/thadeu/ndb/internal/wal"
)

// A field added to encrypted or blind leaves the stored documents plain. The
// reseal job writes each of them again in the background, sealed and with
// its hash, as an index build fills a new index. Until the job is ready, a
// filter on the field also matches the plain value (filterField).
//
// The file resealFile lists the encrypted fields whose old documents are
// sealed. A field of the definition that is not in it starts the job: a
// field added by a PUT, a field added to ndb.yml while the daemon was
// stopped, a job that a stop cut, and a data dir of 1.22 or older.

const (
	resealFile  = "encrypted.json"
	resealBatch = 500
)

// resealState is the progress of the job. fields does not change; the
// query hook reads ready with no lock.
type resealState struct {
	fields []string
	key    string
	done   atomic.Int64
	total  int
	ready  atomic.Bool
}

type resealReq struct {
	items []expireItem
}

var errNothingToReseal = errors.New("nothing to reseal")

// resealing tells whether the encrypted field may still be plain in old
// documents: it is not in the file yet. Before the open reads the file,
// every field may be.
func (c *Collection) resealing(field string) bool {
	if c.dir == "" {
		return false
	}

	done := c.resealed.Load()

	return done == nil || !slices.Contains(*done, field)
}

// StartReseal starts the job for the encrypted fields not sealed with the
// current key: after a key rotation, the engine calls it.
func (c *Collection) StartReseal() {
	c.startReseal()
}

// Resealed tells whether every encrypted field is sealed with the current
// key in every document.
func (c *Collection) Resealed() bool {
	done := c.savedReseal()

	for _, f := range c.Encrypted() {
		if !slices.Contains(done, f) {
			return false
		}
	}

	return true
}

// Resealing is the state of the job, for stats: one entry per field.
func (c *Collection) Resealing() []IndexInfo {
	st := c.reseal.Load()
	if st == nil {
		return nil
	}

	state := "building"

	if st.ready.Load() {
		state = "ready"
	}

	out := make([]IndexInfo, 0, len(st.fields))

	for _, f := range st.fields {
		out = append(out, IndexInfo{Field: f, State: state, Done: int(st.done.Load()), Total: st.total})
	}

	return out
}

// startReseal starts the job for the encrypted fields that are not in the
// file yet. While a job runs, a new field waits: the job starts the next one
// when it ends.
func (c *Collection) startReseal() {
	if c.w == nil || c.dir == "" {
		return
	}

	c.resealMu.Lock()
	defer c.resealMu.Unlock()

	done := c.savedReseal()

	c.resealed.Store(&done)

	if run := c.reseal.Load(); run != nil && !run.ready.Load() {
		return
	}

	var fields []string

	for _, f := range c.Encrypted() {
		if !slices.Contains(done, f) {
			fields = append(fields, f)
		}
	}

	if len(fields) == 0 {
		return
	}

	st := &resealState{fields: fields, key: c.keyID(), total: c.Len()}

	c.rd.mu.Lock()
	defer c.rd.mu.Unlock()

	if c.rd.closed {
		return
	}

	c.reseal.Store(st)

	c.rd.wg.Add(1)

	go c.runReseal(st)
}

func (c *Collection) runReseal(st *resealState) {
	defer c.rd.wg.Done()

	err := c.resealAll(st)

	if err != nil {
		if !c.stopping() {
			slog.Error("reseal: stopped, it runs again at the next start", "collection", c.def.Name, "err", err)
		}

		return
	}

	if c.stopping() {
		return
	}

	c.resealMu.Lock()

	// A rotation during the job: values written before it have the old
	// key, so the job runs again for the new one.
	if st.key != c.keyID() {
		st.ready.Store(true)
		c.resealMu.Unlock()
		c.startReseal()

		return
	}

	done := normFields(append(c.savedReseal(), st.fields...))
	err = c.saveReseal(st.key, done)

	if err == nil {
		c.resealed.Store(&done)
		st.ready.Store(true)
	}

	c.resealMu.Unlock()

	if err != nil {
		slog.Error("reseal: save the fields, the job runs again at the next start", "collection", c.def.Name, "err", err)

		return
	}

	slog.Info("reseal: ready", "collection", c.def.Name, "fields", st.fields, "documents", st.done.Load())

	c.startReseal()
}

// resealAll reads every document that has a plain value in one of the
// fields, page by page, and writes it again.
func (c *Collection) resealAll(st *resealState) error {
	body, err := json.Marshal(map[string]any{"query": staleWhere(st.fields, c.box.Prefix()), "limit": resealBatch, "versions": true, "snapshot": true})
	if err != nil {
		return err
	}

	q, err := query.Parse(body, query.Options{TimeField: c.def.Time})
	if err != nil {
		return err
	}

	for {
		if c.resealGate != nil {
			c.resealGate()
		}

		if c.stopping() {
			return nil
		}

		page, err := c.Query(context.Background(), q)
		if err != nil {
			return err
		}

		items, err := rowItems(page.Rows)
		if err != nil {
			return err
		}

		if len(items) > 0 {
			_, _, err = c.w.Commit(context.Background(), resealReq{items: items})

			if err != nil && !errors.Is(err, errNothingToReseal) {
				return err
			}
		}

		st.done.Add(int64(len(items)))

		if page.Next == "" {
			return nil
		}

		q.After = page.Next
	}
}

// prepareReseal runs on the writer goroutine. A document that changed since
// the job read it was written by then, so it is sealed already.
func (c *Collection) prepareReseal(v *view, r resealReq) ([]wal.Op, error) {
	var ops []wal.Op

	for _, it := range r.items {
		cur, ok := v.get(it.id)
		if !ok || cur.v != it.v {
			continue
		}

		raw, err := cur.raw()
		if err != nil {
			return nil, err
		}

		doc, err := c.encrypt(raw)
		if err != nil {
			return nil, err
		}

		if bytes.Equal(doc, raw) {
			continue
		}

		op := wal.Op{Kind: wal.Put, ID: it.id, V: cur.v + 1, Doc: doc}

		v.apply(op)
		ops = append(ops, op)
	}

	if len(ops) == 0 {
		return nil, errNothingToReseal
	}

	return ops, nil
}

// staleWhere matches a document with a value in one of fields that is not
// sealed with the current key (prefix): a plain value, or one of the old
// key of a rotation. It is parsed with no Field hook: it reads the stored
// values.
func staleWhere(fields []string, prefix string) map[string]any {
	or := make([]any, 0, len(fields))

	for _, f := range fields {
		or = append(or, map[string]any{f: map[string]any{
			"$exists": true,
			"$ne":     nil,
			"$not":    map[string]any{"$regex": "^" + regexp.QuoteMeta(prefix)},
		}})
	}

	return map[string]any{"$or": or}
}

func rowItems(rows []json.RawMessage) ([]expireItem, error) {
	items := make([]expireItem, 0, len(rows))

	for _, raw := range rows {
		var row struct {
			ID string `json:"_id"`
			V  uint64 `json:"_v"`
		}

		if err := json.Unmarshal(raw, &row); err != nil {
			return nil, err
		}

		items = append(items, expireItem{id: row.ID, v: row.V})
	}

	return items, nil
}

func (c *Collection) stopping() bool {
	select {
	case <-c.rd.stop:
		return true
	default:
		return false
	}
}

func (c *Collection) resealPath() string {
	return filepath.Join(c.dir, resealFile)
}

// savedReseal is the fields whose old documents are sealed.
func (c *Collection) savedReseal() []string {
	f, err := c.fs().Open(c.resealPath())
	if err != nil {
		return nil
	}

	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil
	}

	data := make([]byte, info.Size())

	if _, err := f.ReadAt(data, 0); err != nil && !errors.Is(err, io.EOF) {
		return nil
	}

	var saved resealFileBody

	// 1.23.0 wrote a list of fields, all of the key k1.
	if json.Unmarshal(data, &saved.Fields) == nil {
		saved.Key = "k1"
	} else if json.Unmarshal(data, &saved) != nil {
		return nil
	}

	if saved.Key != c.keyID() {
		return nil // a key rotation: every field is sealed again
	}

	return saved.Fields
}

// resealFileBody is the file: the key of the values and the fields done.
type resealFileBody struct {
	Key    string   `json:"key"`
	Fields []string `json:"fields"`
}

func (c *Collection) keyID() string {
	if c.box == nil {
		return ""
	}

	return c.box.ID()
}

func (c *Collection) saveReseal(key string, fields []string) error {
	data, err := json.Marshal(resealFileBody{Key: key, Fields: fields})
	if err != nil {
		return err
	}

	tmp := c.resealPath() + ".tmp"

	f, err := c.fs().Create(tmp)
	if err != nil {
		return err
	}

	if _, err := f.Write(data); err != nil {
		f.Close()

		return err
	}

	if err := f.Sync(); err != nil {
		f.Close()

		return err
	}

	if err := f.Close(); err != nil {
		return err
	}

	return c.fs().Rename(tmp, c.resealPath())
}

func normFields(fields []string) []string {
	out := slices.Clone(fields)

	slices.Sort(out)

	return slices.Compact(out)
}

func (c *Collection) fs() fs.FS {
	if c.fsys == nil {
		return fs.OS{}
	}

	return c.fsys
}
