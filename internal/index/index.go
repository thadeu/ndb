// Package index has the order-preserving key encoding and the in-memory
// secondary index (a B-tree of key + id). Segment indexes (M04) implement
// the same Index interface.
package index

import (
	"bytes"
	"encoding/binary"
	"math"
	"strings"

	"github.com/google/btree"

	"github.com/thadeu/ndb/internal/query"
)

// Index answers range lookups with document ids.
type Index interface {
	// Ascend calls fn with each id in key order until fn returns false.
	Ascend(r Range, fn func(id string) bool)
	Len() int
}

// Range is a span of encoded keys. A nil bound is open.
type Range struct {
	Lo, Hi         []byte
	LoIncl, HiIncl bool
}

const (
	tagNull   = 0x10
	tagNumber = 0x20
	tagString = 0x30
	tagTime   = 0x40
	tagObject = 0x50
	tagArray  = 0x60
	tagBool   = 0x70
)

var tags = map[query.Kind]byte{
	query.Null: tagNull, query.Number: tagNumber, query.String: tagString, query.Time: tagTime,
	query.Object: tagObject, query.Array: tagArray, query.Bool: tagBool,
}

// Encode returns bytes that sort like query.Compare. The encoding is
// prefix-free, so key + id never mixes with a longer key.
//
//	number: tag + 8 bytes (float64 bits, flipped so bytes sort as numbers)
//	time:   tag + 8 bytes (unix nanoseconds, sign bit flipped)
//	string, object, array: tag + bytes with 0x00 → 0x00 0xFF, then 0x00 0x01
func Encode(v query.Value) []byte {
	tag := tags[v.Kind]

	switch v.Kind {
	case query.Number:
		bits := math.Float64bits(v.Num + 0)

		if bits>>63 == 1 {
			bits = ^bits
		} else {
			bits |= 1 << 63
		}

		return binary.BigEndian.AppendUint64([]byte{tag}, bits)
	case query.Time:
		return binary.BigEndian.AppendUint64([]byte{tag}, uint64(v.Int)^(1<<63))
	case query.String, query.Object, query.Array:
		out := append(make([]byte, 0, len(v.Str)+3), tag)

		for i := 0; i < len(v.Str); i++ {
			out = append(out, v.Str[i])

			if v.Str[i] == 0 {
				out = append(out, 0xFF)
			}
		}

		return append(out, 0, 1)
	case query.Bool:
		if v.Bool {
			return []byte{tag, 1}
		}

		return []byte{tag, 0}
	}

	return []byte{tag}
}

// Keys are the distinct encoded keys of a document field: one per array
// element, none when the field is missing.
func Keys(doc map[string]any, path string, isTime bool) [][]byte {
	var out [][]byte

	for _, v := range query.Elements(doc, path, isTime) {
		k := Encode(v)

		if !containsKey(out, k) {
			out = append(out, k)
		}
	}

	return out
}

func containsKey(keys [][]byte, k []byte) bool {
	for _, o := range keys {
		if bytes.Equal(o, k) {
			return true
		}
	}

	return false
}

// Eq is the range of one value.
func Eq(v query.Value) Range {
	k := Encode(v)

	return Range{Lo: k, Hi: k, LoIncl: true, HiIncl: true}
}

// Cmp is the range of $gt, $gte, $lt or $lte. Comparisons stay inside the
// type of v, like the evaluator does.
func Cmp(op string, v query.Value) Range {
	k := Encode(v)
	tag := tags[v.Kind]
	r := Range{Lo: []byte{tag}, LoIncl: true, Hi: []byte{tag + 0x10}}

	switch op {
	case "gt":
		r.Lo, r.LoIncl = k, false
	case "gte":
		r.Lo = k
	case "lt":
		r.Hi = k
	case "lte":
		r.Hi, r.HiIncl = k, true
	}

	return r
}

type item struct {
	key []byte
	id  string
}

func less(a, b item) bool {
	if c := bytes.Compare(a.key, b.key); c != 0 {
		return c < 0
	}

	return strings.Compare(a.id, b.id) < 0
}

// Mem is the memtable index. It is not safe for concurrent use: the
// collection locks around it.
type Mem struct {
	t *btree.BTreeG[item]
}

func NewMem() *Mem {
	return &Mem{t: btree.NewG(32, less)}
}

func (m *Mem) Add(key []byte, id string) {
	m.t.ReplaceOrInsert(item{key, id})
}

func (m *Mem) Remove(key []byte, id string) {
	m.t.Delete(item{key, id})
}

func (m *Mem) Len() int {
	return m.t.Len()
}

func (m *Mem) Ascend(r Range, fn func(id string) bool) {
	m.t.AscendGreaterOrEqual(item{key: r.Lo}, func(it item) bool {
		if !r.LoIncl && bytes.Equal(it.key, r.Lo) {
			return true
		}

		if r.Hi != nil {
			c := bytes.Compare(it.key, r.Hi)

			if c > 0 || c == 0 && !r.HiIncl {
				return false
			}
		}

		return fn(it.id)
	})
}
