package collection

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"slices"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/query"
	"github.com/thadeu/ndb/internal/segment"
	"github.com/thadeu/ndb/internal/writer"
)

// Compaction (plan §7.3, §8.3). It merges adjacent sealed segments into one
// and drops what no read can see: superseded versions, tombstones that no
// older segment needs, and rows that are expired or past the retention.
//
// A compaction of the run names[lo:hi] has five steps, serialized with Seal:
//  1. plan, on the writer goroutine: the live rows of the run (live bits).
//  2. build the merged segment and its key files, with no lock held; writes
//     go on.
//  3. publish the files (tmp + fsync + rename + fsync dir).
//  4. commit: one bolt transaction replaces the run by the new segment in the
//     checkpoint, with its live bits. The WAL position does not change.
//  5. swap, on the writer goroutine: the new segment takes the place of the
//     run, and its live bits drop the rows that changed since step 1. The
//     run is retired: a retired segment closes and its files go when the
//     last query that holds it ends (segref.go).
//
// A crash before step 4 leaves orphan files, removed at the next open. A
// crash after it starts from the new checkpoint, and the WAL tail clears the
// bits of the rows that changed since step 1 again.
//
// A lookup takes the newest row of an id, so a row that is dropped must not
// let an older segment bring its id back. A tombstone is written for such an
// id when an older segment (before lo) holds a row of it.
//
// A run holds at most maxCompactRows live rows: the plan and the key files of
// the merged segment are in memory.

// defaultCompactMin is the number of adjacent small segments that start a
// compaction on the sealer tick. A segment is small under smallSegmentRows.
const (
	defaultCompactMin = 4
	smallSegmentRows  = 100_000
	maxCompactRows    = 512 << 10
)

// Compact merges the segments, in runs of at most maxCompactRows live rows.
// Without segments it does nothing.
func (c *Collection) Compact() error {
	if c.seal == nil {
		return nil
	}

	for lo := 0; ; lo++ {
		hi, ok := c.runFrom(lo)

		if !ok {
			return nil
		}

		if err := c.compactRange(lo, hi); err != nil {
			return err
		}
	}
}

// runFrom is the end of the run that starts at segment lo: as many segments
// as fit in maxCompactRows live rows, at least one.
func (c *Collection) runFrom(lo int) (int, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	rs := c.seal.readers

	if lo >= len(rs) {
		return 0, false
	}

	hi, rows := lo+1, rs[lo].nlive

	for hi < len(rs) && rows+rs[hi].nlive <= maxCompactRows {
		rows += rs[hi].nlive
		hi++
	}

	return hi, true
}

type liveRow struct {
	id  string
	v   uint64
	seq uint64
	tx  uint64
	r   *segRef
	row int

	// out is the row in the merged segment, -1 when the row is dropped.
	out int
}

type compactPlan struct {
	run    []*segRef
	live   []liveRow
	gone   map[string]uint64
	fields []indexField
	now    time.Time
}

// compactRange merges the segments lo..hi-1.
func (c *Collection) compactRange(lo, hi int) error {
	s := c.seal

	if s == nil {
		return nil
	}

	s.compacting.Lock()

	defer s.compacting.Unlock()

	if !c.shared.acquire(s.stop) {
		return errStopped
	}

	defer c.shared.release()

	if lo < 0 || hi > len(s.readers) || lo >= hi {
		return fmt.Errorf("compact: bad range %d..%d of %d segments", lo, hi, len(s.readers))
	}

	plan, err := c.planCompact(lo, hi)
	if err != nil {
		return err
	}

	tombs, err := c.planDrops(&plan, lo)
	if err != nil {
		return err
	}

	name, merged, info, err := c.publishMerged(plan, tombs)
	if err != nil {
		return err
	}

	s.step("published")

	if err := c.commitAndSwap(plan, lo, hi, name, info, merged); err != nil {
		return err
	}

	s.step("swapped")

	for _, r := range plan.run {
		r.retire()
	}

	s.step("retired")

	if s.onCompact != nil {
		s.onCompact(c.liveDocs())
	}

	return nil
}

// commitAndSwap commits the merged segment to meta.bolt and swaps it in, with
// no seal commit in between (s.commitMu).
func (c *Collection) commitAndSwap(plan compactPlan, lo, hi int, name string, info segment.Info, merged *segRef) error {
	s := c.seal

	s.commitMu.Lock()
	defer s.commitMu.Unlock()

	// A seal may have committed during the build: its checkpoint is past the
	// writes made since the plan, so the bits saved here must already drop
	// the rows those writes replaced (the WAL tail will not replay them).
	c.mu.RLock()
	transferCleared(plan, merged)
	c.mu.RUnlock()

	if err := c.commitSwap(lo, hi, name, info, merged); err != nil {
		c.discard(merged, name)

		return err
	}

	s.step("committed")

	return c.swap(plan, lo, hi, name, merged)
}

// errStopped ends a seal or a compaction because the collection is closing
// (a drop, a truncate, a shutdown): its result would be thrown away.
// ErrStopped is a seal or a compaction stopped by a shutdown (StopWork).
var ErrStopped = errors.New("stopping: the daemon is shutting down; retry")

var errStopped = ErrStopped

// stopping tells if the sealer was asked to stop.
func (s *sealer) stopping() bool {
	select {
	case <-s.stop:
		return true
	default:
		return false
	}
}

func (s *sealer) step(name string) {
	if s.hook != nil {
		s.hook(name)
	}
}

// planCompact lists the live rows of the run from the live bits, and the
// tombstones it holds. It runs on the writer goroutine, so the rows are one
// point in the commit order.
func (c *Collection) planCompact(lo, hi int) (compactPlan, error) {
	plan := compactPlan{run: slices.Clone(c.seal.readers[lo:hi]), gone: map[string]uint64{}, now: c.clock()}

	var failed error

	err := c.exclusive(func(writer.Pos) {
		c.mu.RLock()

		defer c.mu.RUnlock()

		plan.fields = c.indexFields()

		for _, r := range plan.run {
			failed = r.ids.Each(func(e segment.KeyEntry) bool {
				switch {
				case e.Del:
					plan.gone[string(e.Key)] = max(plan.gone[string(e.Key)], e.Tx)
				case r.live.has(e.Row):
					plan.live = append(plan.live, liveRow{id: string(e.Key), v: e.V, seq: e.Seq, tx: e.Tx, r: r, row: e.Row})
				}

				return true
			})

			if failed != nil {
				return
			}
		}
	})

	sort.Slice(plan.live, func(a, b int) bool { return plan.live[a].seq < plan.live[b].seq })

	return plan, cmp.Or(err, failed)
}

// planDrops marks the live rows that no read can see any more (expired or
// past the retention), and returns the tombstones the merged segment needs.
// It reads the time columns, not the documents. The kept rows get their row
// in the merged segment, after the tombstones.
func (c *Collection) planDrops(plan *compactPlan, lo int) ([]segment.Row, error) {
	hidden, err := c.hiddenRows(plan)
	if err != nil {
		return nil, err
	}

	for i := range plan.live {
		lr := &plan.live[i]

		if hidden[lr.r] != nil && hidden[lr.r].has(lr.row) {
			lr.out = -1
			plan.gone[lr.id] = max(plan.gone[lr.id], lr.tx)
		}
	}

	tombs, err := c.tombstonesFor(plan.gone, lo)
	if err != nil {
		return nil, err
	}

	out := len(tombs)

	for i := range plan.live {
		if plan.live[i].out == 0 {
			plan.live[i].out = out
			out++
		}
	}

	return tombs, nil
}

// hideBound is one rule of hidden(): a row whose path is at or before cutoff
// is hidden (TTL, expires_field, retention).
type hideBound struct {
	path   string
	cutoff time.Time
}

func (c *Collection) hideBounds(now time.Time) []hideBound {
	var out []hideBound

	add := func(path, dur string) {
		if path == "" || dur == "" {
			return
		}

		if d, err := catalog.ParseDuration(dur); err == nil {
			out = append(out, hideBound{path: path, cutoff: now.Add(-d)})
		}
	}

	add(c.def.Time, c.def.TTL)
	add(c.def.Time, c.def.Retention)

	if c.def.ExpiresField != "" {
		out = append(out, hideBound{path: c.def.ExpiresField, cutoff: now})
	}

	return out
}

// hiddenRows marks, per segment of the run, the live rows that a read can no
// longer see. It reads the time columns of the groups with live rows; a row
// whose column cannot answer is decoded.
func (c *Collection) hiddenRows(plan *compactPlan) (map[*segRef]bitset, error) {
	bounds := c.hideBounds(plan.now)

	if len(bounds) == 0 {
		return nil, nil
	}

	vis, err := c.visibility(plan.now)
	if err != nil {
		return nil, err
	}

	paths := make([]string, len(bounds))

	for i, b := range bounds {
		paths[i] = b.path
	}

	out := map[*segRef]bitset{}

	for _, r := range plan.run {
		hidden := newBitset(r.Len())

		for gi, g := range r.Groups() {
			if c.seal.stopping() {
				return nil, errStopped
			}

			if liveIn(r.live, g) == 0 {
				continue
			}

			rows, err := r.ReadGroup(gi, paths)
			if err != nil {
				return nil, err
			}

			for i := range g.Rows {
				row := g.Start + i

				if !r.live.has(row) {
					continue
				}

				hide, err := rowHidden(r, row, rows, i, bounds, vis)
				if err != nil {
					return nil, err
				}

				if hide {
					hidden.set(row)
				}
			}
		}

		out[r] = hidden
	}

	return out, nil
}

// rowHidden checks one row against the bounds with its column cells, or
// against vis with its document when a cell cannot answer.
func rowHidden(r *segRef, row int, rows segment.GroupRows, i int, bounds []hideBound, vis *query.Query) (bool, error) {
	for j, b := range bounds {
		cell := rows.Cells[j][i]

		if !cell.Known {
			raw, err := r.Raw(row)
			if err != nil {
				return false, err
			}

			return !visibleTo(vis, raw), nil
		}

		if t, ok := cellTime(cell); ok && !t.After(b.cutoff) {
			return true, nil
		}
	}

	return false, nil
}

// mergedRows streams the rows of the merged segment: tombstones first, then
// the kept rows in insertion order, as a seal does. Each document is read
// from its segment when the encoder asks for it.
func (c *Collection) mergedRows(plan compactPlan, tombs []segment.Row) func(fn func(sideRow) error) error {
	return func(fn func(sideRow) error) error {
		for i, t := range tombs {
			if err := fn(sideRow{row: i, id: t.ID, tx: t.Tx, del: true}); err != nil {
				return err
			}
		}

		for i, lr := range plan.live {
			if i&1023 == 0 && c.seal.stopping() {
				return errStopped
			}

			if lr.out < 0 {
				continue
			}

			raw, err := lr.r.Raw(lr.row)
			if err != nil {
				return err
			}

			if err := fn(sideRow{row: lr.out, id: lr.id, v: lr.v, seq: lr.seq, tx: lr.tx, doc: raw}); err != nil {
				return err
			}
		}

		return nil
	}
}

func visibleTo(vis *query.Query, raw []byte) bool {
	m, err := query.Decode(raw)

	return err == nil && vis.Match(m)
}

// tombstonesFor keeps the deletes whose id has a row in a segment before lo:
// without them that row would come back. Rows are sorted by id for a stable
// file.
func (c *Collection) tombstonesFor(gone map[string]uint64, lo int) ([]segment.Row, error) {
	var out []segment.Row

	for id, tx := range gone {
		for _, r := range c.seal.readers[:lo] {
			_, ok, err := r.ids.Find([]byte(id))
			if err != nil {
				return nil, err
			}

			if ok {
				out = append(out, segment.Row{ID: id, Del: true, Tx: tx})

				break
			}
		}
	}

	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })

	return out, nil
}

// publishMerged writes the merged segment and its key files. With no rows
// there is no file: the run just leaves the checkpoint.
func (c *Collection) publishMerged(plan compactPlan, tombs []segment.Row) (string, *segRef, segment.Info, error) {
	s := c.seal
	rows := c.mergedRows(plan, tombs)
	count := len(tombs)

	for _, lr := range plan.live {
		if lr.out >= 0 {
			count++
		}
	}

	if count == 0 {
		return "", nil, segment.Info{}, nil
	}

	name := s.newName()

	// The encoder reads the rows twice; the key files take the second pass,
	// so each document is read from its old segment twice, not three times.
	kf := c.newKeyFiles(plan.fields)
	pass := 0

	tap := func(fn func(sideRow) error) error {
		pass++

		return rows(func(r sideRow) error {
			if pass == 2 {
				if err := kf.add(r); err != nil {
					return err
				}
			}

			return fn(r)
		})
	}

	info, err := segment.PublishRows(s.fs, s.path(name), segmentRowsOf(tap), c.def.Indexes)
	if err != nil {
		return "", nil, info, err
	}

	if err := kf.write(s.path(name)); err != nil {
		return "", nil, info, err
	}

	merged, err := c.openSegment(name, plan.fields)
	if err != nil {
		return "", nil, info, err
	}

	for _, lr := range plan.live {
		if lr.out >= 0 {
			merged.live.set(lr.out)
			merged.nlive++
		}
	}

	return name, merged, info, nil
}

// segmentRowsOf turns sideRows into the rows of the segment encoder.
func segmentRowsOf(rows func(fn func(sideRow) error) error) segment.Rows {
	return func(fn func(segment.Row) error) error {
		return rows(func(r sideRow) error {
			return fn(segment.Row{ID: r.id, V: r.v, Seq: r.seq, Tx: r.tx, Del: r.del, Raw: r.doc})
		})
	}
}

// discard drops a published segment whose commit failed.
func (c *Collection) discard(r *segRef, name string) {
	if r == nil {
		return
	}

	r.closeFiles()
	c.seal.fs.Remove(c.seal.path(name))
	removeSidecars(c.seal.fs, c.seal.path(name))
}

// commitSwap replaces the run by the merged segment in meta.bolt, with its
// live bits, in one transaction. The WAL position of the checkpoint stays.
func (c *Collection) commitSwap(lo, hi int, name string, info segment.Info, merged *segRef) error {
	s := c.seal

	names := slices.Clone(s.names[:lo])

	if merged != nil {
		names = append(names, name)
	}

	names = append(names, s.names[hi:]...)

	cur, err := readCheckpoint(s.meta)
	if err != nil {
		return err
	}

	cp := checkpoint{Pos: cur.Pos, Segments: names, Next: s.peekNext(), Format: formatSidecars, Sources: cur.Sources}

	return s.meta.Update(func(tx *bolt.Tx) error {
		segs, err := tx.CreateBucketIfNotExists(bucketSegments)
		if err != nil {
			return err
		}

		for _, old := range s.names[lo:hi] {
			if err := segs.Delete([]byte(old)); err != nil {
				return err
			}
		}

		bits := map[string][]byte{}

		if merged != nil {
			infoJSON, err := json.Marshal(info)
			if err != nil {
				return err
			}

			if err := segs.Put([]byte(name), infoJSON); err != nil {
				return err
			}

			bits[name] = merged.live.bytes()
		}

		if err := putLiveBits(tx, bits, s.names[lo:hi]); err != nil {
			return err
		}

		return putCheckpoint(tx, cp)
	})
}

// swap runs on the writer goroutine: the merged segment takes the place of
// the run, and a row that changed since the plan is not live in it.
func (c *Collection) swap(plan compactPlan, lo, hi int, name string, merged *segRef) error {
	return c.exclusive(func(writer.Pos) {
		c.mu.Lock()

		defer c.mu.Unlock()

		s := c.seal
		readers := slices.Clone(s.readers[:lo])
		names := slices.Clone(s.names[:lo])

		if merged != nil {
			readers = append(readers, merged)
			names = append(names, name)
		}

		s.readers = append(readers, s.readers[hi:]...)
		s.names = append(names, s.names[hi:]...)

		for _, r := range plan.run {
			delete(s.maxTimes, r)
		}

		if merged == nil {
			return
		}

		merged.saved = merged.gen

		transferCleared(plan, merged)
	})
}

// transferCleared clears in merged the rows whose source row is no longer
// live: a write replaced or deleted them after the plan. The caller holds
// c.mu.
func transferCleared(plan compactPlan, merged *segRef) {
	if merged == nil {
		return
	}

	for _, lr := range plan.live {
		if lr.out >= 0 && merged.live.has(lr.out) && !lr.r.live.has(lr.row) {
			merged.live.clear(lr.out)
			merged.nlive--
			merged.gen++
		}
	}
}

// liveDocs iterates the raw bytes of every live document: the memtable and
// the sealed ones. The iterator keeps the segments open while it runs.
func (c *Collection) liveDocs() iter.Seq[[]byte] {
	return func(yield func([]byte) bool) {
		c.mu.RLock()

		src := c.scanSource()
		src.held = c.holdSegments()

		c.mu.RUnlock()

		defer src.release()

		for _, cd := range src.cands {
			if !yield(cd.e.doc) {
				return
			}
		}

		err := eachLive(src.segs, func(_ string, e *entry) error {
			raw, err := e.raw()
			if err != nil {
				return err
			}

			if !yield(raw) {
				return errStopped
			}

			return nil
		})
		if err != nil && !errors.Is(err, errStopped) {
			c.fail(err)
		}
	}
}

// compactDue runs on the sealer tick. It merges a run of small segments,
// rewrites a segment whose rows are mostly replaced, or drops one segment
// that is wholly past the retention.
func (c *Collection) compactDue() error {
	lo, hi, ok := c.pickRun()

	if !ok {
		return nil
	}

	return c.compactRange(lo, hi)
}

func (c *Collection) pickRun() (int, int, bool) {
	s := c.seal

	s.compacting.Lock()

	defer s.compacting.Unlock()

	if i := c.expiredSegment(); i >= 0 {
		return i, i + 1, true
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	for i, r := range s.readers {
		if r.Len() >= smallSegmentRows && 2*r.nlive < r.Len() {
			return i, i + 1, true
		}
	}

	start, rows := -1, 0

	for i := 0; i <= len(s.readers); i++ {
		small := i < len(s.readers) && s.readers[i].nlive < smallSegmentRows && rows+s.readers[i].nlive <= maxCompactRows

		switch {
		case small && start < 0:
			start, rows = i, s.readers[i].nlive
		case small:
			rows += s.readers[i].nlive
		case start >= 0:
			if i-start >= s.compactMin {
				return start, i, true
			}

			start, rows = -1, 0

			if i < len(s.readers) && s.readers[i].nlive < smallSegmentRows {
				start, rows = i, s.readers[i].nlive
			}
		}
	}

	return 0, 0, false
}

// expiredSegment returns the first segment whose time values are all past the
// retention, or -1.
func (c *Collection) expiredSegment() int {
	if c.def.Retention == "" || c.def.Time == "" {
		return -1
	}

	d, err := catalog.ParseDuration(c.def.Retention)
	if err != nil {
		return -1
	}

	cutoff := c.clock().Add(-d)

	for i, r := range c.seal.readers {
		if max, ok := c.seal.maxTime(r, c.def.Time); ok && !max.After(cutoff) {
			return i
		}
	}

	return -1
}

type segMax struct {
	t  time.Time
	ok bool
}

// maxTime is the latest value of the time field in the segment, read once
// from the time column and kept: a segment never changes. It is not ok when a
// row has no usable time value, so such a segment is never dropped as old.
// Tombstones (seq 0) hold no document and are skipped.
func (s *sealer) maxTime(r *segRef, path string) (time.Time, bool) {
	if m, ok := s.maxTimes[r]; ok {
		return m.t, m.ok
	}

	t, ok := columnMaxTime(r, path)

	s.maxTimes[r] = segMax{t: t, ok: ok}

	return t, ok
}

func columnMaxTime(r *segRef, path string) (time.Time, bool) {
	var latest time.Time

	for gi := range r.Groups() {
		rows, err := r.ReadCells(gi, []string{path}, nil)
		if err != nil {
			return time.Time{}, false
		}

		for i, seq := range rows.Seqs {
			if seq == 0 {
				continue
			}

			t, ok := cellTime(rows.Cells[0][i])
			if !ok {
				return time.Time{}, false
			}

			if t.After(latest) {
				latest = t
			}
		}
	}

	return latest, !latest.IsZero()
}

// cellTime reads an RFC 3339 string or a number of unix seconds.
func cellTime(cell segment.Cell) (time.Time, bool) {
	if !cell.Known || cell.List || len(cell.Vals) != 1 {
		return time.Time{}, false
	}

	v := cell.Vals[0]

	switch v.Kind {
	case segment.ScalarString:
		t, err := time.Parse(time.RFC3339Nano, v.Str)

		return t, err == nil
	case segment.ScalarNumber:
		return time.Unix(int64(v.Num), 0), true
	}

	return time.Time{}, false
}
