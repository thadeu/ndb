package collection

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"

	"github.com/thadeu/ndb/internal/query"
	"github.com/thadeu/ndb/internal/segment"
)

// Scans (plan §11.2, §11.4). A scan reads the memtable entries, and the live
// rows of each segment. Each unit (the memtable, one segment) runs on a
// bounded pool, and its top-k rows are merged at the end.
//
// Liveness is a snapshot: at the start, every sealed entry marks its row in
// a bitset per segment. A row that a later write replaced is then still
// visible to this query, as the snapshot rules (read.go) say.

// source is what one query reads, taken under c.mu.RLock. For an id or
// index plan it holds the candidate entries. For a scan it holds the
// memtable entries and the live bits of every segment.
type source struct {
	plan  string
	cands []cand
	segs  []segSnap

	// held are the segments this query keeps open; release drops them.
	held []*segRef
}

func (s source) release() {
	releaseAll(s.held)
}

type segSnap struct {
	r      segment.Reader
	groups []segment.Group
	live   bitset
}

// part is the result of one unit: its top-k rows, the matches, and the counters.
type part struct {
	rows  []row
	total int
	ex    Explain
}

// scanSource takes the memtable entries and a copy of the live bits of each
// segment. It runs under c.mu.RLock.
func (c *Collection) scanSource() source {
	src := source{plan: "scan", cands: make([]cand, 0, len(c.durable.docs))}

	for id, e := range c.durable.docs {
		src.cands = append(src.cands, cand{id, e})
	}

	if c.seal == nil {
		return src
	}

	for _, r := range c.seal.readers {
		src.segs = append(src.segs, segSnap{r: r, groups: r.Groups(), live: r.live.clone()})
	}

	return src
}

func setLive(bits []uint64, row int) {
	bits[row>>6] |= 1 << (uint(row) & 63)
}

func isLive(bits []uint64, row int) bool {
	return bits[row>>6]&(1<<(uint(row)&63)) != 0
}

func liveIn(bits []uint64, g segment.Group) int {
	n := 0

	for row := g.Start; row < g.Start+g.Rows; row++ {
		if isLive(bits, row) {
			n++
		}
	}

	return n
}

// execute runs unit 0 (the candidates) and one unit per segment on the pool.
func (c *Collection) execute(ctx context.Context, q *query.Query, src source, cur query.Pos, hasCur bool) ([]part, error) {
	return runPool(ctx, c.scanWorkers, 1+len(src.segs), func(i int) (part, error) {
		if i == 0 {
			return c.evalCands(ctx, q, src, cur, hasCur)
		}

		return c.scanSegment(ctx, q, src.segs[i-1], cur, hasCur)
	})
}

// runPool calls do for 0..n-1 with at most workers goroutines (at least one:
// zero workers would block the first send forever). The first error wins.
func runPool[T any](ctx context.Context, workers, n int, do func(i int) (T, error)) ([]T, error) {
	workers = max(workers, 1)
	out := make([]T, n)
	errs := make([]error, n)
	jobs := make(chan int)

	var wg sync.WaitGroup

	for range min(workers, n) {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for i := range jobs {
				out[i], errs[i] = do(i)
			}
		}()
	}

	for i := range n {
		select {
		case jobs <- i:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()

			return nil, ctx.Err()
		}
	}

	close(jobs)
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}

	return out, nil
}

// merge joins the parts into the final top-k rows, with the totals.
func merge(q *query.Query, parts []part) ([]row, int, Explain) {
	var (
		top   = newTopK(q)
		total int
		ex    Explain
	)

	for _, p := range parts {
		for _, r := range p.rows {
			top.add(r)
		}

		total += p.total
		ex.add(p.ex)
	}

	return top.sorted(), total, ex
}

// evalCands matches the candidate entries of an id or index plan, or the
// memtable entries of a scan.
func (c *Collection) evalCands(ctx context.Context, q *query.Query, src source, cur query.Pos, hasCur bool) (part, error) {
	var p part

	top := newTopK(q)
	useCache := src.plan == "id"

	for i, cd := range src.cands {
		if i&1023 == 0 && ctx.Err() != nil {
			return part{}, ctx.Err()
		}

		p.ex.RowsRead++

		doc, m, err := c.docOf(cd.e, useCache, &p.ex)
		if err != nil {
			return part{}, err
		}

		if m == nil || !q.Match(m) {
			continue
		}

		p.total++

		pos := query.Pos{Keys: q.SortKeys(m), ID: cd.id, Seq: cd.e.seq}

		if hasCur && q.Cmp(pos, cur) <= 0 {
			continue
		}

		top.add(row{Pos: pos, doc: doc, v: cd.e.v})
	}

	p.rows = top.sorted()

	return p, nil
}

// docOf returns the document of e and its decoded form. m is nil when the
// document is not an object. useCache keeps the decoded forms of recent ids.
func (c *Collection) docOf(e *entry, useCache bool, ex *Explain) (json.RawMessage, map[string]any, error) {
	if useCache {
		if hit, ok := c.docs.get(e); ok {
			ex.CacheHits++

			return hit.raw, hit.m, nil
		}
	}

	doc, err := e.raw()
	if err != nil {
		return nil, nil, err
	}

	ex.RawFetched++

	m, err := query.Decode(doc)
	if err != nil {
		return doc, nil, nil
	}

	if useCache {
		c.docs.put(cached{e: e, raw: doc, m: m})
	}

	return doc, m, nil
}

// segScan is the state of one segment scan: the paths of the groups read,
// the index of each path in them, and the top-k.
type segScan struct {
	q      *query.Query
	r      segment.Reader
	paths  []string
	at     map[string]int
	where  []int
	sorted []int
	cur    query.Pos
	hasCur bool
	top    *topK
	p      part
}

// scanSegment reads one segment. A row group that its live bits, zone maps
// or bloom filters rule out is never read. In a group that is read, the
// columns of the where and sort paths are read, and _raw only for the rows
// that match.
func (c *Collection) scanSegment(ctx context.Context, q *query.Query, s segSnap, cur query.Pos, hasCur bool) (part, error) {
	sc := newSegScan(q, s.r, cur, hasCur)
	conds := q.Indexable()

	for gi, g := range s.groups {
		if err := ctx.Err(); err != nil {
			return part{}, err
		}

		if liveIn(s.live, g) == 0 || pruned(s.r, gi, conds) {
			sc.p.ex.RowGroupsSkipped++

			continue
		}

		sc.p.ex.RowGroupsRead++

		rows, err := s.r.ReadGroup(gi, sc.paths)
		if err != nil {
			return part{}, err
		}

		for i := range g.Rows {
			abs := g.Start + i

			if !isLive(s.live, abs) {
				continue
			}

			if err := sc.row(rows, i, abs); err != nil {
				return part{}, err
			}
		}
	}

	if sc.p.ex.RowGroupsRead > 0 {
		sc.p.ex.SegmentsRead = 1
	} else {
		sc.p.ex.SegmentsSkipped = 1
	}

	sc.p.rows = sc.top.sorted()

	return sc.p, nil
}

// newSegScan lists the paths to read: the where paths, then the sort paths
// that are not already there.
func newSegScan(q *query.Query, r segment.Reader, cur query.Pos, hasCur bool) *segScan {
	sc := &segScan{q: q, r: r, at: map[string]int{}, cur: cur, hasCur: hasCur, top: newTopK(q)}

	for _, p := range q.Paths() {
		sc.where = append(sc.where, sc.index(p))
	}

	for _, s := range q.Sort {
		sc.sorted = append(sc.sorted, sc.index(s.Path))
	}

	sc.paths = make([]string, len(sc.at))

	for p, i := range sc.at {
		sc.paths[i] = p
	}

	return sc
}

func (sc *segScan) index(path string) int {
	if i, ok := sc.at[path]; ok {
		return i
	}

	sc.at[path] = len(sc.at)

	return sc.at[path]
}

// rowState is one row of a read group. The raw document is fetched only when
// a cell cannot answer.
type rowState struct {
	sc   *segScan
	row  int
	rows segment.GroupRows
	i    int
	doc  json.RawMessage
	m    map[string]any
}

// row matches one live row of a read group, and keeps it in the top-k when it
// matches and comes after the cursor. abs is the row in the segment, i its
// index in the group.
func (sc *segScan) row(rows segment.GroupRows, i, abs int) error {
	sc.p.ex.RowsRead++

	st := &rowState{sc: sc, row: abs, rows: rows, i: i}

	matched, err := st.match()
	if err != nil || !matched {
		return err
	}

	sc.p.total++

	keys, err := st.keys()
	if err != nil {
		return err
	}

	pos := query.Pos{Keys: keys, ID: rows.IDs[i], Seq: rows.Seqs[i]}

	if sc.hasCur && sc.q.Cmp(pos, sc.cur) <= 0 {
		return nil
	}

	if !sc.top.wants(pos) {
		return nil
	}

	doc, err := st.document()
	if err != nil {
		return err
	}

	sc.top.add(row{Pos: pos, doc: doc, v: rows.Vs[i]})

	return nil
}

// match runs where on the column values, and on the document for a row
// whose column cannot answer.
func (st *rowState) match() (bool, error) {
	cands := make([][]query.Value, len(st.sc.where))
	known := !st.sc.q.NeedsDoc()

	for slot, at := range st.sc.where {
		if !known {
			break
		}

		cell := st.rows.Cells[at][st.i]

		if !cell.Known {
			known = false

			break
		}

		_, cands[slot] = cellValues(cell, st.sc.q.IsTime(st.sc.paths[at]))
	}

	if known {
		return st.sc.q.MatchCols(cands), nil
	}

	if err := st.decode(); err != nil {
		return false, err
	}

	return st.m != nil && st.sc.q.Match(st.m), nil
}

// keys are the sort keys of the row: from its cells, or from the document
// for a sort path whose cell cannot answer.
func (st *rowState) keys() ([]query.Value, error) {
	q := st.sc.q
	elems := make([][]query.Value, len(q.Sort))

	for j, s := range q.Sort {
		cell := st.rows.Cells[st.sc.sorted[j]][st.i]

		if cell.Known {
			elems[j], _ = cellValues(cell, q.IsTime(s.Path))

			continue
		}

		if err := st.decode(); err != nil {
			return nil, err
		}

		elems[j] = query.Elements(st.m, s.Path, q.IsTime(s.Path))
	}

	return q.SortKeysOf(elems), nil
}

// document is the raw document of the row, which the output needs.
func (st *rowState) document() (json.RawMessage, error) {
	if st.doc != nil {
		return st.doc, nil
	}

	raw, err := st.sc.r.Raw(st.row)
	if err != nil {
		return nil, err
	}

	st.sc.p.ex.RawFetched++
	st.doc = raw

	return raw, nil
}

// decode reads the document and its decoded form once.
func (st *rowState) decode() error {
	if st.m != nil {
		return nil
	}

	doc, err := st.document()
	if err != nil {
		return err
	}

	m, err := query.Decode(doc)
	if err != nil {
		return nil
	}

	st.m = m

	return nil
}

// cellValues returns the elements of a known cell and its candidates: the
// elements, and for an array the whole array too, as the matcher sees it.
func cellValues(c segment.Cell, isTime bool) (elems, cands []query.Value) {
	raws := make([]any, len(c.Vals))
	elems = make([]query.Value, len(c.Vals))

	for i, s := range c.Vals {
		raws[i] = rawOf(s)
		elems[i] = query.FromJSON(raws[i], isTime)
	}

	if !c.List {
		return elems, elems
	}

	return elems, append(slices.Clone(elems), query.FromJSON(raws, false))
}

// rawOf is the decoded JSON value of a scalar, as Decode gives it.
func rawOf(s segment.Scalar) any {
	switch s.Kind {
	case segment.ScalarNumber:
		return s.Num
	case segment.ScalarBool:
		return s.Bool
	}

	return s.Str
}

// pruned tells whether a row group cannot hold a row that matches the
// top-level conditions: by zone map or by bloom filter.
func pruned(r segment.Reader, g int, conds []query.Cond) bool {
	for _, cd := range conds {
		if zoneExcludes(r, g, cd) || bloomExcludes(r, g, cd) {
			return true
		}
	}

	return false
}

// zoneExcludes is true when no value of the zone can satisfy cd. Only number
// and string conditions are checked against the zone.
func zoneExcludes(r segment.Reader, g int, cd query.Cond) bool {
	if len(cd.Vals) == 0 {
		return false
	}

	zone, ok := r.Zone(g, cd.Path)

	if !ok {
		return false
	}

	kind, ok := condKind(cd)

	if !ok {
		return false
	}

	if zone.Empty {
		return true
	}

	if kind != zone.Kind {
		return false
	}

	switch cd.Op {
	case "eq", "in":
		for _, v := range cd.Vals {
			if !outside(zone, v) {
				return false
			}
		}

		return true
	case "gt":
		return cmpScalar(zone.Hi, cd.Vals[0]) <= 0
	case "gte":
		return cmpScalar(zone.Hi, cd.Vals[0]) < 0
	case "lt":
		return cmpScalar(zone.Lo, cd.Vals[0]) >= 0
	case "lte":
		return cmpScalar(zone.Lo, cd.Vals[0]) > 0
	}

	return false
}

// bloomExcludes is true when the bloom filter of the group rules out every
// value of an equality or an in condition.
func bloomExcludes(r segment.Reader, g int, cd query.Cond) bool {
	if cd.Op != "eq" && cd.Op != "in" {
		return false
	}

	if len(cd.Vals) == 0 {
		return false
	}

	for _, v := range cd.Vals {
		s, ok := scalarOfValue(v)

		if !ok {
			return false
		}

		may, known := r.Bloom(g, cd.Path, s)

		if !known || may {
			return false
		}
	}

	return true
}

func outside(z segment.Zone, v query.Value) bool {
	return cmpScalar(z.Lo, v) > 0 || cmpScalar(z.Hi, v) < 0
}

// cmpScalar compares a zone bound with a condition value of the same kind.
func cmpScalar(a segment.Scalar, v query.Value) int {
	if a.Kind == segment.ScalarString {
		return strings.Compare(a.Str, v.Str)
	}

	switch {
	case a.Num < v.Num:
		return -1
	case a.Num > v.Num:
		return 1
	}

	return 0
}

// condKind is the kind of the values of a condition, when they are all
// numbers or all strings. Other values are not compared with zones.
func condKind(cd query.Cond) (segment.ScalarKind, bool) {
	kind := segment.ScalarKind(0)

	for _, v := range cd.Vals {
		s, ok := scalarOfValue(v)

		if !ok || (kind != 0 && s.Kind != kind) {
			return 0, false
		}

		kind = s.Kind
	}

	return kind, kind != 0
}

func scalarOfValue(v query.Value) (segment.Scalar, bool) {
	switch v.Kind {
	case query.Number:
		return segment.Scalar{Kind: segment.ScalarNumber, Num: v.Num}, true
	case query.String:
		return segment.Scalar{Kind: segment.ScalarString, Str: v.Str}, true
	}

	return segment.Scalar{}, false
}
