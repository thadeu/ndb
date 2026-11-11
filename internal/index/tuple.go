package index

import (
	"bytes"
	"fmt"
	"slices"
	"strings"

	"github.com/thadeu/ndb/internal/query"
)

// A compound index has 2 to 4 fields. Its name is the fields with commas,
// "account_id,created_at"; a name with no comma is an index of one field.
//
// The key of a document is the encodings of its fields, one after the
// other. Encode is order-preserving and prefix-free, so the keys sort as the
// tuples sort, and "every tuple that starts with these values" is one range
// of bytes (see PrefixEnd).
//
// A query condition on a field matches a document when one element of an
// array matches, so a field that holds an array gives one key per element,
// as in an index of one field; two arrays give every pair. A field that is
// missing is encoded as missing (it sorts first), so a read by the first
// fields still finds the document. A document with no first field has no
// key: every use of the index binds the first field to a value.
//
// A document whose arrays give more than MaxTupleKeys pairs gets one key
// instead, the overflow key, after every tuple. Every read of the index
// also takes the overflow documents (Overflow) as candidates; the caller
// tests each candidate with the query, so the answer stays right.

const (
	// MaxFields is the most fields of one compound index.
	MaxFields = 4

	// MaxTupleKeys is the most keys one document may have in one compound
	// index (the pairs of the elements of its arrays).
	MaxTupleKeys = 1024

	tagMissing  = 0x00
	tagOverflow = 0xFF
)

// Overflow is the range of the overflow documents of a compound index.
func Overflow() Range {
	return Range{Lo: []byte{tagOverflow}, LoIncl: true}
}

// Fields are the field paths of an index name.
func Fields(name string) []string {
	return strings.Split(name, ",")
}

// Compound tells if name is the name of a compound index.
func Compound(name string) bool {
	return strings.Contains(name, ",")
}

// CheckName tells if name is a valid index name: one field, or 2 to
// MaxFields different fields. The id field is not allowed in a compound
// index (every key ends with the id already).
func CheckName(name string) error {
	fields := Fields(name)

	if len(fields) > MaxFields {
		return fmt.Errorf("index %q has more than %d fields", name, MaxFields)
	}

	for i, f := range fields {
		switch {
		case f == "":
			return fmt.Errorf("index %q has an empty field", name)
		case slices.Contains(fields[:i], f):
			return fmt.Errorf("index %q has the field %s twice", name, f)
		case len(fields) > 1 && f == "_id":
			return fmt.Errorf("index %q: _id cannot be in a compound index", name)
		}
	}

	return nil
}

// DocKeys are the keys of doc in the index name: Keys for one field, the
// tuple keys for a compound index. timeField is the time field of the
// collection ("" for none).
func DocKeys(doc map[string]any, name, timeField string) [][]byte {
	if !Compound(name) {
		return Keys(doc, name, name == timeField)
	}

	var keys [][]byte

	for i, f := range Fields(name) {
		parts := Keys(doc, f, f == timeField)

		if len(parts) == 0 {
			if i == 0 {
				return nil
			}

			parts = [][]byte{{tagMissing}}
		}

		if i == 0 {
			keys = parts

			continue
		}

		if len(keys)*len(parts) > MaxTupleKeys {
			return [][]byte{{tagOverflow}}
		}

		next := make([][]byte, 0, len(keys)*len(parts))

		for _, k := range keys {
			for _, p := range parts {
				next = append(next, append(slices.Clone(k), p...))
			}
		}

		keys = next
	}

	return keys
}

// PrefixEnd is the first key after every key that starts with prefix: the
// keys that start with prefix are those in [prefix, PrefixEnd(prefix)). It
// is nil when no key is after them.
func PrefixEnd(prefix []byte) []byte {
	end := slices.Clone(prefix)

	for i := len(end) - 1; i >= 0; i-- {
		if end[i] != 0xFF {
			end[i]++

			return end[:i+1]
		}
	}

	return nil
}

// PrefixRange is the range of the tuples that start with prefix.
func PrefixRange(prefix []byte) Range {
	return Range{Lo: prefix, LoIncl: true, Hi: PrefixEnd(prefix)}
}

// TupleCmp is the range of the tuples that start with prefix and whose next
// field passes one comparison (gt, gte, lt or lte) with v. Like Cmp, it
// stays inside the type of v.
func TupleCmp(prefix []byte, op string, v query.Value) Range {
	key := append(slices.Clone(prefix), Encode(v)...)
	tag := tags[v.Kind]
	r := Range{Lo: append(slices.Clone(prefix), tag), LoIncl: true, Hi: append(slices.Clone(prefix), tag+0x10)}

	switch op {
	case "gt":
		r.Lo = PrefixEnd(key)
	case "gte":
		r.Lo = key
	case "lt":
		r.Hi = key
	case "lte":
		r.Hi = PrefixEnd(key)
	}

	return r
}

// Intersect is the keys in both a and b. Both are ranges of tuples: the low
// end is in, the high end is out (as PrefixRange and TupleCmp give them).
func Intersect(a, b Range) Range {
	if bytes.Compare(b.Lo, a.Lo) > 0 {
		a.Lo = b.Lo
	}

	if a.Hi == nil || b.Hi != nil && bytes.Compare(b.Hi, a.Hi) < 0 {
		a.Hi = b.Hi
	}

	return a
}
