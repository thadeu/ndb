package collection

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/query"
	"github.com/thadeu/ndb/internal/wal"
	"github.com/thadeu/ndb/internal/writer"
)

// Document TTL and retention (plan §11.5, §5).
//
// Reads. A query hides what is expired (ttl or expires_field) and what is
// older than the retention window. The cutoff is the time of the parse, so
// no background job has to run first. A document without the field never
// expires. The field is an RFC 3339 string or a number of unix seconds.
//
// Sweeper. Engine mode only. On a tick it finds the expired documents with
// the same where clause and deletes them in batches of sweepBatch through the
// writer, as a del that carries Exp. A del is idempotent, and it names the
// version it saw: an update that moved the expiry forward made a newer
// version, so the sweeper skips that document. Direct mode never deletes.

const (
	defaultSweepEvery = 10 * time.Second
	sweepBatch        = 100
)

var errNothingToExpire = errors.New("nothing to expire")

// clock is the time source of the collection.
func (c *Collection) clock() time.Time {
	if c.now != nil {
		return c.now()
	}

	return time.Now()
}

func (c *Collection) queryOptions() query.Options {
	return query.Options{TimeField: c.def.Time, ExpiresField: c.def.ExpiresField, Now: c.clock}
}

// hidden lists the where clauses of the rows that a read must not see.
func (c *Collection) hidden(now time.Time) []map[string]any {
	var out []map[string]any

	if c.def.TTL != "" && c.def.Time != "" {
		if d, err := catalog.ParseDuration(c.def.TTL); err == nil {
			out = append(out, query.ExpiredWhere(c.def.Time, now.Add(-d)))
		}
	}

	if c.def.ExpiresField != "" {
		out = append(out, query.ExpiredWhere(c.def.ExpiresField, now))
	}

	if c.def.Retention != "" && c.def.Time != "" {
		if d, err := catalog.ParseDuration(c.def.Retention); err == nil {
			out = append(out, query.ExpiredWhere(c.def.Time, now.Add(-d)))
		}
	}

	return out
}

// hide makes q skip the expired and out-of-window rows, at the time now.
func (c *Collection) hide(q *query.Query) error {
	for _, w := range c.hidden(c.clock()) {
		if err := q.Hide(w); err != nil {
			return err
		}
	}

	return nil
}

// visibility returns a query that matches the documents a read can see, or
// nil when the collection hides nothing.
func (c *Collection) visibility(now time.Time) (*query.Query, error) {
	hidden := c.hidden(now)

	if len(hidden) == 0 {
		return nil, nil
	}

	q, err := query.Parse(nil, c.queryOptions())
	if err != nil {
		return nil, err
	}

	for _, w := range hidden {
		if err := q.Hide(w); err != nil {
			return nil, err
		}
	}

	return q, nil
}

// visible tells whether a read can see the document: it is not expired and
// not past the retention.
func (c *Collection) visible(raw []byte) bool {
	vq, err := c.visibility(c.clock())
	if err != nil || vq == nil {
		return true
	}

	m, err := query.Decode(raw)

	return err == nil && vq.Match(m)
}

// expires tells whether the sweeper has work to do in this collection.
func (c *Collection) expires() bool {
	return c.def.ExpiresField != "" || (c.def.TTL != "" && c.def.Time != "")
}

// expireWhere is the where clause of the documents the sweeper deletes.
func (c *Collection) expireWhere(now time.Time) (map[string]any, error) {
	if c.def.ExpiresField != "" {
		return query.ExpiredWhere(c.def.ExpiresField, now), nil
	}

	d, err := catalog.ParseDuration(c.def.TTL)
	if err != nil {
		return nil, err
	}

	return query.ExpiredWhere(c.def.Time, now.Add(-d)), nil
}

// Sweep deletes the expired documents now, and returns how many. Without ttl
// or expires_field it does nothing.
func (c *Collection) Sweep() (int, error) {
	if !c.expires() || c.w == nil {
		return 0, nil
	}

	total := 0

	for {
		found, n, err := c.sweepBatch()
		total += n

		if err != nil {
			return total, err
		}

		if found < sweepBatch || n == 0 {
			return total, nil
		}
	}
}

type expireItem struct {
	id string
	v  uint64
}

type expireReq struct {
	items []expireItem
}

// sweepBatch deletes up to sweepBatch expired documents. It returns how many
// it found and how many it deleted.
func (c *Collection) sweepBatch() (int, int, error) {
	items, err := c.findExpired()
	if err != nil || len(items) == 0 {
		return 0, 0, err
	}

	_, ops, err := c.w.Commit(context.Background(), expireReq{items: items})

	if errors.Is(err, errNothingToExpire) {
		return len(items), 0, nil
	}

	return len(items), len(ops), err
}

func (c *Collection) findExpired() ([]expireItem, error) {
	where, err := c.expireWhere(c.clock())
	if err != nil {
		return nil, err
	}

	body, err := json.Marshal(map[string]any{"where": where, "limit": sweepBatch, "versions": true})
	if err != nil {
		return nil, err
	}

	q, err := query.Parse(body, c.queryOptions())
	if err != nil {
		return nil, err
	}

	page, err := c.Query(context.Background(), q)
	if err != nil {
		return nil, err
	}

	items := make([]expireItem, 0, len(page.Rows))

	for _, raw := range page.Rows {
		var row map[string]any

		if err := json.Unmarshal(raw, &row); err != nil {
			return nil, err
		}

		id, _ := row[idField].(string)
		v, _ := row["_v"].(float64)

		items = append(items, expireItem{id: id, v: uint64(v)})
	}

	return items, nil
}

// prepareExpire runs on the writer goroutine. It skips a document that is
// gone or changed since the sweeper read it.
func (c *Collection) prepareExpire(v *view, r expireReq) ([]wal.Op, error) {
	var ops []wal.Op

	for _, it := range r.items {
		cur, ok := v.get(it.id)
		if !ok || cur.v != it.v {
			continue
		}

		op := wal.Op{Kind: wal.Del, ID: it.id, V: cur.v + 1, Exp: true}

		v.apply(op)
		ops = append(ops, op)
	}

	if len(ops) == 0 {
		return nil, errNothingToExpire
	}

	return ops, nil
}

type sweeper struct {
	every time.Duration
	stop  chan struct{}
	once  sync.Once
	wg    sync.WaitGroup
}

func (c *Collection) startSweeper(every time.Duration) {
	if every <= 0 {
		every = defaultSweepEvery
	}

	c.sweep = &sweeper{every: every, stop: make(chan struct{})}
	c.sweep.wg.Add(1)

	go c.runSweeper()
}

func (c *Collection) runSweeper() {
	defer c.sweep.wg.Done()

	t := time.NewTicker(c.sweep.every)

	defer t.Stop()

	for {
		select {
		case <-c.sweep.stop:
			return
		case <-t.C:
			if _, err := c.Sweep(); err != nil && !errors.Is(err, writer.ErrClosed) {
				log.Printf("ndb: ttl sweep failed, retrying on the next tick: %v", err)
			}
		}
	}
}

func (c *Collection) stopSweeper() {
	if c.sweep == nil {
		return
	}

	c.sweep.once.Do(func() { close(c.sweep.stop) })
	c.sweep.wg.Wait()
}
