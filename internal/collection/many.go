package collection

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/thadeu/ndb/internal/query"
	"github.com/thadeu/ndb/internal/segment"
	"github.com/thadeu/ndb/internal/wal"
)

// A many write (update or delete with many) is split into WAL records.
//
// The caller lists the candidates once, before the first record, from the
// id field or a ready index. That runs outside the writer goroutine (it only
// reads durable state), so a list of a million ids does not stall other
// writes. Each record then takes the next candidates until its ops reach
// recordBudget, so a write of any size fits max_line_size, and the writer
// is free for other commits between two records. Each record is atomic; the
// write as a whole is not: after a crash, the records already written stay.
//
// Each candidate is visited once, so an $inc never runs twice on the same
// document. A document that starts to match after the list is made is not
// in the write, like a document written after the write ends.

type manyCursor struct {
	cands  []cand
	pos    int
	listed bool

	// exact: the index alone answers the filter (query.IndexOnly), so an
	// unchanged candidate is deleted without reading it.
	exact bool
}

func (m *manyCursor) done() bool {
	return m.listed && m.pos >= len(m.cands)
}

// indexWait is how long a many write waits for an index that is building.
var indexWait = 30 * time.Second

// list makes the candidate list of the write. Each candidate is tested
// again in the writer, so a list made before the first record is safe. An
// index of the filter that is still building is waited for, up to
// indexWait: it is declared, so the write is not unindexed_many. A new
// partition, for one, builds its indexes after its first write.
func (c *Collection) list(ctx context.Context, m *manyCursor, q *query.Query) error {
	conds := q.Indexable()
	path, exact := q.IndexOnly()

	if ids, ok := c.idValues(conds); ok {
		m.cands = make([]cand, len(ids))

		for i, id := range ids {
			m.cands[i] = cand{id: id}
		}

		m.listed = true
		m.exact = exact && path == idField

		return nil
	}

	plan, cands, ok := c.lockedCands(conds)

	if !ok && c.building(conds) {
		c.waitBuilt(ctx, conds)

		plan, cands, ok = c.lockedCands(conds)
	}

	if !ok {
		return errf("unindexed_many", "many needs the id or an indexed field in the filter")
	}

	byLocation(cands)

	m.cands = cands
	m.listed = true
	m.exact = exact && plan == "index:"+path

	return nil
}

func (c *Collection) lockedCands(conds []query.Cond) (string, []cand, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.indexedCands(conds)
}

// building tells whether a condition is on an index that is not ready.
func (c *Collection) building(conds []query.Cond) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, cd := range conds {
		if s, ok := c.rd.idx[cd.Path]; ok && !s.ready {
			return true
		}
	}

	return false
}

// waitBuilt waits until no index of conds is building, up to indexWait, the
// end of ctx or the close of the collection.
func (c *Collection) waitBuilt(ctx context.Context, conds []query.Cond) {
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()

	deadline := time.After(indexWait)

	for c.building(conds) {
		select {
		case <-ctx.Done():
			return
		case <-c.rd.stop:
			return
		case <-deadline:
			return
		case <-tick.C:
		}
	}
}

// byLocation sorts the candidates by segment and row, memtable rows first.
// A segment reader keeps one decoded row group, so a write that reads its
// documents in index order would decode a group again for almost each row;
// in row order it decodes each group once. The order of a many write does
// not change its result.
func byLocation(cands []cand) {
	segs := map[segment.Reader]int{}

	rank := func(cd cand) (int, int) {
		if cd.e == nil || cd.e.loc == nil {
			return 0, 0
		}

		n, ok := segs[cd.e.loc.seg]
		if !ok {
			n = len(segs) + 1
			segs[cd.e.loc.seg] = n
		}

		return n, cd.e.loc.row
	}

	slices.SortStableFunc(cands, func(a, b cand) int {
		sa, ra := rank(a)
		sb, rb := rank(b)

		return cmp.Or(cmp.Compare(sa, sb), cmp.Compare(ra, rb))
	})
}

// skipRead tells if a candidate of an exact list still matches without
// reading it. That holds while its durable entry is the one the index gave,
// and no commit is staged in head that is not durable yet: then the index
// key of the document is its current value.
func (c *Collection) skipRead(m *manyCursor, cd cand, vq *query.Query) bool {
	if !m.exact || vq != nil || c.staged > 0 {
		return false
	}

	if cd.e == nil {
		return true
	}

	c.mu.RLock()
	cur, ok := c.durable.get(cd.id)
	c.mu.RUnlock()

	return ok && sameVersion(cur, cd.e)
}

// recordBudget is the size of ops one record of a many write takes, half
// of max_line_size: the record also holds the tx, the time and the JSON
// around each op.
func (c *Collection) recordBudget() int {
	return c.maxLine / 2
}

// opCost is about the encoded size of op. The id counts twice, for the
// escapes it may need.
func opCost(op wal.Op) int {
	return 64 + 2*len(op.ID) + len(op.Doc)
}

// full tells if a record with ops of this size is done. One op always fits:
// a single op over the line limit is the writer's line_too_large.
func (c *Collection) full(ops []wal.Op, size int) bool {
	return len(ops) > 0 && size >= c.recordBudget()
}
