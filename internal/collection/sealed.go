package collection

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math/bits"
	"os"
	"strings"

	"github.com/thadeu/ndb/internal/fs"
	"github.com/thadeu/ndb/internal/index"
	"github.com/thadeu/ndb/internal/query"
	"github.com/thadeu/ndb/internal/segment"
)

// Sealed documents live on disk (M16). The memtable is the only map of
// documents; a sealed document is found through the key files of its
// segment (segment/keys.go), and the rows that are still the current version
// of their document are marked in one bitmap per segment (live).
//
//   - A lookup by id asks the memtable first, then the segments from the
//     newest to the oldest: the first row found is the current version, and a
//     tombstone means the id is gone.
//   - The live bits are the durable view of the sealed rows. A write that
//     replaces or deletes a sealed document clears the bit of its row; a scan
//     reads only the rows whose bit is set.
//   - The bits are saved with each checkpoint (meta.bolt). After a crash the
//     WAL tail replays the writes since the checkpoint, which clear the same
//     bits again.
//
// Each segment has these files next to it:
//
//	000007.parquet      the rows
//	000007.ids          id → row, version, position, tx, tombstone (bloom)
//	000007.k.<hex>      secondary index of a field: encoded key → row
//	000007.u.<hex>      unique field: canonical value + 0x00 + id → row

// bitset is one bit per row of a segment.
type bitset []uint64

func newBitset(n int) bitset { return make(bitset, (n+63)/64) }

func (b bitset) set(i int)      { b[i>>6] |= 1 << (uint(i) & 63) }
func (b bitset) clear(i int)    { b[i>>6] &^= 1 << (uint(i) & 63) }
func (b bitset) has(i int) bool { return b[i>>6]&(1<<(uint(i)&63)) != 0 }

func (b bitset) count() int {
	n := 0

	for _, w := range b {
		n += bits.OnesCount64(w)
	}

	return n
}

func (b bitset) and(o bitset) {
	for i := range b {
		b[i] &= o[i]
	}
}

// orLive adds the rows of o that are in live.
func (b bitset) orLive(o, live bitset) {
	for i := range b {
		b[i] |= o[i] & live[i]
	}
}

func (b bitset) clone() bitset { return append(bitset(nil), b...) }

func (b bitset) bytes() []byte {
	out := make([]byte, 8*len(b))

	for i, w := range b {
		binary.LittleEndian.PutUint64(out[8*i:], w)
	}

	return out
}

func bitsetOf(raw []byte) bitset {
	out := make(bitset, len(raw)/8)

	for i := range out {
		out[i] = binary.LittleEndian.Uint64(raw[8*i:])
	}

	return out
}

// sidecar paths of a segment file.
func sidecarBase(segPath string) string { return strings.TrimSuffix(segPath, ".parquet") }

func idsPath(segPath string) string { return sidecarBase(segPath) + ".ids" }

// indexPath is the key file of an index field. "k" is the key encoding in
// which an RFC 3339 string of any field is a time; the files of the older
// encoding (".i.") are not read, and each open builds the new ones.
func indexPath(segPath, field string) string {
	return sidecarBase(segPath) + ".k." + hex.EncodeToString([]byte(field))
}

// legacyIndexPath is the key file of the older encoding, removed when the
// new one is built.
func legacyIndexPath(segPath, field string) string {
	return sidecarBase(segPath) + ".i." + hex.EncodeToString([]byte(field))
}

func uniquePath(segPath, field string) string {
	return sidecarBase(segPath) + ".u." + hex.EncodeToString([]byte(field))
}

// sideRow is one row of a segment, as the key files need it. doc is nil for
// a tombstone.
type sideRow struct {
	row int
	id  string
	v   uint64
	seq uint64
	tx  uint64
	del bool
	doc []byte
}

// indexField is a secondary index whose key files a segment gets: its name
// (one field, or the fields of a compound index with commas) and the time
// field of the collection.
type indexField struct {
	name string
	time string
}

// writeSidecars writes the key files of the segment at segPath: the ids, one
// file per index field and one per unique field.
func (c *Collection) writeSidecars(segPath string, fields []indexField, rows func(fn func(sideRow) error) error) error {
	kf := c.newKeyFiles(fields)

	if err := rows(kf.add); err != nil {
		return err
	}

	return kf.write(segPath)
}

// keyFiles collects the key entries of one segment. The rows of one segment
// are held in memory as key entries: a segment is never larger than a seal
// or maxCompactRows.
type keyFiles struct {
	fsys   fs.FS
	fields []indexField
	unique []string

	ids  []segment.KeyEntry
	idx  [][]segment.KeyEntry
	uniq [][]segment.KeyEntry
}

func (c *Collection) newKeyFiles(fields []indexField) *keyFiles {
	return &keyFiles{
		fsys:   c.seal.fs,
		fields: fields,
		unique: c.def.Unique,
		idx:    make([][]segment.KeyEntry, len(fields)),
		uniq:   make([][]segment.KeyEntry, len(c.def.Unique)),
	}
}

func (kf *keyFiles) add(r sideRow) error {
	kf.ids = append(kf.ids, segment.KeyEntry{Key: []byte(r.id), Row: r.row, V: r.v, Seq: r.seq, Tx: r.tx, Del: r.del})

	if r.del || len(kf.fields)+len(kf.unique) == 0 {
		return nil
	}

	m, err := query.Decode(r.doc)
	if err != nil {
		return nil
	}

	for i, f := range kf.fields {
		for _, k := range index.DocKeys(m, f.name, f.time) {
			kf.idx[i] = append(kf.idx[i], segment.KeyEntry{Key: k, Row: r.row})
		}
	}

	for i, f := range kf.unique {
		if key, ok := uniqueKey(r.doc, f); ok {
			kf.uniq[i] = append(kf.uniq[i], segment.KeyEntry{Key: uniqueEntry(key, r.id), Row: r.row})
		}
	}

	return nil
}

func (kf *keyFiles) write(segPath string) error {
	if err := segment.WriteKeys(kf.fsys, idsPath(segPath), kf.ids, true, true); err != nil {
		return err
	}

	for i, f := range kf.fields {
		if err := segment.WriteKeys(kf.fsys, indexPath(segPath, f.name), kf.idx[i], false, false); err != nil {
			return err
		}
	}

	for i, f := range kf.unique {
		if err := segment.WriteKeys(kf.fsys, uniquePath(segPath, f), kf.uniq[i], false, false); err != nil {
			return err
		}
	}

	return nil
}

// uniqueEntry is the key of a unique file: the value, then the id, so all
// owners of one value are one key range.
func uniqueEntry(key, id string) []byte {
	return append(append([]byte(key), 0), id...)
}

// openSidecars opens the key files of r. A missing index file leaves that
// field without an index in this segment (a scan reads it instead).
func (c *Collection) openSidecars(r *segRef, fields []indexField) error {
	fsys := c.seal.fs

	ids, err := segment.OpenKeys(fsys, idsPath(r.path))
	if err != nil {
		return err
	}

	r.ids = ids
	r.keys = map[string]*segment.Keys{}
	r.uniq = map[string]*segment.Keys{}

	for _, f := range fields {
		if err := r.attachIndex(fsys, f.name); err != nil {
			return err
		}
	}

	for _, f := range c.def.Unique {
		path := uniquePath(r.path, f)

		// A unique field added after the seal of this segment has no file
		// yet: it is made now, from the rows.
		if _, err := fsys.Stat(path); err != nil {
			if err := c.writeUniqueFile(r, f); err != nil {
				return err
			}
		}

		k, err := segment.OpenKeys(fsys, path)

		if err != nil {
			return err
		}

		r.uniq[f] = k
	}

	return nil
}

// writeUniqueFile writes the key file of the unique field f of a segment.
func (c *Collection) writeUniqueFile(r *segRef, f string) error {
	var entries []segment.KeyEntry

	err := segmentRows(r)(func(row sideRow) error {
		if key, ok := uniqueKey(row.doc, f); ok && !row.del {
			entries = append(entries, segment.KeyEntry{Key: uniqueEntry(key, row.id), Row: row.row})
		}

		return nil
	})

	if err != nil {
		return err
	}

	return segment.WriteKeys(c.seal.fs, uniquePath(r.path, f), entries, false, false)
}

// sidecarsExist tells if the key files of the segment were written (a
// segment of an older version has none).
func (c *Collection) sidecarsExist(segPath string) bool {
	_, err := c.seal.fs.Stat(idsPath(segPath))

	return err == nil
}

// indexFields are the secondary indexes a new segment gets. The caller holds
// c.mu.
func (c *Collection) indexFields() []indexField {
	out := make([]indexField, 0, len(c.rd.idx))

	for _, s := range c.rd.idx {
		out = append(out, indexField{name: s.field, time: s.time})
	}

	return out
}

// sealedGet is the newest sealed version of id, as a stub that reads its
// document from the segment. ok is false when no segment has id, or its
// newest row is a tombstone. The caller holds c.mu, or runs on the writer
// goroutine (the only one that changes the segment list).
func (c *Collection) sealedGet(id string) (*entry, bool) {
	if c.seal == nil {
		return nil, false
	}

	key := []byte(id)
	rs := c.seal.readers

	for i := len(rs) - 1; i >= 0; i-- {
		e, ok, err := rs[i].ids.Find(key)
		if err != nil {
			c.fail(err)

			return nil, false
		}

		if !ok {
			continue
		}

		if e.Del {
			return nil, false
		}

		return &entry{v: e.V, seq: e.Seq, tx: e.Tx, loc: &location{seg: rs[i], row: e.Row}}, true
	}

	return nil, false
}

// sealedOwners lists the ids of the sealed rows whose unique field holds
// key, newest segment first. Some may be old versions: the caller checks the
// current version of each.
func (c *Collection) sealedOwners(field, key string) []string {
	if c.seal == nil {
		return nil
	}

	var out []string

	lo := uniqueEntry(key, "")
	hi := append([]byte(key), 1)
	rs := c.seal.readers

	for i := len(rs) - 1; i >= 0; i-- {
		k := rs[i].uniq[field]

		if k == nil {
			continue
		}

		err := k.Range(lo, hi, true, false, func(e segment.KeyEntry) bool {
			out = append(out, string(e.Key[len(lo):]))

			return true
		})
		if err != nil {
			c.fail(err)

			return nil
		}
	}

	return out
}

// clearLive marks a sealed row as replaced. The caller holds c.mu for
// writing.
func (c *Collection) clearLive(loc *location) {
	r, ok := loc.seg.(*segRef)

	if !ok || r.live == nil || !r.live.has(loc.row) {
		return
	}

	r.live.clear(loc.row)
	r.nlive--
	r.gen++
}

// sealedLen is the number of live sealed documents. The caller holds c.mu.
func (c *Collection) sealedLen() int {
	if c.seal == nil {
		return 0
	}

	n := 0

	for _, r := range c.seal.readers {
		n += r.nlive
	}

	return n
}

// eachLive calls fn with the id and a stub of every row whose bit is set in
// the snapshot segs, segment by segment in row order.
func eachLive(segs []segSnap, fn func(id string, e *entry) error) error {
	for _, s := range segs {
		for k, g := range s.groups {
			if liveIn(s.live, g) == 0 {
				continue
			}

			rows, err := s.r.ReadGroup(s.first+k, nil)
			if err != nil {
				return err
			}

			for i := range g.Rows {
				if !isLive(s.live, g.Start+i) {
					continue
				}

				e := &entry{v: rows.Vs[i], seq: rows.Seqs[i], loc: &location{seg: s.r, row: g.Start + i}}

				if err := fn(rows.IDs[i], e); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

func isNotExist(err error) bool {
	return errors.Is(err, os.ErrNotExist)
}
