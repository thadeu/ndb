package agg

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"

	"github.com/thadeu/ndb/internal/query"
)

// Find is a pipeline with no $group, as MongoDB allows: it reads documents,
// so it runs as a find. The stages are $match, $sort, $skip, $limit and
// $project, in an order that a find can give:
//
//	$match and one $sort (in any order), then $skip and $limit, then one
//	$project (it can also come before $skip and $limit).
//
// A $match or a $sort after $skip, $limit or $project changes the result in
// MongoDB, so it is invalid_pipeline here.
type Find struct {
	Limit   int
	Skip    int
	After   string
	Count   bool
	Explain bool

	where  []json.RawMessage
	sort   json.RawMessage
	fields []string
	drop   *stage

	// The window of the $skip and $limit stages. limit < 0 is no $limit.
	skip  int
	limit int

	hash string
}

// ParseFind reads an aggregate body. ok is false when the pipeline has a
// $group (or cannot be read): then Parse handles it. Every error has the
// code invalid_pipeline.
func ParseFind(body []byte) (f *Find, ok bool, err error) {
	var top map[string]json.RawMessage

	if json.Unmarshal(body, &top) != nil {
		return nil, false, nil
	}

	var stages []json.RawMessage

	if json.Unmarshal(top["pipeline"], &stages) != nil || len(stages) == 0 {
		return nil, false, nil
	}

	for _, raw := range stages {
		var m map[string]json.RawMessage

		if json.Unmarshal(raw, &m) != nil {
			return nil, false, nil
		}

		if _, group := m["$group"]; group {
			return nil, false, nil
		}
	}

	for k := range top {
		if !slices.Contains(topKeys, k) {
			return nil, true, invalid("unknown key %q", k)
		}
	}

	f = &Find{limit: -1, hash: hashOf(top["pipeline"])}

	p := &Pipeline{}

	if err := p.parseScalars(top); err != nil {
		return nil, true, err
	}

	f.Limit, f.Skip, f.After, f.Count, f.Explain = p.Limit, p.Skip, p.After, p.Count, p.Explain

	return f, true, f.parseStages(stages)
}

func (f *Find) parseStages(stages []json.RawMessage) error {
	// A $match or a $sort cannot come after these stages.
	windowed, projected := false, false

	for _, raw := range stages {
		name, arg, err := stageOf(raw)
		if err != nil {
			return err
		}

		if (name == "$match" || name == "$sort") && (windowed || projected) {
			return invalid("%s after $skip, $limit or $project is not supported: put it first", name)
		}

		switch name {
		case "$match":
			f.where = append(f.where, arg)
		case "$sort":
			if f.sort != nil {
				return invalid("only one $sort stage is allowed")
			}

			f.sort = arg
		case "$skip", "$limit":
			windowed = true

			err = f.window(name, arg)
		case "$project":
			if projected {
				return invalid("only one $project stage is allowed")
			}

			projected = true

			err = f.project(arg)
		case "$having":
			return invalid("$having filters the groups of a $group: use $match")
		case "$unwind":
			return invalid("$unwind needs a $group stage after it")
		}

		if err != nil {
			return err
		}
	}

	return f.check()
}

// window applies one $skip or $limit to the window of the pipeline.
func (f *Find) window(name string, arg json.RawMessage) error {
	var n int

	if json.Unmarshal(arg, &n) != nil || n < 0 || (name == "$limit" && n == 0) {
		return invalid("%s must be a positive integer", name)
	}

	if name == "$skip" {
		f.skip += n

		if f.limit >= 0 {
			f.limit = max(f.limit-n, 0)
		}

		return nil
	}

	if f.limit < 0 || n < f.limit {
		f.limit = n
	}

	return nil
}

// project keeps fields through the fields of the find, or removes them
// from the rows after it.
func (f *Find) project(arg json.RawMessage) error {
	st, err := compileStage("$project", arg)
	if err != nil {
		return err
	}

	if st.drop {
		f.drop = &st

		return nil
	}

	if !st.noID {
		f.fields = append(f.fields, "_id")
	}

	f.fields = append(f.fields, st.fields...)

	return nil
}

// check parses the find of the stages, so a bad $match or $sort is
// invalid_pipeline before any read.
func (f *Find) check() error {
	raw, err := f.body(nil)
	if err != nil {
		return invalid("%v", err)
	}

	if _, err := query.Parse(raw, query.Options{}); err != nil {
		return invalid("%s", strings.TrimPrefix(err.Error(), "invalid_query: "))
	}

	return nil
}

// FindPage is one page of a Find: the find body to run, and what Finish
// needs to give the rows, the cursor and the total of the pipeline.
type FindPage struct {
	Body []byte

	f    *Find
	seen int  // rows of the window before this page
	none bool // the window has no row left: the find only counts
}

// findCursor is the next token of a Find with a $limit: the find cursor and
// the rows of the window already given.
type findCursor struct {
	After string `json:"a"`
	Seen  int    `json:"n"`
	Hash  string `json:"h"`
}

// Page makes the find body of the page that the request asks for.
func (f *Find) Page() (*FindPage, error) {
	pg := &FindPage{f: f}
	after := f.After

	if f.limit >= 0 && after != "" {
		c, err := f.decode(after)
		if err != nil {
			return nil, err
		}

		after, pg.seen = c.After, c.Seen
	}

	keys := map[string]any{"count": f.Count, "explain": f.Explain}

	if after != "" {
		keys["after"] = after
	}

	skip := f.Skip

	if f.After == "" {
		skip += f.skip
	}

	limit := f.Limit

	if f.limit >= 0 {
		pg.seen += f.Skip

		left := f.limit - pg.seen

		if left <= 0 {
			pg.none, skip, left = true, 0, 1
		}

		if limit == 0 || left < limit {
			limit = left
		}
	}

	if skip > 0 {
		keys["skip"] = skip
	}

	if limit > 0 {
		keys["limit"] = limit
	}

	raw, err := f.body(keys)
	if err != nil {
		return nil, err
	}

	pg.Body = raw

	return pg, nil
}

// body is the JSON of the find of one page.
func (f *Find) body(keys map[string]any) ([]byte, error) {
	body := map[string]any{}

	switch len(f.where) {
	case 0:
	case 1:
		body["query"] = f.where[0]
	default:
		body["query"] = map[string]any{"$and": f.where}
	}

	if f.sort != nil {
		body["sort"] = f.sort
	}

	if len(f.fields) > 0 {
		body["fields"] = f.fields
	}

	for k, v := range keys {
		body[k] = v
	}

	return json.Marshal(body)
}

func (f *Find) decode(tok string) (findCursor, error) {
	var c findCursor

	raw, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil || json.NewDecoder(bytes.NewReader(raw)).Decode(&c) != nil || c.Seen < 0 {
		return c, query.NewError("cursor_invalid", "bad cursor")
	}

	if c.Hash != f.hash {
		return c, query.NewError("cursor_mismatch", "the cursor was made for a different pipeline")
	}

	return c, nil
}

// Finish turns the answer of the find into the answer of the pipeline.
func (pg *FindPage) Finish(rows []json.RawMessage, next string, total *int) ([]json.RawMessage, string, *int, error) {
	f := pg.f

	if pg.none {
		rows, next = nil, ""
	}

	if total != nil {
		t := max(*total-f.skip, 0)

		if f.limit >= 0 {
			t = min(t, f.limit)
		}

		total = &t
	}

	if f.drop != nil {
		var err error

		if rows, err = f.dropFields(rows); err != nil {
			return nil, "", nil, err
		}
	}

	if f.limit < 0 || next == "" {
		return rows, next, total, nil
	}

	seen := pg.seen + len(rows)

	if seen >= f.limit {
		return rows, "", total, nil
	}

	b, err := json.Marshal(findCursor{After: next, Seen: seen, Hash: f.hash})
	if err != nil {
		return nil, "", nil, err
	}

	return rows, base64.RawURLEncoding.EncodeToString(b), total, nil
}

// dropFields runs a $project that removes fields on the rows of the find.
func (f *Find) dropFields(rows []json.RawMessage) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, len(rows))

	for i, r := range rows {
		m, err := query.Decode(r)
		if err != nil {
			return nil, err
		}

		if out[i], err = json.Marshal(f.drop.projectRow(m)); err != nil {
			return nil, err
		}
	}

	return out, nil
}
