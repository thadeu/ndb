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

	keys := make([]Value, len(q.Sort))

	for i, s := range q.Sort {
		keys[i] = pick(Elements(m, s.Path, q.IsTime(s.Path)), s.Desc)
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

	for i, s := range q.Sort {
		if c := Compare(a.Keys[i], b.Keys[i]); c != 0 {
			return flip(c, s.Desc)
		}
	}

	return flip(strings.Compare(a.ID, b.ID), q.Sort[len(q.Sort)-1].Desc)
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
}

// EncodeCursor makes the opaque next token: base64 JSON of the sort values,
// the id, the insertion position, the query hash and the snapshot id.
func (q *Query) EncodeCursor(p Pos, snap string) string {
	c := cursor{Keys: make([][]any, len(p.Keys)), ID: p.ID, Seq: p.Seq, Hash: q.Hash(), Snap: snap}

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
	raw, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil {
		return Pos{}, "", NewError("cursor_invalid", "bad cursor")
	}

	var c cursor

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	if err := dec.Decode(&c); err != nil {
		return Pos{}, "", NewError("cursor_invalid", "bad cursor")
	}

	if c.Hash != q.Hash() || len(c.Keys) != len(q.Sort) {
		return Pos{}, "", NewError("cursor_mismatch", "the cursor was made for a different where or sort")
	}

	p := Pos{ID: c.ID, Seq: c.Seq}

	for i, k := range c.Keys {
		if len(k) == 2 {
			p.Keys = append(p.Keys, FromJSON(k[1], q.IsTime(q.Sort[i].Path)))
		} else {
			p.Keys = append(p.Keys, Value{})
		}
	}

	return p, c.Snap, nil
}
