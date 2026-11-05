package collection

import (
	"encoding/json"
	"fmt"
	"iter"
	"log"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/thadeu/ndb/internal/fs"
	"github.com/thadeu/ndb/internal/recovery"
	"github.com/thadeu/ndb/internal/segment"
	"github.com/thadeu/ndb/internal/writer"
)

// Sealing (plan §7.3, §8.3, §8.4). The memtable becomes one segment, and the
// checkpoint in meta.bolt names the live segments and the WAL end they cover.
//
// A seal has four steps:
//  1. capture, on the writer goroutine: the WAL end and the memtable rows.
//     This copies pointers only.
//  2. publish the segment file, with no lock held; writes go on.
//  3. commit the checkpoint, in one bolt transaction.
//  4. adopt, on the writer goroutine: the sealed entries become stubs that
//     read from the segment, so their bytes leave memory. An entry that
//     changed after the capture keeps its memtable version.
//
// A crash before step 3 leaves an orphan file, which the next start removes.
// A crash after step 3 starts from the checkpoint and replays the WAL tail.

const (
	defaultSealBytes = 64 << 20
	defaultSealAge   = 10 * time.Minute
	defaultSealTick  = time.Second
	segmentDir       = "segments"
	metaFile         = "meta.bolt"
)

var (
	bucketSegments   = []byte("segments")
	bucketCheckpoint = []byte("checkpoint")
	keyLive          = []byte("live")
)

type sealer struct {
	dir   string
	fs    fs.FS
	meta  *bolt.DB
	enc   segment.Writer
	bytes int64
	age   time.Duration
	tick  time.Duration

	compactMin int
	onCompact  func(liveDocs iter.Seq[[]byte])
	hook       func(step string)
	maxTimes   map[*segRef]segMax

	// names, readers, next and last are guarded by Collection.mu for writes.
	// Seal runs one at a time under mu, so it reads them without c.mu.
	names   []string
	readers []*segRef
	next    int
	last    time.Time

	running  sync.Mutex
	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// checkpoint is the value of meta.bolt: the WAL end covered by the
// segments, and the live segment names in order.
type checkpoint struct {
	Pos      recovery.Position `json:"pos"`
	Segments []string          `json:"segments"`
	Next     int               `json:"next"`
}

type sealItem struct {
	id string

	// e is nil for a tombstone.
	e *entry
}

type sealSnap struct {
	pos   recovery.Position
	items []sealItem
	rows  []segment.Row
	uniq  []map[string]string
	tombs map[string]uint64
}

// openSegments opens meta.bolt and the live segments, and makes their rows
// the durable state. It returns the WAL position to replay from.
func (c *Collection) openSegments(cfg Config) (recovery.Position, error) {
	s := &sealer{
		dir:   cfg.Data,
		fs:    cfg.FS,
		enc:   segment.NewWriter(),
		bytes: cfg.SealBytes,
		age:   cfg.SealAge,
		tick:  cfg.SealTick,
		last:  time.Now(),
		stop:  make(chan struct{}),

		compactMin: cfg.CompactMin,
		onCompact:  cfg.OnCompact,
		maxTimes:   map[*segRef]segMax{},
	}

	s.setDefaults()

	c.seal = s

	if err := cfg.FS.MkdirAll(filepath.Join(cfg.Data, segmentDir)); err != nil {
		return recovery.Position{}, err
	}

	db, err := bolt.Open(filepath.Join(cfg.Data, metaFile), 0o644, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return recovery.Position{}, err
	}

	s.meta = db

	cp, err := readCheckpoint(db)
	if err != nil {
		return recovery.Position{}, err
	}

	for _, name := range cp.Segments {
		raw, err := segment.Open(cfg.FS, s.path(name))
		if err != nil {
			return recovery.Position{}, err
		}

		r := newSegRef(raw, cfg.FS, s.path(name))

		s.readers = append(s.readers, r)
		s.names = append(s.names, name)

		if err := c.loadSegment(r); err != nil {
			return recovery.Position{}, err
		}
	}

	s.next = cp.Next

	if err := s.removeOrphans(cp.Segments); err != nil {
		return recovery.Position{}, err
	}

	return cp.Pos, nil
}

func (s *sealer) setDefaults() {
	if s.bytes == 0 {
		s.bytes = defaultSealBytes
	}

	if s.age == 0 {
		s.age = defaultSealAge
	}

	if s.tick == 0 {
		s.tick = defaultSealTick
	}

	if s.compactMin == 0 {
		s.compactMin = defaultCompactMin
	}
}

func (s *sealer) path(name string) string {
	return filepath.Join(s.dir, segmentDir, name)
}

// removeOrphans deletes the files of segmentDir that the checkpoint does not
// name: tmp files and segments of a seal that did not commit.
func (s *sealer) removeOrphans(live []string) error {
	paths, err := filepath.Glob(filepath.Join(s.dir, segmentDir, "*"))
	if err != nil {
		return err
	}

	keep := map[string]bool{}

	for _, name := range live {
		keep[s.path(name)] = true
	}

	for _, p := range paths {
		if keep[p] {
			continue
		}

		if err := s.fs.Remove(p); err != nil {
			return err
		}
	}

	return nil
}

// loadSegment makes the rows of r the state of their ids, as sealed entries.
// Rows are read in file order: a later row replaces an earlier one, and a
// tombstone removes the id.
func (c *Collection) loadSegment(r segment.Reader) error {
	return r.Entries(func(row int, rec segment.Row) error {
		if rec.Del {
			c.head.remove(rec.ID)
			c.durable.remove(rec.ID)

			return nil
		}

		e := &entry{v: rec.V, seq: rec.Seq, tx: rec.Tx, loc: &location{seg: r, row: row}}

		if len(c.def.Unique) > 0 {
			raw, err := r.Raw(row)
			if err != nil {
				return err
			}

			e.uniq = uniqueKeys(raw, c.def.Unique)
		}

		c.head.put(rec.ID, e)
		c.durable.put(rec.ID, e)
		c.rd.seq = max(c.rd.seq, rec.Seq)

		return nil
	})
}

func readCheckpoint(db *bolt.DB) (checkpoint, error) {
	var cp checkpoint

	err := db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketCheckpoint)

		if b == nil {
			return nil
		}

		raw := b.Get(keyLive)

		if raw == nil {
			return nil
		}

		return json.Unmarshal(raw, &cp)
	})

	return cp, err
}

// commit writes the segment info and the checkpoint in one transaction.
func (s *sealer) commit(info segment.Info, name string, cp checkpoint) error {
	return s.meta.Update(func(tx *bolt.Tx) error {
		segs, err := tx.CreateBucketIfNotExists(bucketSegments)
		if err != nil {
			return err
		}

		infoJSON, err := json.Marshal(info)
		if err != nil {
			return err
		}

		if err := segs.Put([]byte(name), infoJSON); err != nil {
			return err
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

// Seal writes the memtable to one new segment and releases the bytes of its
// documents. A failed seal leaves the data where it was, and the next seal
// tries again. Without Data it does nothing.
func (c *Collection) Seal() error {
	s := c.seal

	if s == nil {
		return nil
	}

	s.running.Lock()

	defer s.running.Unlock()

	snap, err := c.capture()
	if err != nil {
		return err
	}

	if len(snap.items) == 0 {
		return nil
	}

	snap.uniq = c.uniqueOf(snap.items)

	name := fmt.Sprintf("%06d.parquet", s.next)

	data, info, err := s.enc.Encode(snap.rows, c.def.Indexes)
	if err != nil {
		return err
	}

	if err := segment.Publish(s.fs, s.path(name), data); err != nil {
		return err
	}

	raw, err := segment.Open(s.fs, s.path(name))
	if err != nil {
		return err
	}

	reader := newSegRef(raw, s.fs, s.path(name))

	cp := checkpoint{Pos: snap.pos, Segments: append(slices.Clone(s.names), name), Next: s.next + 1}

	if err := s.commit(info, name, cp); err != nil {
		reader.Close()

		return err
	}

	return c.adopt(snap, reader, name)
}

// capture runs on the writer goroutine, so the memtable is the state after
// the last commit that is durable, and the WAL end matches it.
func (c *Collection) capture() (sealSnap, error) {
	var snap sealSnap

	err := c.w.Run(func(end writer.Pos) {
		snap.pos = recovery.Position{Seq: end.Seq, Offset: end.Offset, Tx: end.Tx}
		snap.tombs = map[string]uint64{}

		c.mu.RLock()

		defer c.mu.RUnlock()

		var items []sealItem

		for id, e := range c.durable.docs {
			if e.doc != nil {
				items = append(items, sealItem{id: id, e: e})
			}
		}

		for id, tx := range c.tombs {
			snap.tombs[id] = tx

			if _, live := c.durable.docs[id]; !live {
				items = append(items, sealItem{id: id})
			}
		}

		sort.Slice(items, func(a, b int) bool { return sealOrder(items[a], items[b]) })

		snap.items = items
		snap.rows = make([]segment.Row, len(items))

		for i, it := range items {
			snap.rows[i] = rowOf(it, c.tombs)
		}
	})

	return snap, err
}

// sealOrder puts tombstones first, then rows in insertion order.
func sealOrder(a, b sealItem) bool {
	as, bs := uint64(0), uint64(0)

	if a.e != nil {
		as = a.e.seq
	}

	if b.e != nil {
		bs = b.e.seq
	}

	if as != bs {
		return as < bs
	}

	return a.id < b.id
}

func rowOf(it sealItem, tombs map[string]uint64) segment.Row {
	if it.e == nil {
		return segment.Row{ID: it.id, Del: true, Tx: tombs[it.id]}
	}

	return segment.Row{ID: it.id, V: it.e.v, Seq: it.e.seq, Tx: it.e.tx, Raw: it.e.doc}
}

// uniqueOf returns the unique keys of each row, for the stubs.
func (c *Collection) uniqueOf(items []sealItem) []map[string]string {
	out := make([]map[string]string, len(items))

	if len(c.def.Unique) == 0 {
		return out
	}

	for i, it := range items {
		if it.e != nil {
			out[i] = uniqueKeys(it.e.doc, c.def.Unique)
		}
	}

	return out
}

// adopt runs on the writer goroutine. It makes the segment live and turns
// the captured entries into stubs. An entry that changed after the capture
// keeps its memtable version; a tombstone that was captured is cleared.
func (c *Collection) adopt(snap sealSnap, r *segRef, name string) error {
	err := c.w.Run(func(writer.Pos) {
		c.mu.Lock()

		defer c.mu.Unlock()

		s := c.seal

		s.readers = append(s.readers, r)
		s.names = append(s.names, name)
		s.next++
		s.last = time.Now()

		for i, it := range snap.items {
			if it.e == nil {
				continue
			}

			stub := &entry{v: it.e.v, seq: it.e.seq, tx: it.e.tx, loc: &location{seg: r, row: i}, uniq: snap.uniq[i]}

			if c.durable.docs[it.id] == it.e {
				c.durable.docs[it.id] = stub
				c.memBytes -= int64(len(it.e.doc))
			}

			if cur := c.head.docs[it.id]; cur != nil && cur.v == it.e.v {
				c.head.docs[it.id] = stub
			}
		}

		for id, tx := range snap.tombs {
			if c.tombs[id] == tx {
				delete(c.tombs, id)
			}
		}
	})

	if err != nil {
		r.Close()
	}

	return err
}

// Segments is the number of live segments.
func (c *Collection) Segments() int {
	c.mu.RLock()

	defer c.mu.RUnlock()

	if c.seal == nil {
		return 0
	}

	return len(c.seal.names)
}

// sealDue tells whether the memtable is over its size limit, or dirty and
// older than its age limit.
func (c *Collection) sealDue(now time.Time) bool {
	c.mu.RLock()

	defer c.mu.RUnlock()

	s := c.seal

	if c.memBytes > 0 && c.memBytes >= s.bytes {
		return true
	}

	dirty := c.memBytes > 0 || len(c.tombs) > 0

	return dirty && now.Sub(s.last) >= s.age
}

func (c *Collection) startSealer() {
	c.seal.wg.Add(1)

	go c.runSealer()
}

func (c *Collection) runSealer() {
	defer c.seal.wg.Done()

	t := time.NewTicker(c.seal.tick)

	defer t.Stop()

	for {
		select {
		case <-c.seal.stop:
			return
		case <-t.C:
			if c.sealDue(time.Now()) {
				if err := c.Seal(); err != nil {
					log.Printf("ndb: seal failed, retrying on the next tick: %v", err)
				}
			}

			if err := c.compactDue(); err != nil {
				log.Printf("ndb: compaction failed, retrying on the next tick: %v", err)
			}

			if _, err := c.TruncateWAL(); err != nil {
				log.Printf("ndb: wal truncation failed, retrying on the next tick: %v", err)
			}
		}
	}
}

func (c *Collection) stopSealer() {
	if c.seal == nil {
		return
	}

	c.seal.stopOnce.Do(func() { close(c.seal.stop) })
	c.seal.wg.Wait()
}

// closeSegments closes the segment readers and meta.bolt. It runs after the
// writer has stopped, so no read is in flight.
func (c *Collection) closeSegments() {
	s := c.seal

	if s == nil {
		return
	}

	for _, r := range s.readers {
		r.Reader.Close()
	}

	s.readers = nil

	if s.meta != nil {
		s.meta.Close()
		s.meta = nil
	}
}
