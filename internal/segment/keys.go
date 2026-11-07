package segment

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"slices"
	"sort"
	"sync"

	"github.com/thadeu/ndb/internal/fs"
)

// Key files (M16). A segment keeps its ids, and the values of each indexed
// field, in sorted files next to it, so a lookup reads the disk and not a map
// of every document. Only a sparse index (one key every keyBlock entries) and,
// for the ids, a bloom filter stay in memory: a few bits per row.
//
// File layout (little endian):
//
//	entries: per entry uvarint(len key), key, uvarint row,
//	         and with meta: uvarint v, uvarint seq, uvarint tx, byte del
//	sparse:  per block uvarint(len key), first key, uint64 offset
//	bloom:   the bits
//	footer:  magic, flags, count, maxSeq, sparse offset, bloom offset, bloom k
//	         (keysFooter bytes)

const (
	keyBlock    = 128
	keysMagic   = "NDBK"
	keysFooter  = 4 + 1 + 8*6
	bloomBits   = 10
	bloomHashes = 7
	flagMeta    = 1
)

// KeyEntry is one key of a segment and the row that holds it. A file of ids
// also keeps the metadata of the row (Meta): version, insertion position,
// transaction and tombstone flag.
type KeyEntry struct {
	Key []byte
	Row int

	V   uint64
	Seq uint64
	Tx  uint64
	Del bool
}

// WriteKeys sorts entries (by key, then row) and writes them to path,
// atomically. meta keeps the row metadata; bloom adds a bloom filter of the
// keys.
func WriteKeys(fsys fs.FS, path string, entries []KeyEntry, meta, bloom bool) error {
	slices.SortFunc(entries, func(a, b KeyEntry) int {
		if c := bytes.Compare(a.Key, b.Key); c != 0 {
			return c
		}

		return a.Row - b.Row
	})

	var (
		buf    bytes.Buffer
		sparse bytes.Buffer
		maxSeq uint64
		tmp    [binary.MaxVarintLen64]byte
	)

	put := func(w *bytes.Buffer, n uint64) {
		w.Write(tmp[:binary.PutUvarint(tmp[:], n)])
	}

	var bits []byte

	if bloom {
		bits = make([]byte, (max(len(entries), 1)*bloomBits+7)/8)
	}

	for i, e := range entries {
		if i%keyBlock == 0 {
			put(&sparse, uint64(len(e.Key)))
			sparse.Write(e.Key)
			binary.Write(&sparse, binary.LittleEndian, uint64(buf.Len()))
		}

		put(&buf, uint64(len(e.Key)))
		buf.Write(e.Key)
		put(&buf, uint64(e.Row))

		if meta {
			put(&buf, e.V)
			put(&buf, e.Seq)
			put(&buf, e.Tx)
			buf.WriteByte(boolByte(e.Del))
			maxSeq = max(maxSeq, e.Seq)
		}

		if bloom {
			bloomAdd(bits, e.Key)
		}
	}

	sparseAt := uint64(buf.Len())
	buf.Write(sparse.Bytes())

	bloomAt := uint64(buf.Len())
	buf.Write(bits)

	flags := byte(0)

	if meta {
		flags |= flagMeta
	}

	buf.WriteString(keysMagic)
	buf.WriteByte(flags)

	for _, n := range []uint64{uint64(len(entries)), maxSeq, sparseAt, bloomAt, uint64(len(bits)), bloomHashes} {
		binary.Write(&buf, binary.LittleEndian, n)
	}

	return Publish(fsys, path, buf.Bytes())
}

func boolByte(b bool) byte {
	if b {
		return 1
	}

	return 0
}

// Keys is an open key file. It is safe for concurrent use.
type Keys struct {
	f      fs.File
	meta   bool
	count  int
	maxSeq uint64
	end    int64

	firsts  [][]byte
	offsets []int64

	bloom []byte

	pool sync.Pool
}

// OpenKeys opens a key file and loads its sparse index and bloom filter.
func OpenKeys(fsys fs.FS, path string) (*Keys, error) {
	f, err := fsys.Open(path)
	if err != nil {
		return nil, err
	}

	k, err := readKeys(f)
	if err != nil {
		f.Close()

		return nil, fmt.Errorf("segment: key file %s: %w", path, err)
	}

	return k, nil
}

func readKeys(f fs.File) (*Keys, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}

	size := info.Size()

	if size < keysFooter {
		return nil, errors.New("too short")
	}

	foot := make([]byte, keysFooter)

	if _, err := f.ReadAt(foot, size-keysFooter); err != nil {
		return nil, err
	}

	if string(foot[:4]) != keysMagic {
		return nil, errors.New("bad magic")
	}

	nums := make([]uint64, 6)

	for i := range nums {
		nums[i] = binary.LittleEndian.Uint64(foot[5+8*i:])
	}

	k := &Keys{f: f, meta: foot[4]&flagMeta != 0, count: int(nums[0]), maxSeq: nums[1], end: int64(nums[2])}

	tail := make([]byte, size-keysFooter-k.end)

	if _, err := f.ReadAt(tail, k.end); err != nil {
		return nil, err
	}

	sparse := tail[:int64(nums[3])-k.end]
	k.bloom = tail[len(sparse) : len(sparse)+int(nums[4])]

	for len(sparse) > 0 {
		n, w := binary.Uvarint(sparse)
		key := sparse[w : w+int(n)]
		sparse = sparse[w+int(n):]

		k.firsts = append(k.firsts, key)
		k.offsets = append(k.offsets, int64(binary.LittleEndian.Uint64(sparse)))
		sparse = sparse[8:]
	}

	return k, nil
}

// Len is the number of entries. MaxSeq is the highest insertion position
// (files with meta only).
func (k *Keys) Len() int { return k.count }

func (k *Keys) MaxSeq() uint64 { return k.maxSeq }

func (k *Keys) Close() error { return k.f.Close() }

// MemSize is about the memory the file keeps: its sparse index and bloom.
func (k *Keys) MemSize() int {
	n := len(k.bloom) + 16*len(k.offsets)

	for _, f := range k.firsts {
		n += len(f)
	}

	return n
}

// MayHave is false when the bloom filter proves key is absent. A file
// without a bloom filter always says true.
func (k *Keys) MayHave(key []byte) bool {
	if len(k.bloom) == 0 {
		return true
	}

	return bloomHas(k.bloom, key)
}

// Find returns the first entry of key.
func (k *Keys) Find(key []byte) (KeyEntry, bool, error) {
	if !k.MayHave(key) {
		return KeyEntry{}, false, nil
	}

	var (
		out   KeyEntry
		found bool
	)

	err := k.Range(key, key, true, true, func(e KeyEntry) bool {
		out, found = e, true

		return false
	})

	return out, found, err
}

// Range calls fn for every entry with lo ≤ key ≤ hi (bounds by incl), in key
// order, until fn returns false. A nil bound is open.
func (k *Keys) Range(lo, hi []byte, loIncl, hiIncl bool, fn func(KeyEntry) bool) error {
	if k.count == 0 {
		return nil
	}

	block := 0

	if lo != nil {
		// The last block whose first key is < lo may still hold lo.
		block = max(0, sort.Search(len(k.firsts), func(i int) bool { return bytes.Compare(k.firsts[i], lo) >= 0 })-1)
	}

	for ; block < len(k.offsets); block++ {
		entries, err := k.block(block)
		if err != nil {
			return err
		}

		for _, e := range entries {
			if lo != nil {
				c := bytes.Compare(e.Key, lo)

				if c < 0 || (c == 0 && !loIncl) {
					continue
				}
			}

			if hi != nil {
				c := bytes.Compare(e.Key, hi)

				if c > 0 || (c == 0 && !hiIncl) {
					return nil
				}
			}

			if !fn(e) {
				return nil
			}
		}
	}

	return nil
}

// block reads and decodes one block of entries.
func (k *Keys) block(i int) ([]KeyEntry, error) {
	from := k.offsets[i]
	to := k.end

	if i+1 < len(k.offsets) {
		to = k.offsets[i+1]
	}

	buf, _ := k.pool.Get().(*[]byte)

	if buf == nil {
		buf = new([]byte)
	}

	defer k.pool.Put(buf)

	if cap(*buf) < int(to-from) {
		*buf = make([]byte, to-from)
	}

	b := (*buf)[:to-from]

	if _, err := k.f.ReadAt(b, from); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}

	out := make([]KeyEntry, 0, keyBlock)

	for len(b) > 0 {
		var e KeyEntry

		n, w := binary.Uvarint(b)
		e.Key = bytes.Clone(b[w : w+int(n)])
		b = b[w+int(n):]

		row, w := binary.Uvarint(b)
		e.Row = int(row)
		b = b[w:]

		if k.meta {
			e.V, w = binary.Uvarint(b)
			b = b[w:]
			e.Seq, w = binary.Uvarint(b)
			b = b[w:]
			e.Tx, w = binary.Uvarint(b)
			b = b[w:]
			e.Del = b[0] == 1
			b = b[1:]
		}

		out = append(out, e)
	}

	return out, nil
}

// Each calls fn for every entry in key order, until fn returns false.
func (k *Keys) Each(fn func(KeyEntry) bool) error {
	return k.Range(nil, nil, true, true, fn)
}

func bloomHashPair(key []byte) (uint64, uint64) {
	h := fnv.New64a()
	h.Write(key)

	a := h.Sum64()

	return a, a>>33 | a<<31 | 1
}

func bloomAdd(bits, key []byte) {
	a, b := bloomHashPair(key)
	n := uint64(len(bits) * 8)

	for i := range uint64(bloomHashes) {
		bit := (a + i*b) % n
		bits[bit/8] |= 1 << (bit % 8)
	}
}

func bloomHas(bits, key []byte) bool {
	a, b := bloomHashPair(key)
	n := uint64(len(bits) * 8)

	for i := range uint64(bloomHashes) {
		bit := (a + i*b) % n

		if bits[bit/8]&(1<<(bit%8)) == 0 {
			return false
		}
	}

	return true
}
