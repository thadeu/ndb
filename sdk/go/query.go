package ndb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"strconv"
)

// SortKey is one field of a sort, with its direction.
type SortKey struct {
	field string
	dir   int
}

// Asc sorts by field, lowest first.
func Asc(field string) SortKey {
	return SortKey{field: field, dir: 1}
}

// Desc sorts by field, highest first.
func Desc(field string) SortKey {
	return SortKey{field: field, dir: -1}
}

// sortOrder is the sort object of a find. The order of its keys is the order
// of the sort, so it is encoded by hand and not as a Go map.
type sortOrder []SortKey

func (s sortOrder) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer

	buf.WriteByte('{')

	for i, k := range s {
		if i > 0 {
			buf.WriteByte(',')
		}

		name, err := json.Marshal(k.field)
		if err != nil {
			return nil, err
		}

		buf.Write(name)
		buf.WriteByte(':')
		buf.WriteString(strconv.Itoa(k.dir))
	}

	buf.WriteByte('}')

	return buf.Bytes(), nil
}

// findBody is the body of POST /v1/c/{coll}/find (plan §10.1 and §10.6).
type findBody struct {
	Query  map[string]any `json:"query,omitempty"`
	Fields []string       `json:"fields,omitempty"`
	Sort   sortOrder      `json:"sort,omitempty"`
	Limit  int            `json:"limit,omitempty"`
	Skip   int            `json:"skip,omitempty"`
	After  string         `json:"after,omitempty"`
	Count  bool           `json:"count,omitempty"`

	// Versions asks the daemon for "_v" in each row, for a version field.
	Versions bool `json:"versions,omitempty"`
}

// Query builds a find on the collection of T. It builds JSON only: the
// daemon does the matching and the sorting. The methods change the query and
// return it, so a query is for one goroutine at a time.
type Query[T any] struct {
	coll    *Coll[T]
	filters []Filter
	fields  []string
	sort    sortOrder
	limit   int
	skip    int
	after   string
	next    string
}

// Query starts a search with filters (POST /{coll}/_search). The filters
// are ANDed. No filter matches every document.
func (c *Coll[T]) Query(filters ...Filter) *Query[T] {
	return &Query[T]{coll: c, filters: filters}
}

// Count is how many documents match the filters (POST /{coll}/_count).
func (c *Coll[T]) Count(ctx context.Context, filters ...Filter) (int, error) {
	return c.Query(filters...).Count(ctx)
}

// Query adds filters, ANDed with the ones already there.
func (q *Query[T]) Query(filters ...Filter) *Query[T] {
	q.filters = append(q.filters, filters...)

	return q
}

// Sort adds sort keys. The first key is the main order.
func (q *Query[T]) Sort(keys ...SortKey) *Query[T] {
	q.sort = append(q.sort, keys...)

	return q
}

// Limit sets the most rows to return. Zero means no limit.
func (q *Query[T]) Limit(n int) *Query[T] {
	q.limit = n

	return q
}

// Skip drops the first n rows. Its cost grows with n; use After for deep pages.
func (q *Query[T]) Skip(n int) *Query[T] {
	q.skip = n

	return q
}

// Page sets the offset page n (from 1) of size rows.
func (q *Query[T]) Page(n, size int) *Query[T] {
	q.skip = (n - 1) * size
	q.limit = size

	return q
}

// Fields returns only these fields. The other fields keep their zero value.
func (q *Query[T]) Fields(names ...string) *Query[T] {
	q.fields = names

	return q
}

// After continues from a cursor token that NextCursor returned for the same
// query and sort. Another query shape gets ErrCursorMismatch.
func (q *Query[T]) After(token string) *Query[T] {
	q.after = token

	return q
}

// NextCursor returns the token for the page after the last run of this
// query, or "" on the last page.
func (q *Query[T]) NextCursor() string {
	return q.next
}

// All returns the rows of one request: one page with Limit, every row
// without it. Each follows the cursor across pages.
func (q *Query[T]) All(ctx context.Context) ([]T, error) {
	out := []T{}

	for v, err := range q.rows(ctx, q.body()) {
		if err != nil {
			return nil, err
		}

		out = append(out, v)
	}

	return out, nil
}

// First returns the first row, or ErrNotFound when no row matches.
func (q *Query[T]) First(ctx context.Context) (T, error) {
	var zero T

	body := q.body()
	body.Limit = 1

	for v, err := range q.rows(ctx, body) {
		return v, err
	}

	return zero, ErrNotFound
}

// Count returns how many documents match the query (POST /{coll}/_count).
// Limit, Skip and Sort do not change it.
func (q *Query[T]) Count(ctx context.Context) (int, error) {
	body, err := json.Marshal(map[string]any{"query": queryOf(q.filters)})
	if err != nil {
		return 0, err
	}

	var out struct {
		Count int `json:"count"`
	}

	return out.Count, q.coll.call(ctx, http.MethodPost, "_count", body, &out)
}

// Each yields every row of the query. It follows the cursor page by page,
// with Limit as the batch size (no limit = one request). A row that fails to
// decode ends the iteration with that error.
func (q *Query[T]) Each(ctx context.Context) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		body := q.body()

		for {
			for row, err := range q.rows(ctx, body) {
				if !yield(row, err) || err != nil {
					return
				}
			}

			if q.next == "" {
				return
			}

			body.After = q.next
			body.Skip = 0
		}
	}
}

func (q *Query[T]) body() findBody {
	return findBody{
		Query:    queryOf(q.filters),
		Fields:   q.fields,
		Sort:     q.sort,
		Limit:    q.limit,
		Skip:     q.skip,
		After:    q.after,
		Versions: q.wantsVersions(),
	}
}

// wantsVersions is true when T has a version field to fill.
func (q *Query[T]) wantsVersions() bool {
	return q.coll != nil && q.coll.schema != nil && q.coll.schema.version != ""
}

// rows runs one find with body and yields each decoded row.
func (q *Query[T]) rows(ctx context.Context, body findBody) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T

		raw, err := json.Marshal(body)
		if err != nil {
			yield(zero, err)

			return
		}

		resp, err := q.coll.stream(ctx, "_search", raw)
		if err != nil {
			yield(zero, err)

			return
		}

		defer resp.Body.Close()

		q.next = resp.Header.Get("X-Ndb-Next")

		for row, err := range ndjsonRows(resp.Body) {
			if err != nil {
				yield(zero, err)

				return
			}

			v, err := q.coll.decode(row)

			if !yield(v, err) || err != nil {
				return
			}
		}
	}
}

// SearchResult is one page of a Search. Next is empty on the last page.
// Total is set only when the body has "count": true.
type SearchResult[T any] struct {
	Rows  []T
	Next  string
	Total int
}

// Search runs a whole search body built elsewhere, such as from a search
// form: {"query": {...}, "sort": {...}, "limit": 50, "after": "...",
// "count": true}. The body is sent as is (the daemon validates it), with
// "versions" added when T has a version field. Rows decode into T.
func (c *Coll[T]) Search(ctx context.Context, body []byte) (*SearchResult[T], error) {
	doc, err := decodeObject(body)
	if err != nil {
		return nil, fmt.Errorf("ndb: search body: %w", err)
	}

	if c.schema.version != "" {
		doc["versions"] = true
	}

	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}

	resp, err := c.stream(ctx, "_search", raw)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	res := &SearchResult[T]{Rows: []T{}, Next: resp.Header.Get("X-Ndb-Next")}

	if total := resp.Header.Get("X-Ndb-Total"); total != "" {
		if res.Total, err = strconv.Atoi(total); err != nil {
			return nil, fmt.Errorf("ndb: bad X-Ndb-Total %q", total)
		}
	}

	for row, err := range ndjsonRows(resp.Body) {
		if err != nil {
			return nil, err
		}

		v, err := c.decode(row)
		if err != nil {
			return nil, err
		}

		res.Rows = append(res.Rows, v)
	}

	return res, nil
}
