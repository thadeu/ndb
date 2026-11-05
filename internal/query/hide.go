package query

import "time"

// ExpiredWhere is the where clause of the rows whose time value at path is at
// or before cutoff. A time is an RFC 3339 string or a number of unix seconds.
// A missing, null or unparseable value never matches.
func ExpiredWhere(path string, cutoff time.Time) map[string]any {
	return map[string]any{"$or": []any{
		map[string]any{path: map[string]any{"$lte": cutoff.UTC().Format(time.RFC3339Nano)}},
		map[string]any{path: map[string]any{"$lte": float64(cutoff.Unix())}},
	}}
}

// Hide removes from the result every row that where matches. The condition is
// not part of the query text, so the cursor hash stays the same.
func (q *Query) Hide(where map[string]any) error {
	n, err := q.compileWhere(where)
	if err != nil {
		return err
	}

	q.where = andNode{q.where, notNode{n}}

	return nil
}
