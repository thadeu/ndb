package collection

import (
	"bytes"
	"context"
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

// openWorkers is how many segment files one open decodes at once.
const openWorkers = 4

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

	// sealing serializes the seals and compacting the compactions: a seal
	// runs while a compaction builds its segment, so writes do not wait for
	// it. commitMu serializes the commit of either one to meta.bolt with its
	// apply on the writer goroutine, so each checkpoint is made from the
	// segment list it replaces. A compaction changes names[lo:hi] and a
	// seal only appends, so the run of a compaction stays at lo:hi.
	sealing    sync.Mutex
	compacting sync.Mutex
	commitMu   sync.Mutex
	nameMu     sync.Mutex
	stop       chan struct{}
	stopOnce   sync.Once

	// sealJob and compactJob run on the scheduler (schedule.go).
	sealJob    *job
	compactJob *job
}

// checkpoint is the value of meta.bolt: the WAL end covered by the
// segments, and the live segment names in order.
type checkpoint struct {
	Pos      recovery.Position `json:"pos"`
	Segments []string          `json:"segments"`
	Next     int               `json:"next"`
	Format   int               `json:"format,omitempty"`

	// Sources are the read positions of a direct collection in its files,
	// as of the seal (opaque to this package).
	Sources json.RawMessage `json:"sources,omitempty"`
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

	// sources are the read positions of a direct collection, as of the
	// capture.
	sources json.RawMessage

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

	db, err := bolt.Open(filepath.Join(cfg.Data, metaFile), 0o644, metaOptions())
	if err != nil {
		return recovery.Position{}, err
	}

	s.meta = db

	cp, err := readCheckpoint(db)
	if err != nil {
		return recovery.Position{}, err
	}

	// The footers are decoded in parallel: on a start or a wake, opening
	// the segments is most of the time an open takes.
	raws, err := runPool(context.Background(), openWorkers, len(cp.Segments), func(i int) (segment.Reader, error) {
		return segment.Open(cfg.FS, s.path(cp.Segments[i]))
	})
	if err != nil {
		return recovery.Position{}, err
	}

	for i, name := range cp.Segments {
		s.readers = append(s.readers, newSegRef(raws[i], cfg.FS, s.path(name)))
		s.names = append(s.names, name)
	}

	s.next = cp.Next
	c.savedSources = cp.Sources

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

	s.sealing.Lock()

	defer s.sealing.Unlock()

	snap, err := c.capture()
	if err != nil {
		return err
	}

	if len(snap.items) == 0 {
		return nil
	}

	defer s.compactJob.poke(time.Now().Add(s.tick))

	name := s.newName()
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

	s.commitMu.Lock()
	defer s.commitMu.Unlock()

	bits := s.liveOnly(snap.bits)
	bits[name] = reader.live.bytes()

	cp := checkpoint{Pos: snap.pos, Segments: append(slices.Clone(s.names), name), Next: s.peekNext(), Format: formatSidecars, Sources: snap.sources}

	if err := s.commit(info, name, cp, bits); err != nil {
		reader.closeFiles()

		return err
	}

	return c.adopt(snap, reader, name)
}

// newName is the file name of a new segment. A seal and a compaction may
// both make one at the same time.
func (s *sealer) newName() string {
	s.nameMu.Lock()
	defer s.nameMu.Unlock()

	name := fmt.Sprintf("%06d.parquet", s.next)
	s.next++

	return name
}

// peekNext is the number of the next segment name, for the checkpoint.
func (s *sealer) peekNext() int {
	s.nameMu.Lock()
	defer s.nameMu.Unlock()

	return s.next
}

// liveOnly keeps the bits of the segments that are still live: a compaction
// may have replaced some since the capture. The caller holds s.commitMu.
func (s *sealer) liveOnly(bits map[string][]byte) map[string][]byte {
	out := map[string][]byte{}

	for _, name := range s.names {
		if b, ok := bits[name]; ok {
			out[name] = b
		}
	}

	return out
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

	err := c.exclusive(func(end writer.Pos) {
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

		if c.sources != nil {
			snap.sources = c.sources()
		}
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
	err := c.exclusive(func(writer.Pos) {
		c.mu.Lock()

		defer c.mu.Unlock()

		s := c.seal

		s.readers = append(s.readers, r)
		s.names = append(s.names, name)
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

// writeHold is how many times its seal size a memtable may grow before
// writes wait for a seal (waitRoom).
const writeHold = 2

// waitRoom holds a write while the memtable is over writeHold times its seal
// size, and wakes the sealer: when writes come faster than seals, they slow
// down instead of growing the memory.
func (c *Collection) waitRoom(ctx context.Context) error {
	s := c.seal

	if s == nil {
		return nil
	}

	limit := writeHold * min(s.bytes, c.shared.budget())

	for {
		c.mu.RLock()
		mem := c.memBytes
		c.mu.RUnlock()

		if mem < limit || s.stopping() {
			return nil
		}

		s.sealJob.poke(time.Now())

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// idleChecks is how many seal ticks apart a collection at rest checks the
// work that comes with time, not with writes: WAL files a reader or PITR
// held, segments past the retention (1 min with the default tick).
const idleChecks = 60

func (c *Collection) startSealer() {
	s := c.seal
	soon := time.Now().Add(s.tick)

	s.sealJob = newJob(c.sealTick)
	s.compactJob = newJob(c.compactTick)
	c.shared.join(c, s.tick)
	s.sealJob.poke(soon)
	s.compactJob.poke(soon)
	c.shared.written()
}

// dirtied gives the seal job a deadline after a write: now when the
// memtable passed its seal size, else when it turns seal_age old (a tick
// away at least). Callers hold c.mu.
func (c *Collection) dirtied() {
	s := c.seal

	if s == nil {
		return
	}

	now := time.Now()

	if c.memBytes >= s.bytes {
		s.sealJob.poke(now)
	} else {
		s.sealJob.poke(s.sealAt(now))
	}

	c.shared.written()
}

// sealAt is when a dirty memtable is due by age, a tick from now at least.
// Callers hold c.mu.
func (s *sealer) sealAt(now time.Time) time.Time {
	return later(s.last.Add(s.age), now.Add(s.tick))
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}

	return b
}

// sealTick seals a memtable that is due and deletes the WAL files that the
// seals cover. The next check is when the memtable turns seal_age old (a
// write past the seal size or the write buffer pokes it sooner),
// idleChecks ticks away while old WAL files wait for a reader, and none
// when the collection is at rest.
func (c *Collection) sealTick() time.Time {
	s := c.seal
	now := time.Now()

	if c.sealDue(now) {
		if err := c.Seal(); err != nil && !errors.Is(err, errStopped) {
			log.Printf("ndb: seal failed, retrying on the next tick: %v", err)
		}
	}

	if _, err := c.TruncateWAL(); err != nil {
		log.Printf("ndb: wal truncation failed, retrying on the next tick: %v", err)
	}

	var next time.Time

	if c.walHeld() {
		next = now.Add(idleChecks * s.tick)
	}

	c.mu.RLock()

	if c.memBytes > 0 || len(c.tombs) > 0 {
		next = earliest(next, s.sealAt(now))
	}

	c.mu.RUnlock()

	return next
}

// earliest is the earlier of two deadlines; a zero one is none.
func earliest(a, b time.Time) time.Time {
	if a.IsZero() || b.Before(a) {
		return b
	}

	return a
}

// compactTick runs one compaction that is due, and checks again a tick
// later while there was one; a collection with a retention checks every
// idleChecks ticks for segments that time made old.
func (c *Collection) compactTick() time.Time {
	s := c.seal
	now := time.Now()

	if lo, hi, ok := c.pickRun(); ok {
		if err := c.compactRange(lo, hi); err != nil && !errors.Is(err, errStopped) {
			log.Printf("ndb: compaction failed, retrying on the next tick: %v", err)
		}

		return now.Add(s.tick)
	}

	if c.def.Retention != "" && c.def.Time != "" && c.Segments() > 0 {
		return now.Add(idleChecks * s.tick)
	}

	return time.Time{}
}

// walHeld tells whether WAL files wait to be truncated (only with
// wal_retention: without it the WAL is not truncated).
func (c *Collection) walHeld() bool {
	if c.walKeep <= 0 || c.feed == nil {
		return false
	}

	files, err := c.feed.files()

	return err == nil && len(files) > 1
}

// StopWork tells the seals and compactions in progress to stop, and none to
// start: the first step of a shutdown, so it does not wait for the merge
// of big segments (or for a request that runs one). Writes still work and
// stay in the WAL. Close finishes the stop.
func (c *Collection) StopWork() {
	if c.seal != nil {
		c.seal.stopOnce.Do(func() { close(c.seal.stop) })
	}
}

func (c *Collection) stopSealer() {
	if c.seal == nil {
		return
	}

	c.seal.stopOnce.Do(func() { close(c.seal.stop) })
	c.seal.sealJob.stop()
	c.seal.compactJob.stop()
}

// closeSegments closes the segment readers and meta.bolt. It runs after the
// writer has stopped, so no read is in flight. A compaction started by a
// call (Compact, not the sealer) can still run: it sees the stop at its next
// step, and closeSegments waits for it under compacting, so the compaction
// never reads a reader that is closed or gone.
func (c *Collection) closeSegments() {
	s := c.seal

	if s == nil {
		return
	}

	s.compacting.Lock()

	defer s.compacting.Unlock()

	for _, r := range s.readers {
		r.closeFiles()
	}

	s.readers = nil

	if s.meta != nil {
		s.meta.Close()
		s.meta = nil
	}
}

// metaPageSize is the page of a new meta.bolt. bbolt takes the OS page by
// default: 16 KiB on Apple Silicon made every meta.bolt 128 KiB, against
// 32 KiB on Linux. An existing file keeps the page of its header.
const metaPageSize = 4096

func metaOptions() *bolt.Options {
	return &bolt.Options{Timeout: time.Second, PageSize: metaPageSize}
}
