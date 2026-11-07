package collection

import (
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
	"github.com/thadeu/ndb/internal/wal"
	"github.com/thadeu/ndb/internal/writer"
)

// Compaction (plan §7.3, §8.3). It merges adjacent sealed segments into one
// and drops what no read can see: superseded versions, tombstones that no
// older segment needs, and rows that are expired or past the retention.
//
// A compaction of the run names[lo:hi] has five steps, serialized with Seal:
//  1. plan, on the writer goroutine: the live entries that point into the run.
//  2. build the merged segment from them, with no lock held; writes go on.
//  3. publish the file (tmp + fsync + rename + fsync dir).
//  4. commit: one bolt transaction replaces the run by the new segment in the
//     checkpoint. The WAL position of the checkpoint does not change.
//  5. swap, on the writer goroutine: the stubs that still point into the run
//     point to the new segment, and the run is retired. A retired segment
//     closes and its file goes when the last query that holds it ends
//     (segref.go).
//
// A crash before step 4 leaves an orphan file, removed at the next open. A
// crash after it starts from the new checkpoint, and the old files are
// orphans. An entry that changed after step 1 keeps its newer version; the
// merged segment only holds a dead row for it.
//
// Load replays the rows of every segment in order, so a row that is dropped
// must not let an older segment bring its id back. A tombstone is written
// for such an id when an older segment (before lo) holds a row of it.

// defaultCompactMin is the number of adjacent small segments that start a
// compaction on the sealer tick. A segment is small under smallSegmentRows.
const (
	defaultCompactMin = 4
	smallSegmentRows  = 100_000
)

// Compact merges every segment into one. Without segments it does nothing.
func (c *Collection) Compact() error {
	if c.seal == nil {
		return nil
	}

	c.seal.running.Lock()

	n := len(c.seal.readers)

	c.seal.running.Unlock()

	if n == 0 {
		return nil
	}

	return c.compactRange(0, n)
}

type liveRow struct {
	id string
	e  *entry
	r  *segRef
}

type compactPlan struct {
	run  []*segRef
	live []liveRow
	now  time.Time
}

// compactRange merges the segments lo..hi-1.
func (c *Collection) compactRange(lo, hi int) error {
	s := c.seal

	if s == nil {
		return nil
	}

	s.running.Lock()

	defer s.running.Unlock()

	if lo < 0 || hi > len(s.readers) || lo >= hi {
		return fmt.Errorf("compact: bad range %d..%d of %d segments", lo, hi, len(s.readers))
	}

	plan, err := c.planCompact(lo, hi)
	if err != nil {
		return err
	}

	rows, drop, err := c.buildRows(plan, lo)
	if err != nil {
		return err
	}

	name, merged, info, err := c.publishMerged(rows)
	if err != nil {
		return err
	}

	s.step("published")

	if err := c.commitSwap(lo, hi, name, info, merged != nil); err != nil {
		c.discard(merged, name)

		return err
	}

	s.step("committed")

	if err := c.swap(plan, rows, drop, lo, hi, name, merged); err != nil {
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

// errStopped ends a compaction because the collection is closing (a drop,
// a truncate, a shutdown): its result would be thrown away.
var errStopped = errors.New("compaction stopped: the collection is closing")

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

// planCompact lists the live entries that point into the run. It runs on the
// writer goroutine, so the entries are one point in the commit order.
func (c *Collection) planCompact(lo, hi int) (compactPlan, error) {
	plan := compactPlan{run: slices.Clone(c.seal.readers[lo:hi]), now: c.clock()}
	in := map[*segRef]bool{}

	for _, r := range plan.run {
		in[r] = true
	}

	err := c.w.Run(func(writer.Pos) {
		c.mu.RLock()

		defer c.mu.RUnlock()

		for id, e := range c.durable.docs {
			if e.doc != nil {
				continue
			}

			if r, ok := e.loc.seg.(*segRef); ok && in[r] {
				plan.live = append(plan.live, liveRow{id: id, e: e, r: r})
			}
		}
	})

	sort.Slice(plan.live, func(a, b int) bool { return plan.live[a].e.seq < plan.live[b].e.seq })

	return plan, err
}

// buildRows makes the rows of the merged segment: tombstones first, then the
// live rows in insertion order, as a seal does. drop holds the ids of live
// entries that are left out because no read can see them.
func (c *Collection) buildRows(plan compactPlan, lo int) ([]segment.Row, map[string]*entry, error) {
	vis, err := c.visibility(plan.now)
	if err != nil {
		return nil, nil, err
	}

	var (
		kept []segment.Row
		drop = map[string]*entry{}
		gone = map[string]segment.Row{}
	)

	for i, lr := range plan.live {
		if i&1023 == 0 && c.seal.stopping() {
			return nil, nil, errStopped
		}

		raw, err := lr.e.loc.seg.Raw(lr.e.loc.row)
		if err != nil {
			return nil, nil, err
		}

		if vis != nil && !visibleTo(vis, raw) {
			drop[lr.id] = lr.e
			gone[lr.id] = segment.Row{ID: lr.id, Del: true, Tx: lr.e.tx}

			continue
		}

		kept = append(kept, segment.Row{ID: lr.id, V: lr.e.v, Seq: lr.e.seq, Tx: lr.e.tx, Raw: raw})
	}

	if err := c.runTombstones(plan, gone); err != nil {
		return nil, nil, err
	}

	tombs, err := c.tombstonesFor(gone, lo)
	if err != nil {
		return nil, nil, err
	}

	return append(tombs, kept...), drop, nil
}

func visibleTo(vis *query.Query, raw []byte) bool {
	m, err := query.Decode(raw)

	return err == nil && vis.Match(m)
}

// runTombstones adds to gone the deletes that the run holds. The one with the
// highest tx of an id wins.
func (c *Collection) runTombstones(plan compactPlan, gone map[string]segment.Row) error {
	for _, r := range plan.run {
		err := r.Entries(func(_ int, rec segment.Row) error {
			if rec.Del && rec.Tx >= gone[rec.ID].Tx {
				gone[rec.ID] = segment.Row{ID: rec.ID, Del: true, Tx: rec.Tx}
			}

			return nil
		})
		if err != nil {
			return err
		}
	}

	return nil
}

// tombstonesFor keeps the deletes whose id has a row in a segment before lo:
// without them that row would come back at the next load. Rows are sorted by
// id for a stable file.
func (c *Collection) tombstonesFor(gone map[string]segment.Row, lo int) ([]segment.Row, error) {
	if lo == 0 || len(gone) == 0 {
		return nil, nil
	}

	older := map[string]bool{}

	for _, r := range c.seal.readers[:lo] {
		err := r.Entries(func(_ int, rec segment.Row) error {
			if _, want := gone[rec.ID]; want {
				older[rec.ID] = true
			}

			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	var out []segment.Row

	for id, row := range gone {
		if older[id] {
			out = append(out, row)
		}
	}

	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })

	return out, nil
}

// publishMerged encodes and writes the merged segment. With no rows there is
// no file: the run just leaves the checkpoint.
func (c *Collection) publishMerged(rows []segment.Row) (string, *segRef, segment.Info, error) {
	s := c.seal

	if len(rows) == 0 {
		return "", nil, segment.Info{}, nil
	}

	name := fmt.Sprintf("%06d.parquet", s.next)

	data, info, err := s.enc.Encode(rows, c.def.Indexes)
	if err != nil {
		return "", nil, info, err
	}

	if err := segment.Publish(s.fs, s.path(name), data); err != nil {
		return "", nil, info, err
	}

	raw, err := segment.Open(s.fs, s.path(name))
	if err != nil {
		return "", nil, info, err
	}

	return name, newSegRef(raw, s.fs, s.path(name)), info, nil
}

// discard drops a published segment whose commit failed.
func (c *Collection) discard(r *segRef, name string) {
	if r == nil {
		return
	}

	r.Reader.Close()
	c.seal.fs.Remove(c.seal.path(name))
}

// commitSwap replaces the run by the merged segment in meta.bolt, in one
// transaction. The WAL position of the checkpoint stays.
func (c *Collection) commitSwap(lo, hi int, name string, info segment.Info, hasRows bool) error {
	s := c.seal

	names := slices.Clone(s.names[:lo])

	if hasRows {
		names = append(names, name)
	}

	names = append(names, s.names[hi:]...)

	cur, err := readCheckpoint(s.meta)
	if err != nil {
		return err
	}

	cp := checkpoint{Pos: cur.Pos, Segments: names, Next: s.next + 1}

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

		if hasRows {
			infoJSON, err := json.Marshal(info)
			if err != nil {
				return err
			}

			if err := segs.Put([]byte(name), infoJSON); err != nil {
				return err
			}
		}

		cps, err := tx.CreateBucketIfNotExists(bucketCheckpoint)
		if err != nil {
			return err
		}

		cpJSON, err := json.Marshal(cp)
		if err != nil {
			return err
		}

		return cps.Put(keyLive, cpJSON)
	})
}

// swap runs on the writer goroutine: it points the entries that still use the
// run to the merged segment, and removes the dropped ones.
func (c *Collection) swap(plan compactPlan, rows []segment.Row, drop map[string]*entry, lo, hi int, name string, merged *segRef) error {
	rowOf := map[string]int{}

	for i, r := range rows {
		if !r.Del {
			rowOf[r.ID] = i
		}
	}

	return c.w.Run(func(writer.Pos) {
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
		s.next++

		for _, r := range plan.run {
			delete(s.maxTimes, r)
		}

		for _, lr := range plan.live {
			c.repoint(lr, rowOf, drop, merged)
		}
	})
}

// repoint moves one live entry of the run to its row in the merged segment, or
// removes it when the compaction dropped it. An entry that changed since the
// plan is left alone.
func (c *Collection) repoint(lr liveRow, rowOf map[string]int, drop map[string]*entry, merged *segRef) {
	cur := c.durable.docs[lr.id]
	head := c.head.docs[lr.id]
	headOld := head != nil && head.loc != nil && head.loc.seg == lr.e.loc.seg && head.loc.row == lr.e.loc.row

	if _, dropped := drop[lr.id]; dropped {
		if cur == lr.e {
			c.dropEntry(lr.id)
		} else if headOld {
			c.head.remove(lr.id)
		}

		return
	}

	stub := &entry{v: lr.e.v, seq: lr.e.seq, tx: lr.e.tx, loc: &location{seg: merged, row: rowOf[lr.id]}, uniq: lr.e.uniq}

	if cur == lr.e {
		c.durable.docs[lr.id] = stub
	}

	if headOld {
		c.head.docs[lr.id] = stub
	}
}

// dropEntry removes a document from the state without a WAL record: the
// compaction dropped it because it is expired or past the retention. A delete
// that waits for the next seal stays as it was.
func (c *Collection) dropEntry(id string) {
	prev, had := c.tombs[id]

	c.applyDurable(wal.Op{Kind: wal.Del, ID: id}, 0)

	if had {
		c.tombs[id] = prev
	} else {
		delete(c.tombs, id)
	}

	c.head.remove(id)
}

// liveDocs iterates the raw bytes of every live document: the memtable and
// the sealed ones. The iterator keeps the segments open while it runs.
func (c *Collection) liveDocs() iter.Seq[[]byte] {
	return func(yield func([]byte) bool) {
		c.mu.RLock()

		held := c.holdSegments()
		entries := make([]*entry, 0, len(c.durable.docs))

		for _, e := range c.durable.docs {
			entries = append(entries, e)
		}

		c.mu.RUnlock()

		defer releaseAll(held)

		for _, e := range entries {
			raw, err := e.raw()
			if err != nil {
				c.fail(err)

				return
			}

			if !yield(raw) {
				return
			}
		}
	}
}

// compactDue runs on the sealer tick. It merges a run of small segments, or
// one segment that is wholly past the retention.
func (c *Collection) compactDue() error {
	lo, hi, ok := c.pickRun()

	if !ok {
		return nil
	}

	return c.compactRange(lo, hi)
}

func (c *Collection) pickRun() (int, int, bool) {
	s := c.seal

	s.running.Lock()

	defer s.running.Unlock()

	if i := c.expiredSegment(); i >= 0 {
		return i, i + 1, true
	}

	start := -1

	for i := 0; i <= len(s.readers); i++ {
		small := i < len(s.readers) && s.readers[i].Len() < smallSegmentRows

		switch {
		case small && start < 0:
			start = i
		case !small && start >= 0:
			if i-start >= s.compactMin {
				return start, i, true
			}

			start = -1
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
		rows, err := r.ReadGroup(gi, []string{path})
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
