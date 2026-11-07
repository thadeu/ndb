package collection

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"github.com/thadeu/ndb/internal/agg"
	"github.com/thadeu/ndb/internal/query"
)

// Reads over a collection pattern (logs-2026-*). Each collection runs its
// own plan with its own indexes; the rows are merged here.
//
// Order: the sort keys, then the collection name, then _id (both in the
// direction of the last sort key). With no sort: collection name, then
// insertion order. The order is total, so one position (last row and its
// collection) is a full cursor: each collection derives its own start from
// it (startIn).

// collField is the name of the collection of a row read through a pattern.
// It replaces a field of the same name in the document.
const collField = "_coll"

// Named is one collection of a pattern read.
type Named struct {
	Name string
	C    *Collection
}

// patternRow is a row and the collection it came from.
type patternRow struct {
	row
	coll string
}

// FindMany runs a find over cols, sorted by name. pattern binds the cursor.
func FindMany(ctx context.Context, pattern string, cols []Named, body []byte) (Page, error) {
	qs, err := parseEach(cols, body)
	if err != nil {
		return Page{}, err
	}

	q := qs[0]

	if q.Snapshot {
		return Page{}, query.NewError("invalid_query", "snapshot is not supported on a collection pattern")
	}

	var (
		after   query.Pos
		afterIn string
	)

	if q.After != "" {
		if after, afterIn, err = q.DecodePatternCursor(q.After, pattern); err != nil {
			return Page{}, err
		}
	}

	var (
		rows  []patternRow
		total int
		ex    Explain
	)

	for i, n := range cols {
		cur, hasCur, done := startIn(q, n.Name, after, afterIn)
		if done && !q.Count {
			continue
		}

		part, err := n.C.queryRows(ctx, qs[i], cur, hasCur)
		if err != nil {
			return Page{}, err
		}

		if !done {
			for _, r := range part.rows {
				rows = append(rows, patternRow{row: r, coll: n.Name})
			}
		}

		total += part.total
		ex.add(part.ex)
	}

	slices.SortFunc(rows, func(a, b patternRow) int { return cmpAcross(q, a, b) })

	return pageAcross(q, pattern, rows, total, ex), nil
}

// AggregateMany runs a pipeline over cols: one group table per collection,
// merged, then the stages after the group stage.
func AggregateMany(ctx context.Context, cols []Named, body []byte) (Page, error) {
	ps := make([]*agg.Pipeline, len(cols))

	for i, n := range cols {
		p, err := n.C.ParseAggregate(body)
		if err != nil {
			return Page{}, err
		}

		ps[i] = p
	}

	p := ps[0]

	if err := p.CheckCursor(); err != nil {
		return Page{}, err
	}

	budget := cols[0].C.budget()

	var all aggPart

	for i, n := range cols {
		part, _, err := n.C.aggTable(ctx, ps[i], budget)
		if err != nil {
			return Page{}, err
		}

		all = joinAgg(all, part)
	}

	return finishAgg(p, all, "pattern")
}

func joinAgg(all, part aggPart) aggPart {
	if all.tbl == nil {
		return part
	}

	_ = all.tbl.Merge(part.tbl)

	all.matched += part.matched
	all.ex.add(part.ex)

	return all
}

func parseEach(cols []Named, body []byte) ([]*query.Query, error) {
	qs := make([]*query.Query, len(cols))

	for i, n := range cols {
		q, err := n.C.ParseQuery(body)
		if err != nil {
			return nil, err
		}

		qs[i] = q
	}

	return qs, nil
}

// Reader is the collection that answers the reads: c itself (a Direct
// collection has an inner one).
func (c *Collection) Reader() *Collection {
	return c
}

// queryRows is the top-k rows of q after cur in this collection, with the
// total number of matches.
func (c *Collection) queryRows(ctx context.Context, q *query.Query, cur query.Pos, hasCur bool) (part, error) {
	src := c.source(q)

	defer src.release()

	parts, err := c.execute(ctx, q, src, cur, hasCur)
	if err != nil {
		return part{}, err
	}

	rows, total, ex := merge(q, parts)

	return part{rows: rows, total: total, ex: ex}, nil
}

// idLast sorts after every _id; "" sorts before every one.
var idLast = string([]byte{0xff})

// startIn is where the rows of coll start, after the last row (after, in
// collection afterIn) of the previous page. done: coll has no rows left.
func startIn(q *query.Query, coll string, after query.Pos, afterIn string) (query.Pos, bool, bool) {
	if afterIn == "" {
		return query.Pos{}, false, false
	}

	c := q.Flip(strings.Compare(coll, afterIn))

	if len(q.Sort) == 0 {
		return after, c == 0, c < 0
	}

	switch {
	case c == 0:
		return after, true, false
	case c < 0:
		// coll sorts before afterIn: a tie on the keys was already read.
		return query.Pos{Keys: after.Keys, ID: lastID(q)}, true, false
	default:
		// coll sorts after afterIn: a tie on the keys is still to read.
		return query.Pos{Keys: after.Keys, ID: firstID(q)}, true, false
	}
}

// lastID is the _id that sorts after every _id in the result order of q.
func lastID(q *query.Query) string {
	if q.Flip(1) > 0 {
		return idLast
	}

	return ""
}

func firstID(q *query.Query) string {
	if q.Flip(1) > 0 {
		return ""
	}

	return idLast
}

// cmpAcross orders rows of many collections; see the notes above.
func cmpAcross(q *query.Query, a, b patternRow) int {
	if len(q.Sort) == 0 {
		return cmp.Or(strings.Compare(a.coll, b.coll), cmp.Compare(a.Seq, b.Seq))
	}

	return cmp.Or(
		q.CmpKeys(a.Pos, b.Pos),
		q.Flip(strings.Compare(a.coll, b.coll)),
		q.Flip(strings.Compare(a.ID, b.ID)),
	)
}

// pageAcross applies skip and limit to the sorted rows and adds _coll to
// each row.
func pageAcross(q *query.Query, pattern string, rows []patternRow, total int, ex Explain) Page {
	rows = rows[min(q.Skip, len(rows)):]

	more := q.Limit > 0 && len(rows) > q.Limit

	if more {
		rows = rows[:q.Limit]
	}

	page := Page{Rows: make([]json.RawMessage, len(rows)), Plan: "pattern", Examined: ex.RowsRead}

	for i, r := range rows {
		doc := project(q.Fields, r.doc)

		if q.Versions {
			doc = withVersion(doc, r.v)
		}

		page.Rows[i] = prependField(withoutField(doc, collField), collField, strconv.AppendQuote(nil, r.coll))
	}

	if q.Count {
		page.Total = &total
	}

	if more {
		last := rows[len(rows)-1]
		page.Next = q.EncodePatternCursor(last.Pos, pattern, last.coll)
	}

	if q.Explain {
		ex.Plan, ex.RowsMatched = "pattern", total
		page.Explain = &ex
	}

	return page
}

// withoutField removes a top-level key from doc. Only a doc that has the key
// is decoded.
func withoutField(doc json.RawMessage, key string) json.RawMessage {
	if !bytes.Contains(doc, []byte(strconv.Quote(key))) {
		return doc
	}

	var m map[string]json.RawMessage

	if json.Unmarshal(doc, &m) != nil {
		return doc
	}

	if _, ok := m[key]; !ok {
		return doc
	}

	delete(m, key)

	out, err := json.Marshal(m)
	if err != nil {
		return doc
	}

	return out
}
