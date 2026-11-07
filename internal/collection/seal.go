package collection

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"log"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/thadeu/ndb/internal/fs"
	"github.com/thadeu/ndb/internal/index"
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
	bucketLiveBits   = []byte("live_bits")
	keyLive          = []byte("live")
)

// formatSidecars is the checkpoint format whose segments have key files and
// saved live bits (M16). An older checkpoint is migrated once at open.
const formatSidecars = 2

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
	Format   int               `json:"format,omitempty"`
}

type sealItem struct {
	id string

	// e is nil for a tombstone.
	e *entry
}

type sealSnap struct {
	pos    recovery.Position
	items  []sealItem
	rows   []segment.Row
	tombs  map[string]uint64
	fields []indexField

	// bits are the live bits that changed since meta.bolt saved them, as of
	// the capture; gens is the gen of each such segment.
	bits map[string][]byte
	gens map[*segRef]uint64
}

// openSegments opens meta.bolt, the live segments, their key files and their
// live bits. It returns the WAL position to replay from.
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

	for _, st := range []*state{c.head, c.durable} {
		st.sealed, st.owners = c.sealedGet, c.sealedOwners
	}

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

		s.readers = append(s.readers, newSegRef(raw, cfg.FS, s.path(name)))
		s.names = append(s.names, name)
	}

	s.next = cp.Next

	if err := s.removeOrphans(cp.Segments); err != nil {
		return recovery.Position{}, err
	}

	fields := c.declaredFields()

	if cp.Format < formatSidecars && len(cp.Segments) > 0 {
		if err := c.migrate(fields, cp); err != nil {
			return recovery.Position{}, err
		}
	}

	if err := c.loadSidecars(fields); err != nil {
		return recovery.Position{}, err
	}

	return cp.Pos, nil
}

// declaredFields are the index fields of the definition, which every
// segment written by this version has.
func (c *Collection) declaredFields() []indexField {
	out := make([]indexField, 0, len(c.def.Indexes))

	for _, f := range c.def.Indexes {
		out = append(out, indexField{name: f, isTime: f == c.def.Time})
	}

	return out
}

// loadSidecars opens the key files and reads the live bits of every segment.
func (c *Collection) loadSidecars(fields []indexField) error {
	s := c.seal

	bits, err := readLiveBits(s.meta)
	if err != nil {
		return err
	}

	for i, r := range s.readers {
		if err := c.openSidecars(r, fields); err != nil {
			return err
		}

		raw, ok := bits[s.names[i]]
		if !ok {
			return fmt.Errorf("segment %s: no live bits in %s", s.names[i], metaFile)
		}

		r.live = bitsetOf(raw)
		r.nlive = r.live.count()
		c.rd.seq = max(c.rd.seq, r.ids.MaxSeq())
	}

	return nil
}

func readLiveBits(db *bolt.DB) (map[string][]byte, error) {
	out := map[string][]byte{}

	err := db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketLiveBits)

		if b == nil {
			return nil
		}

		return b.ForEach(func(k, v []byte) error {
			out[string(k)] = bytes.Clone(v)

			return nil
		})
	})

	return out, err
}

// putLiveBits writes the live bits of the named segments in a bolt
// transaction, and deletes the bits of gone.
func putLiveBits(tx *bolt.Tx, bits map[string][]byte, gone []string) error {
	b, err := tx.CreateBucketIfNotExists(bucketLiveBits)
	if err != nil {
		return err
	}

	for _, name := range gone {
		if err := b.Delete([]byte(name)); err != nil {
			return err
		}
	}

	for name, raw := range bits {
		if err := b.Put([]byte(name), raw); err != nil {
			return err
		}
	}

	return nil
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

// removeOrphans deletes the files of segmentDir that belong to no segment of
// the checkpoint: tmp files, and the files of a seal or a compaction that did
// not commit.
func (s *sealer) removeOrphans(live []string) error {
	paths, err := filepath.Glob(filepath.Join(s.dir, segmentDir, "*"))
	if err != nil {
		return err
	}

	keep := map[string]bool{}

	for _, name := range live {
		keep[strings.TrimSuffix(name, ".parquet")] = true
	}

	for _, p := range paths {
		base, _, _ := strings.Cut(filepath.Base(p), ".")

		if keep[base] && !strings.HasSuffix(p, ".tmp") {
			continue
		}

		if err := s.fs.Remove(p); err != nil {
			return err
		}
	}

	return nil
}

// migrate gives the segments of an older version their key files and live
// bits, once. It reads every row id, so it needs memory for one map of the
// ids; the next opens do not.
func (c *Collection) migrate(fields []indexField, cp checkpoint) error {
	s := c.seal

	type at struct {
		seg, row int
		del      bool
	}

	latest := map[string]at{}

	for si, r := range s.readers {
		err := r.Entries(func(row int, rec segment.Row) error {
			latest[rec.ID] = at{seg: si, row: row, del: rec.Del}

			return nil
		})
		if err != nil {
			return err
		}
	}

	bits := map[string][]byte{}

	for si, r := range s.readers {
		live := newBitset(r.Len())

		for _, a := range latest {
			if a.seg == si && !a.del {
				live.set(a.row)
			}
		}

		if err := c.writeSidecars(r.path, fields, segmentRows(r)); err != nil {
			return err
		}

		bits[s.names[si]] = live.bytes()
	}

	cp.Format = formatSidecars

	return s.meta.Update(func(tx *bolt.Tx) error {
		if err := putLiveBits(tx, bits, nil); err != nil {
			return err
		}

		return putCheckpoint(tx, cp)
	})
}

// segmentRows reads the rows of a segment for its key files.
func segmentRows(r segment.Reader) func(fn func(sideRow) error) error {
	return func(fn func(sideRow) error) error {
		return r.Entries(func(row int, rec segment.Row) error {
			sr := sideRow{row: row, id: rec.ID, v: rec.V, seq: rec.Seq, tx: rec.Tx, del: rec.Del}

			if !rec.Del {
				raw, err := r.Raw(row)
				if err != nil {
					return err
				}

				sr.doc = raw
			}

			return fn(sr)
		})
	}
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

// commit writes the segment info, the live bits that changed and the
// checkpoint in one transaction.
func (s *sealer) commit(info segment.Info, name string, cp checkpoint, bits map[string][]byte) error {
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

		if err := putLiveBits(tx, bits, nil); err != nil {
			return err
		}

		return putCheckpoint(tx, cp)
	})
}

func putCheckpoint(tx *bolt.Tx, cp checkpoint) error {
	cps, err := tx.CreateBucketIfNotExists(bucketCheckpoint)
	if err != nil {
		return err
	}

	cpJSON, err := json.Marshal(cp)
	if err != nil {
		return err
	}

	return cps.Put(keyLive, cpJSON)
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

	if !c.shared.acquire(s.stop) {
		return errStopped
	}

	defer c.shared.release()

	snap, err := c.capture()
	if err != nil {
		return err
	}

	if len(snap.items) == 0 {
		return nil
	}

	name := fmt.Sprintf("%06d.parquet", s.next)
	path := s.path(name)

	info, err := segment.PublishRows(s.fs, path, segment.RowsOf(snap.rows), c.def.Indexes)
	if err != nil {
		return err
	}

	if err := c.writeSidecars(path, snap.fields, snapRows(snap.rows)); err != nil {
		return err
	}

	reader, err := c.openSegment(name, snap.fields)
	if err != nil {
		return err
	}

	for i, r := range snap.rows {
		if !r.Del {
			reader.live.set(i)
			reader.nlive++
		}
	}

	snap.bits[name] = reader.live.bytes()

	cp := checkpoint{Pos: snap.pos, Segments: append(slices.Clone(s.names), name), Next: s.next + 1, Format: formatSidecars}

	if err := s.commit(info, name, cp, snap.bits); err != nil {
		reader.closeFiles()

		return err
	}

	return c.adopt(snap, reader, name)
}

// openSegment opens a new segment and its key files, with no live row yet.
func (c *Collection) openSegment(name string, fields []indexField) (*segRef, error) {
	s := c.seal

	raw, err := segment.Open(s.fs, s.path(name))
	if err != nil {
		return nil, err
	}

	r := newSegRef(raw, s.fs, s.path(name))

	if err := c.openSidecars(r, fields); err != nil {
		r.closeFiles()

		return nil, err
	}

	r.live = newBitset(r.Len())

	return r, nil
}

// snapRows are the rows of a seal, for its key files.
func snapRows(rows []segment.Row) func(fn func(sideRow) error) error {
	return func(fn func(sideRow) error) error {
		for i, r := range rows {
			if err := fn(sideRow{row: i, id: r.ID, v: r.V, seq: r.Seq, tx: r.Tx, del: r.Del, doc: r.Raw}); err != nil {
				return err
			}
		}

		return nil
	}
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
			items = append(items, sealItem{id: id, e: e})
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

		snap.fields = c.indexFields()
		snap.bits, snap.gens = c.changedBits()
	})

	return snap, err
}

// changedBits copies the live bits that changed since meta.bolt saved them.
// The caller holds c.mu.
func (c *Collection) changedBits() (map[string][]byte, map[*segRef]uint64) {
	bits := map[string][]byte{}
	gens := map[*segRef]uint64{}

	for i, r := range c.seal.readers {
		if r.gen != r.saved {
			bits[c.seal.names[i]] = r.live.bytes()
			gens[r] = r.gen
		}
	}

	return bits, gens
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

// adopt runs on the writer goroutine. It makes the segment live and takes
// the sealed documents out of the memtable. A document that changed after
// the capture keeps its memtable version, and its row in the segment is not
// live; a tombstone that was captured is cleared.
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

			if c.durable.docs[it.id] == it.e {
				c.durable.release(it.id)
				c.memBytes -= int64(len(it.e.doc))
			} else {
				r.live.clear(i)
				r.nlive--
				r.gen++
			}

			if cur := c.head.docs[it.id]; cur != nil && cur.v == it.e.v {
				c.head.release(it.id)
			}
		}

		for id, tx := range snap.tombs {
			if c.tombs[id] == tx {
				delete(c.tombs, id)
			}

			delete(c.durable.gone, id)
			delete(c.head.gone, id)
		}

		for seg, gen := range snap.gens {
			seg.saved = gen
		}

		c.durable.shrink()
		c.head.shrink()
		c.tombs = clone(c.tombs)
		c.rebuildMemIndexes()
	})

	if err != nil {
		r.closeFiles()
	}

	return err
}

// rebuildMemIndexes makes the memtable indexes again from the documents
// that stay in the memtable after a seal: usually a few, so this is cheaper
// than taking each sealed document out. The caller holds c.mu for writing.
func (c *Collection) rebuildMemIndexes() {
	for _, s := range c.rd.idx {
		s.mem = index.NewMem()
	}

	if len(c.rd.idx) == 0 {
		return
	}

	for id, e := range c.durable.docs {
		m := c.decodeEntry(e)

		if m == nil {
			continue
		}

		for _, s := range c.rd.idx {
			for _, k := range index.Keys(m, s.field, s.isTime) {
				s.mem.Add(k, id)
			}
		}
	}
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

	if c.shared.overBudget(c, c.memBytes) {
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
				if err := c.Seal(); err != nil && !errors.Is(err, errStopped) {
					log.Printf("ndb: seal failed, retrying on the next tick: %v", err)
				}
			}

			if err := c.compactDue(); err != nil && !errors.Is(err, errStopped) {
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
		r.closeFiles()
	}

	s.readers = nil

	if s.meta != nil {
		s.meta.Close()
		s.meta = nil
	}
}
