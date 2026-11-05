package agg

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"

	"github.com/thadeu/ndb/internal/query"
)

// stage is a stage after the group. It works on the output rows.
type stage struct {
	name   string
	q      *query.Query // $match, $sort
	n      int          // $limit, $skip
	fields []string     // $project
	drop   bool         // $project: the fields are removed, not kept
	noID   bool         // $project: _id is listed with 0
}

func compileStage(name string, arg json.RawMessage) (stage, error) {
	st := stage{name: name}

	switch name {
	case "$match":
		return st, st.parseQuery("where", arg)
	case "$sort":
		return st, st.parseQuery("sort", arg)
	case "$limit", "$skip":
		if err := json.Unmarshal(arg, &st.n); err != nil || st.n < 0 || (name == "$limit" && st.n == 0) {
			return st, invalid("%s must be a positive integer", name)
		}

		return st, nil
	}

	return st, st.parseProject(arg)
}

func (st *stage) parseQuery(key string, arg json.RawMessage) error {
	body, _ := json.Marshal(map[string]json.RawMessage{key: arg})

	q, err := query.Parse(body, query.Options{})
	if err != nil {
		return invalid("%s: %s", st.name, strings.TrimPrefix(err.Error(), "invalid_query: "))
	}

	st.q = q

	return nil
}

func (st *stage) parseProject(arg json.RawMessage) error {
	pairs, err := orderedPairs(arg)
	if err != nil || len(pairs) == 0 {
		return invalid("$project must be a non-empty object of field: 1 or 0")
	}

	var kept, removed []string

	for _, pr := range pairs {
		var on bool

		if !decodeFlag(pr.value, &on) || slices.Contains(strings.Split(pr.name, "."), "") {
			return invalid("$project: %q must be 1 or 0", pr.name)
		}

		if on {
			kept = append(kept, pr.name)
		} else {
			removed = append(removed, pr.name)
		}
	}

	if len(kept) > 0 && len(removed) > 0 && (len(removed) > 1 || removed[0] != "_id") {
		return invalid("$project cannot mix kept and removed fields")
	}

	st.drop = len(kept) == 0
	st.fields = kept

	if st.drop {
		st.fields = removed
	}

	st.noID = slices.Contains(removed, "_id")

	return nil
}

func decodeFlag(raw json.RawMessage, on *bool) bool {
	var n int

	if json.Unmarshal(raw, &n) == nil && (n == 0 || n == 1) {
		*on = n == 1

		return true
	}

	return json.Unmarshal(raw, on) == nil
}

func (st stage) apply(rows []map[string]any) []map[string]any {
	switch st.name {
	case "$match":
		return slices.DeleteFunc(slices.Clone(rows), func(r map[string]any) bool { return !st.q.Match(r) })
	case "$sort":
		return st.sorted(rows)
	case "$limit":
		return rows[:min(st.n, len(rows))]
	case "$skip":
		return rows[min(st.n, len(rows)):]
	}

	return st.project(rows)
}

func (st stage) sorted(rows []map[string]any) []map[string]any {
	type keyed struct {
		pos query.Pos
		row map[string]any
	}

	list := make([]keyed, len(rows))

	for i, r := range rows {
		list[i] = keyed{pos: query.Pos{Keys: st.q.SortKeys(r)}, row: r}
	}

	slices.SortStableFunc(list, func(a, b keyed) int { return st.q.Cmp(a.pos, b.pos) })

	out := make([]map[string]any, len(list))

	for i, k := range list {
		out[i] = k.row
	}

	return out
}

// project keeps (or drops) the listed paths. _id stays unless it is listed
// with 0.
func (st stage) project(rows []map[string]any) []map[string]any {
	out := make([]map[string]any, len(rows))

	for i, r := range rows {
		out[i] = st.projectRow(r)
	}

	return out
}

func (st stage) projectRow(r map[string]any) map[string]any {
	if st.drop {
		cp := r

		for _, f := range st.fields {
			cp = dropPath(cp, strings.Split(f, "."))
		}

		return cp
	}

	out := map[string]any{}

	if !st.noID {
		out["_id"] = r["_id"]
	}

	for _, f := range st.fields {
		copyPath(out, r, strings.Split(f, "."))
	}

	return out
}

func copyPath(dst, src map[string]any, parts []string) {
	v, ok := src[parts[0]]
	if !ok {
		return
	}

	if len(parts) == 1 {
		dst[parts[0]] = v

		return
	}

	sub, ok := v.(map[string]any)
	if !ok {
		return
	}

	child, _ := dst[parts[0]].(map[string]any)

	if child == nil {
		child = map[string]any{}
		dst[parts[0]] = child
	}

	copyPath(child, sub, parts[1:])
}

// dropPath returns m without the path. It copies the maps on the way, so the
// rows of the table stay as they were.
func dropPath(m map[string]any, parts []string) map[string]any {
	if _, ok := m[parts[0]]; !ok {
		return m
	}

	out := make(map[string]any, len(m))

	for k, v := range m {
		out[k] = v
	}

	if len(parts) == 1 {
		delete(out, parts[0])

		return out
	}

	if sub, ok := m[parts[0]].(map[string]any); ok {
		out[parts[0]] = dropPath(sub, parts[1:])
	}

	return out
}

// Result is the final page of an aggregate.
type Result struct {
	Rows []json.RawMessage

	// Next is the cursor for the following page; empty on the last page.
	Next string

	// Total is the number of rows of the last stage, before limit and skip.
	Total int
}

type cursor struct {
	Hash string `json:"h"`
	N    int    `json:"n"`
}

// CheckCursor validates After, so a bad token fails before the pipeline runs.
func (p *Pipeline) CheckCursor() error {
	p.offset = 0

	if p.After == "" {
		return nil
	}

	raw, err := base64.RawURLEncoding.DecodeString(p.After)
	if err != nil {
		return query.NewError("cursor_invalid", "bad cursor")
	}

	var c cursor

	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&c); err != nil || c.N < 0 {
		return query.NewError("cursor_invalid", "bad cursor")
	}

	if c.Hash != p.hash {
		return query.NewError("cursor_mismatch", "the cursor was made for a different pipeline")
	}

	p.offset = c.N

	return nil
}

// Finish runs the stages after the group on the rows of the group table and
// cuts the page (skip, limit, after).
func (p *Pipeline) Finish(rows []map[string]any) (Result, error) {
	if err := p.CheckCursor(); err != nil {
		return Result{}, err
	}

	for _, st := range p.post {
		rows = st.apply(rows)
	}

	res := Result{Total: len(rows)}
	start := min(p.offset+p.Skip, len(rows))
	end := len(rows)

	if p.Limit > 0 && start+p.Limit < end {
		end = start + p.Limit
		b, _ := json.Marshal(cursor{Hash: p.hash, N: end})
		res.Next = base64.RawURLEncoding.EncodeToString(b)
	}

	res.Rows = make([]json.RawMessage, 0, end-start)

	for _, r := range rows[start:end] {
		b, err := json.Marshal(r)
		if err != nil {
			return Result{}, err
		}

		res.Rows = append(res.Rows, b)
	}

	return res, nil
}
