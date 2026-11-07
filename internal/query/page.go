package query

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// Pos is where a row sits in the result order.
type Pos struct {
	Keys []Value // one per sort key; nil with no sort
	ID   string
	Seq  uint64 // insertion position, used with no sort
}

// SortKeys are the values of the sort fields in doc.
func (q *Query) SortKeys(m map[string]any) []Value {
	if len(q.Sort) == 0 {
		return nil
	}

	elems := make([][]Value, len(q.Sort))

	for i, s := range q.Sort {
		elems[i] = Elements(m, s.Path, q.IsTime(s.Path))
	}

	return q.SortKeysOf(elems)
}

// SortKeysOf picks the sort key of each sort field from its elements, as
// SortKeys does for a document. elems is one list per sort field.
func (q *Query) SortKeysOf(elems [][]Value) []Value {
	if len(q.Sort) == 0 {
		return nil
	}

	keys := make([]Value, len(q.Sort))

	for i, s := range q.Sort {
		keys[i] = pick(elems[i], s.Desc)
	}

	return keys
}

// pick is the smallest element (largest when desc); none is Missing.
func pick(vals []Value, desc bool) Value {
	best := Value{}

	for i, v := range vals {
		if i == 0 || (Compare(v, best) < 0) != desc && Compare(v, best) != 0 {
			best = v
		}
	}

	return best
}

// Cmp orders two positions in the result order of q: negative when a is first.
func (q *Query) Cmp(a, b Pos) int {
	if len(q.Sort) == 0 {
		return cmpOrdered(int64(a.Seq), int64(b.Seq))
	}

	if c := q.CmpKeys(a, b); c != 0 {
		return c
	}

	return q.Flip(strings.Compare(a.ID, b.ID))
}

// CmpKeys orders a and b by the sort keys only: 0 when they tie, or with no
// sort.
func (q *Query) CmpKeys(a, b Pos) int {
	for i, s := range q.Sort {
		if c := Compare(a.Keys[i], b.Keys[i]); c != 0 {
			return flip(c, s.Desc)
		}
	}

	return 0
}

// Flip turns a tie-break order (id, collection) into the result order: it
// follows the direction of the last sort key.
func (q *Query) Flip(c int) int {
	if len(q.Sort) == 0 {
		return c
	}

	return flip(c, q.Sort[len(q.Sort)-1].Desc)
}

func flip(c int, desc bool) int {
	if desc {
		return -c
	}

	return c
}

// Hash binds a cursor to the where and sort of the query.
func (q *Query) Hash() string {
	var sb strings.Builder

	writeCanon(&sb, q.raw)

	for _, s := range q.Sort {
		sb.WriteString("|" + s.Path)

		if s.Desc {
			sb.WriteString("-")
		}
	}

	sum := sha256.Sum256([]byte(sb.String()))

	return hex.EncodeToString(sum[:8])
}

type cursor struct {
	Keys [][]any `json:"s"`
	ID   string  `json:"i"`
	Seq  uint64  `json:"p"`
	Hash string  `json:"h"`
	Snap string  `json:"n,omitempty"`

	// Pattern and Coll are set only by a read over a collection pattern.
	Pattern string `json:"m,omitempty"`
	Coll    string `json:"c,omitempty"`
}

// EncodeCursor makes the opaque next token: base64 JSON of the sort values,
// the id, the insertion position, the query hash and the snapshot id.
func (q *Query) EncodeCursor(p Pos, snap string) string {
	return q.encode(p, cursor{Snap: snap})
}

// EncodePatternCursor is the next token of a read over a collection pattern:
// the position of the last row and the collection it came from.
func (q *Query) EncodePatternCursor(p Pos, pattern, coll string) string {
	return q.encode(p, cursor{Pattern: pattern, Coll: coll})
}

func (q *Query) encode(p Pos, c cursor) string {
	c.Keys, c.ID, c.Seq, c.Hash = make([][]any, len(p.Keys)), p.ID, p.Seq, q.Hash()

	for i, k := range p.Keys {
		if k.Kind == Missing {
			c.Keys[i] = []any{0}
		} else {
			c.Keys[i] = []any{1, k.Raw}
		}
	}

	b, _ := json.Marshal(c)

	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeCursor reads a token made for a query with the same where and sort.
func (q *Query) DecodeCursor(tok string) (Pos, string, error) {
	p, c, err := q.decode(tok, "")
	if err != nil {
		return Pos{}, "", err
	}

	return p, c.Snap, nil
}

// DecodePatternCursor reads a token made by a read over the same pattern,
// with the same where and sort. coll is the collection of the last row.
func (q *Query) DecodePatternCursor(tok, pattern string) (Pos, string, error) {
	p, c, err := q.decode(tok, pattern)
	if err != nil {
		return Pos{}, "", err
	}

	return p, c.Coll, nil
}

func (q *Query) decode(tok, pattern string) (Pos, cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil {
		return Pos{}, cursor{}, NewError("cursor_invalid", "bad cursor")
	}

	var c cursor

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	if err := dec.Decode(&c); err != nil {
		return Pos{}, cursor{}, NewError("cursor_invalid", "bad cursor")
	}

	if c.Hash != q.Hash() || len(c.Keys) != len(q.Sort) || c.Pattern != pattern {
		return Pos{}, cursor{}, NewError("cursor_mismatch", "the cursor was made for a different where, sort or collection pattern")
	}

	p := Pos{ID: c.ID, Seq: c.Seq}

	for i, k := range c.Keys {
		if len(k) == 2 {
			p.Keys = append(p.Keys, FromJSON(k[1], q.IsTime(q.Sort[i].Path)))
		} else {
			p.Keys = append(p.Keys, Value{})
		}
	}

	return p, c, nil
}
