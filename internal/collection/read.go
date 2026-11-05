package collection

import (
	"container/heap"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/thadeu/ndb/internal/index"
	"github.com/thadeu/ndb/internal/query"
	"github.com/thadeu/ndb/internal/wal"
)

// Reads (plan §10.1, §10.6, §11.1).
//
// Snapshot. Entries are immutable, so a query takes its snapshot in two steps:
//
//  1. Under c.mu.RLock it collects the candidate entries (pointers) from an
//     index or the whole map. Writers apply under c.mu.Lock, so this is one
//     point in the commit order: later commits are not in the list.
//  2. It decodes, matches and sorts those entries with no lock. A later
//     update makes a new entry; the old one stays valid in the list.
//
// Cost: the lock is held for the collect step only (pointer copies), not for
// the match. A snapshot cursor (snapshot: true) also keeps the sorted rows of
// the first request for a TTL: memory is O(matches) per open cursor.
// When segments arrive (M04) the candidate step lists segments too.

const (
	defaultSnapshotTTL = 60 * time.Second
	buildChunk         = 1000
)

// Page is the answer of Query.
type Page struct {
	Rows []json.RawMessage

	// Next is the cursor for the following page; empty on the last page.
	Next string

	// Total is set when the query has count: true.
	Total *int

	// Plan is "id", "index:<field>" or "scan"; Examined is the number of
	// candidate documents that were decoded and matched.
	Plan     string
	Examined int
}

// IndexInfo is the state of one secondary index, for stats.
type IndexInfo struct {
	Field string
	State string // "building" or "ready"
	Done  int
	Total int
}

type secondary struct {
	field  string
	isTime bool
	mem    *index.Mem
	ready  bool
	done   int
	total  int
}

type snapshot struct {
	rows    []row
	total   int
	expires time.Time
}

// reader is the read-side state of the collection. seq, idx and the
// secondary fields are guarded by Collection.mu.
type reader struct {
	seq uint64
	idx map[string]*secondary

	mu     sync.Mutex
	closed bool
	stop   chan struct{}
	wg     sync.WaitGroup
	ttl    time.Duration
	snaps  map[string]*snapshot

	// gate runs before each build chunk; tests use it to pause a build.
	gate func()
}

func newReader() *reader {
	return &reader{
		idx:   map[string]*secondary{},
		stop:  make(chan struct{}),
		ttl:   defaultSnapshotTTL,
		snaps: map[string]*snapshot{},
	}
}

func (r *reader) close() {
	r.mu.Lock()

	if !r.closed {
		r.closed = true
		close(r.stop)
	}

	r.mu.Unlock()
	r.wg.Wait()
}

// SetSnapshotTTL sets how long an idle snapshot cursor lives (default 60 s).
func (c *Collection) SetSnapshotTTL(d time.Duration) {
	c.rd.mu.Lock()
	c.rd.ttl = d
	c.rd.mu.Unlock()
}

// ParseQuery parses a find body with the collection time field.
func (c *Collection) ParseQuery(body []byte) (*query.Query, error) {
	return query.Parse(body, query.Options{TimeField: c.def.Time})
}

// applyDurable applies op to the durable state, and keeps the insertion
// position and the secondary indexes in step. Callers hold c.mu, or run
// before the collection is shared.
func (c *Collection) applyDurable(op wal.Op) {
	old := c.durable.docs[op.ID]

	c.durable.apply(op)

	cur := c.durable.docs[op.ID]

	if cur != nil && old != nil {
		cur.seq = old.seq
	} else if cur != nil {
		c.rd.seq++
		cur.seq = c.rd.seq
	}

	if len(c.rd.idx) == 0 {
		return
	}

	oldDoc := decodeEntry(old)
	curDoc := decodeEntry(cur)

	for _, s := range c.rd.idx {
		if oldDoc != nil {
			for _, k := range index.Keys(oldDoc, s.field, s.isTime) {
				s.mem.Remove(k, op.ID)
			}
		}

		if curDoc != nil {
			for _, k := range index.Keys(curDoc, s.field, s.isTime) {
				s.mem.Add(k, op.ID)
			}
		}
	}
}

func decodeEntry(e *entry) map[string]any {
	if e == nil {
		return nil
	}

	m, _ := query.Decode(e.doc)

	return m
}

// EnsureIndex adds a secondary index on a field path and builds it in the
// background. Until it is ready, queries scan; writes already keep it
// up to date. The id field needs no index.
func (c *Collection) EnsureIndex(field string) error {
	if field == "" {
		return errf("invalid_index", "empty field")
	}

	if field == c.def.ID {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.rd.idx[field]; ok {
		return nil
	}

	c.rd.mu.Lock()
	defer c.rd.mu.Unlock()

	if c.rd.closed {
		return errf("closed", "collection is closed")
	}

	s := &secondary{field: field, isTime: field == c.def.Time, mem: index.NewMem()}
	items := make([]cand, 0, len(c.durable.docs))

	for id, e := range c.durable.docs {
		items = append(items, cand{id, e})
	}

	s.total = len(items)
	c.rd.idx[field] = s

	c.rd.wg.Add(1)

	go c.build(s, items)

	return nil
}

// build fills s with the documents that existed when it was registered.
// Documents written later are indexed by applyDurable. A document that
// changed since is skipped here: applyDurable indexed its new version.
func (c *Collection) build(s *secondary, items []cand) {
	defer c.rd.wg.Done()

	for start := 0; start < len(items); start += buildChunk {
		if c.rd.gate != nil {
			c.rd.gate()
		}

		select {
		case <-c.rd.stop:
			return
		default:
		}

		chunk := items[start:min(start+buildChunk, len(items))]
		keys := make([][][]byte, len(chunk))

		for i, it := range chunk {
			if m := decodeEntry(it.e); m != nil {
				keys[i] = index.Keys(m, s.field, s.isTime)
			}
		}

		c.mu.Lock()

		for i, it := range chunk {
			if c.durable.docs[it.id] != it.e {
				continue
			}

			for _, k := range keys[i] {
				s.mem.Add(k, it.id)
			}
		}

		s.done += len(chunk)
		c.mu.Unlock()
	}

	c.mu.Lock()
	s.ready = true
	c.mu.Unlock()
}

// Indexes lists the secondary indexes with their build progress.
func (c *Collection) Indexes() []IndexInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()

	out := make([]IndexInfo, 0, len(c.rd.idx))

	for _, s := range c.rd.idx {
		state := "building"

		if s.ready {
			state = "ready"
		}

		out = append(out, IndexInfo{Field: s.field, State: state, Done: s.done, Total: s.total})
	}

	slices.SortFunc(out, func(a, b IndexInfo) int { return strings.Compare(a.Field, b.Field) })

	return out
}

type cand struct {
	id string
	e  *entry
}

type row struct {
	query.Pos
	e *entry
}

// Query runs a find. See the package notes above for the snapshot rules.
func (c *Collection) Query(ctx context.Context, q *query.Query) (Page, error) {
	var (
		cur     query.Pos
		snapID  string
		hasCur  = q.After != ""
		decoded error
	)

	if hasCur {
		cur, snapID, decoded = q.DecodeCursor(q.After)
		if decoded != nil {
			return Page{}, decoded
		}
	}

	if snapID != "" {
		return c.pageFromSnapshot(q, snapID, cur)
	}

	cands, plan := c.candidates(q)

	rows, total, err := c.evaluate(ctx, q, cands, cur, hasCur)
	if err != nil {
		return Page{}, err
	}

	page, more := slice(q, rows)

	page.Plan, page.Examined = plan, len(cands)

	if q.Count {
		page.Total = &total
	}

	if more && q.Snapshot {
		snapID = c.rd.register(&snapshot{rows: rows, total: total})
	}

	if more {
		page.Next = q.EncodeCursor(lastPos(q, rows, len(page.Rows)), snapID)
	}

	return page, nil
}

// lastPos is the position of the last row of the page, whose index in
// rows is q.Skip + n - 1.
func lastPos(q *query.Query, rows []row, n int) query.Pos {
	return rows[min(q.Skip, len(rows))+n-1].Pos
}

// slice applies skip and limit to rows, sorted. more is true when rows
// are left after the page.
func slice(q *query.Query, rows []row) (Page, bool) {
	rows = rows[min(q.Skip, len(rows)):]

	more := q.Limit > 0 && len(rows) > q.Limit

	if more {
		rows = rows[:q.Limit]
	}

	out := make([]json.RawMessage, len(rows))

	for i, r := range rows {
		out[i] = project(q.Fields, r.e.doc)
	}

	return Page{Rows: out}, more
}

func (c *Collection) pageFromSnapshot(q *query.Query, id string, cur query.Pos) (Page, error) {
	s, err := c.rd.lookup(id)
	if err != nil {
		return Page{}, err
	}

	start := sort.Search(len(s.rows), func(i int) bool { return q.Cmp(s.rows[i].Pos, cur) > 0 })
	rows := s.rows[start:]

	page, more := slice(q, rows)

	page.Plan = "snapshot"

	if q.Count {
		page.Total = &s.total
	}

	if more {
		page.Next = q.EncodeCursor(lastPos(q, rows, len(page.Rows)), id)
	}

	return page, nil
}

func (r *reader) register(s *snapshot) string {
	var b [8]byte

	rand.Read(b[:])

	id := hex.EncodeToString(b[:])

	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()

	for k, old := range r.snaps {
		if now.After(old.expires) {
			delete(r.snaps, k)
		}
	}

	s.expires = now.Add(r.ttl)
	r.snaps[id] = s

	return id
}

// lookup returns a live snapshot and renews its TTL: the TTL is idle time.
func (r *reader) lookup(id string) (*snapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	s, ok := r.snaps[id]
	if !ok || time.Now().After(s.expires) {
		delete(r.snaps, id)

		return nil, query.NewError("cursor_expired", "the snapshot of this cursor expired")
	}

	s.expires = time.Now().Add(r.ttl)

	return s, nil
}

// evaluate matches the candidates and returns the rows after cur, sorted
// and (with a limit and no snapshot) cut to what the page needs, plus the
// number of rows that matched where.
func (c *Collection) evaluate(ctx context.Context, q *query.Query, cands []cand, cur query.Pos, hasCur bool) ([]row, int, error) {
	k := 0

	if q.Limit > 0 && !q.Snapshot {
		k = q.Skip + q.Limit + 1
	}

	top := &topK{q: q, k: k}
	total := 0

	for i, cd := range cands {
		if i&1023 == 0 && ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}

		m, err := query.Decode(cd.e.doc)
		if err != nil || !q.Match(m) {
			continue
		}

		total++

		r := row{Pos: query.Pos{Keys: q.SortKeys(m), ID: cd.id, Seq: cd.e.seq}, e: cd.e}

		if hasCur && q.Cmp(r.Pos, cur) <= 0 {
			continue
		}

		top.add(r)
	}

	return top.sorted(), total, nil
}

// candidates collects the entries to match, under the read lock. It uses
// the id map or a ready index when a where condition allows it; otherwise
// it lists every document.
func (c *Collection) candidates(q *query.Query) ([]cand, string) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	conds := q.Indexable()

	if out, ok := c.byID(conds); ok {
		return out, "id"
	}

	for _, pass := range []func(string) bool{isPoint, isRange} {
		for _, cd := range conds {
			s := c.rd.idx[cd.Path]

			if s == nil || !s.ready || !pass(cd.Op) {
				continue
			}

			return c.byIndex(s, cd, conds), "index:" + cd.Path
		}
	}

	out := make([]cand, 0, len(c.durable.docs))

	for id, e := range c.durable.docs {
		out = append(out, cand{id, e})
	}

	return out, "scan"
}

func isPoint(op string) bool {
	return op == "eq" || op == "in"
}

func isRange(op string) bool {
	return !isPoint(op)
}

// byID answers eq and in on the id field with one map lookup per key.
func (c *Collection) byID(conds []query.Cond) ([]cand, bool) {
	for _, cd := range conds {
		if cd.Path != c.def.ID || !isPoint(cd.Op) {
			continue
		}

		if slices.ContainsFunc(cd.Vals, func(v query.Value) bool { return v.Kind != query.String }) {
			continue
		}

		out := make([]cand, 0, len(cd.Vals))

		for _, v := range cd.Vals {
			if e, ok := c.durable.docs[v.Str]; ok && !slices.ContainsFunc(out, func(o cand) bool { return o.id == v.Str }) {
				out = append(out, cand{v.Str, e})
			}
		}

		return out, true
	}

	return nil, false
}

// byIndex does one index lookup per key of the condition. A range
// condition is narrowed with the opposite bound on the same field.
func (c *Collection) byIndex(s *secondary, cd query.Cond, conds []query.Cond) []cand {
	var out []cand

	seen := map[string]struct{}{}

	collect := func(id string) bool {
		if _, dup := seen[id]; dup {
			return true
		}

		seen[id] = struct{}{}

		if e, ok := c.durable.docs[id]; ok {
			out = append(out, cand{id, e})
		}

		return true
	}

	for _, v := range cd.Vals {
		r := index.Eq(v)

		if isRange(cd.Op) {
			r = narrow(index.Cmp(cd.Op, v), cd, conds)
		}

		s.mem.Ascend(r, collect)
	}

	return out
}

// narrow takes the other end of r from a condition of the opposite side
// on the same field and type.
func narrow(r index.Range, cd query.Cond, conds []query.Cond) index.Range {
	lower := cd.Op == "gt" || cd.Op == "gte"

	for _, o := range conds {
		if o.Path != cd.Path || !isRange(o.Op) || o.Vals[0].Kind != cd.Vals[0].Kind || (o.Op == "gt" || o.Op == "gte") == lower {
			continue
		}

		other := index.Cmp(o.Op, o.Vals[0])

		if lower {
			r.Hi, r.HiIncl = other.Hi, other.HiIncl
		} else {
			r.Lo, r.LoIncl = other.Lo, other.LoIncl
		}

		return r
	}

	return r
}

// topK keeps the k first rows in the result order, with a max-heap, so a
// page costs O(n log k). k = 0 keeps every row.
type topK struct {
	q    *query.Query
	k    int
	rows []row
}

func (t *topK) Len() int           { return len(t.rows) }
func (t *topK) Swap(i, j int)      { t.rows[i], t.rows[j] = t.rows[j], t.rows[i] }
func (t *topK) Less(i, j int) bool { return t.q.Cmp(t.rows[i].Pos, t.rows[j].Pos) > 0 }
func (t *topK) Push(x any)         { t.rows = append(t.rows, x.(row)) }

func (t *topK) Pop() any {
	last := t.rows[len(t.rows)-1]
	t.rows = t.rows[:len(t.rows)-1]

	return last
}

func (t *topK) add(r row) {
	switch {
	case t.k == 0:
		t.rows = append(t.rows, r)
	case len(t.rows) < t.k:
		heap.Push(t, r)
	case t.q.Cmp(r.Pos, t.rows[0].Pos) < 0:
		t.rows[0] = r
		heap.Fix(t, 0)
	}
}

func (t *topK) sorted() []row {
	slices.SortFunc(t.rows, func(a, b row) int { return t.q.Cmp(a.Pos, b.Pos) })

	return t.rows
}

// project keeps the listed dot paths of doc (objects only, not arrays).
func project(fields []string, doc json.RawMessage) json.RawMessage {
	if len(fields) == 0 {
		return doc
	}

	src, err := query.Decode(doc)
	if err != nil {
		return doc
	}

	out := map[string]any{}

	for _, f := range fields {
		copyPath(out, src, strings.Split(f, "."))
	}

	b, err := json.Marshal(out)
	if err != nil {
		return doc
	}

	return b
}

func copyPath(dst, src map[string]any, parts []string) {
	v, ok := src[parts[0]]
	if !ok {
		return
	}

	if len(parts) == 1 {
		dst[parts[0]] = v

		return
	}

	sub, ok := v.(map[string]any)
	if !ok {
		return
	}

	child, _ := dst[parts[0]].(map[string]any)

	if child == nil {
		child = map[string]any{}
		dst[parts[0]] = child
	}

	copyPath(child, sub, parts[1:])
}
