package query

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"time"
)

type Options struct {
	// TimeField is the collection time field: RFC 3339 strings compare as time.
	TimeField string

	// ExpiresField is the TTL field of the collection. Like the time field,
	// its RFC 3339 strings compare as time.
	ExpiresField string

	// Now is the clock of date math (now-7d); nil is time.Now.
	Now func() time.Time

	// Field, when set, sees each field condition before it is compiled and
	// may change its path and its value, or refuse it. Encrypted fields use
	// it: a condition on a blind field becomes one on its hash.
	Field func(path string, v any) (string, any, error)
}

type SortKey struct {
	Path string
	Desc bool
}

type Query struct {
	Fields   []string
	Sort     []SortKey
	Limit    int
	Skip     int
	After    string
	Count    bool
	Snapshot bool
	Explain  bool

	// Versions adds "_v" (the document version) to each output row, so a
	// client can send it back in the filter of an update.
	Versions bool

	// Per keeps the first Per.Limit rows (in sort order) of each value of
	// Per.Field, instead of a limit over all rows: the has_many of a lookup
	// reads every row of a page in one search.
	Per *Per

	opts  Options
	where Node
	raw   any
	paths []string

	// now is the instant of every date math of the query, read once. A
	// cursor keeps it, so the pages of one walk see the same window.
	now time.Time

	// parent is the query of an $elemMatch filter: it owns the clock.
	parent *Query

	// needsDoc: a node needs the decoded document ($elemMatch), so the
	// column values alone cannot answer.
	needsDoc bool

	// user is the where of the request, before Hide added the rows a read
	// cannot see (nil when none was added), and userPaths the number of
	// its paths (the paths of Hide come after).
	user      Node
	userPaths int
}

// Per is the "per" key of a search: {"field": "customer", "limit": 5}.
type Per struct {
	Field string `json:"field"`
	Limit int    `json:"limit"`
}

// Cond is one index-friendly condition of the where clause (top level,
// AND-ed). Op is eq, in, gt, gte or lt, lte. Every value is a scalar.
type Cond struct {
	Path string
	Op   string
	Vals []Value
}

var topKeys = []string{"query", "fields", "sort", "limit", "skip", "after", "count", "snapshot", "explain", "versions", "per"}

// Parse validates a find body. An empty body is an empty query.
func Parse(body []byte, opts Options) (*Query, error) {
	q := &Query{opts: opts, where: andNode{}}

	if len(bytes.TrimSpace(body)) == 0 {
		return q, nil
	}

	var top map[string]json.RawMessage

	if err := json.Unmarshal(body, &top); err != nil {
		return nil, invalid("body is not a JSON object: %v", err)
	}

	for k := range top {
		if !slices.Contains(topKeys, k) {
			return nil, invalid("unknown key %q", k)
		}
	}

	q.now = cursorNow(top["after"])

	if err := q.parseWhere(top["query"]); err != nil {
		return nil, err
	}

	if err := q.parseSort(top["sort"]); err != nil {
		return nil, err
	}

	return q, q.parseScalars(top)
}

// ParseWhere compiles a where map that is already decoded (numbers as
// json.Number). The update and delete filters use it.
func ParseWhere(where map[string]any, opts Options) (*Query, error) {
	q := &Query{opts: opts}

	n, err := q.compileWhere(where)
	if err != nil {
		return nil, err
	}

	q.where = n

	return q, nil
}

func (q *Query) parseScalars(top map[string]json.RawMessage) error {
	for key, dst := range map[string]*int{"limit": &q.Limit, "skip": &q.Skip} {
		raw, ok := top[key]
		if !ok {
			continue
		}

		if err := json.Unmarshal(raw, dst); err != nil || *dst < 0 {
			return invalid("%s must be a non-negative integer", key)
		}
	}

	for key, dst := range map[string]*bool{"count": &q.Count, "snapshot": &q.Snapshot, "explain": &q.Explain, "versions": &q.Versions} {
		if raw, ok := top[key]; ok && json.Unmarshal(raw, dst) != nil {
			return invalid("%s must be a boolean", key)
		}
	}

	if raw, ok := top["after"]; ok && json.Unmarshal(raw, &q.After) != nil {
		return invalid("after must be a string")
	}

	if raw, ok := top["per"]; ok {
		if err := q.parsePer(raw); err != nil {
			return err
		}
	}

	if raw, ok := top["fields"]; ok {
		if json.Unmarshal(raw, &q.Fields) != nil {
			return invalid("fields must be an array of strings")
		}

		for _, f := range q.Fields {
			if err := checkPath(f); err != nil {
				return err
			}
		}
	}

	return nil
}

// parsePer reads per. It gives every row of the page, so it does not go
// with limit, skip, after or snapshot.
func (q *Query) parsePer(raw json.RawMessage) error {
	var p Per

	if json.Unmarshal(raw, &p) != nil || p.Field == "" || p.Limit < 1 {
		return invalid(`per must be {"field": "<path>", "limit": <n ≥ 1>}`)
	}

	if err := checkPath(p.Field); err != nil {
		return err
	}

	if q.Limit > 0 || q.Skip > 0 || q.After != "" || q.Snapshot {
		return invalid("per does not go with limit, skip, after or snapshot: it gives the first per.limit rows of each value")
	}

	q.Per = &p

	return nil
}

func (q *Query) parseWhere(raw json.RawMessage) error {
	if raw == nil {
		return nil
	}

	var v any

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	if err := dec.Decode(&v); err != nil {
		return invalid("query: %v", err)
	}

	m, ok := v.(map[string]any)
	if !ok {
		return invalid("query must be an object")
	}

	node, err := q.compileWhere(m)
	if err != nil {
		return err
	}

	q.where = node
	q.raw = v

	return nil
}

// parseSort reads the sort object in key order: the order of the keys is
// the order of the sort.
func (q *Query) parseSort(raw json.RawMessage) error {
	if raw == nil {
		return nil
	}

	dec := json.NewDecoder(bytes.NewReader(raw))

	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return invalid("sort must be an object")
	}

	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return invalid("sort: %v", err)
		}

		path := tok.(string)

		if err := checkPath(path); err != nil {
			return err
		}

		var dir int

		if err := dec.Decode(&dir); err != nil || (dir != 1 && dir != -1) {
			return invalid("sort %q must be 1 or -1", path)
		}

		q.Sort = append(q.Sort, SortKey{Path: path, Desc: dir == -1})
	}

	return nil
}

func checkPath(p string) error {
	if p == "" || slices.Contains(strings.Split(p, "."), "") {
		return invalid("bad field path %q", p)
	}

	return nil
}

func splitPath(p string) []string {
	return strings.Split(p, ".")
}

func (q *Query) compileWhere(m map[string]any) (Node, error) {
	keys := sortedKeys(m)
	out := make(andNode, 0, len(keys))

	for _, k := range keys {
		var (
			n   Node
			err error
		)

		switch {
		case k == "$and" || k == "$or" || k == "$nor":
			n, err = q.compileList(k, m[k])
		case k == "$not":
			n, err = q.compileNot(m[k])
		case strings.HasPrefix(k, "$"):
			err = invalid("unknown operator %s", k)
		default:
			n, err = q.compileField(k, m[k])
		}

		if err != nil {
			return nil, err
		}

		out = append(out, n)
	}

	return out, nil
}

func (q *Query) compileList(name string, v any) (Node, error) {
	list, ok := v.([]any)
	if !ok || len(list) == 0 {
		return nil, invalid("%s must be a non-empty array of objects", name)
	}

	nodes := make([]Node, 0, len(list))

	for _, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			return nil, invalid("%s must be a non-empty array of objects", name)
		}

		n, err := q.compileWhere(m)
		if err != nil {
			return nil, err
		}

		nodes = append(nodes, n)
	}

	switch name {
	case "$or":
		return orNode(nodes), nil
	case "$nor":
		return notNode{orNode(nodes)}, nil
	}

	return andNode(nodes), nil
}

func (q *Query) compileNot(v any) (Node, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, invalid("$not must be an object")
	}

	n, err := q.compileWhere(m)
	if err != nil {
		return nil, err
	}

	return notNode{n}, nil
}

func (q *Query) compileField(path string, v any) (Node, error) {
	if hook := q.root().opts.Field; hook != nil {
		var err error

		if path, v, err = hook(path, v); err != nil {
			return nil, err
		}
	}

	if err := checkPath(path); err != nil {
		return nil, err
	}

	f := &fieldNode{path: path, parts: splitPath(path), isTime: q.IsTime(path), slot: q.slot(path)}

	m, isObj := v.(map[string]any)

	if isObj && operatorKeys(m) > 0 && operatorKeys(m) < len(m) {
		return nil, invalid("%s mixes operators and plain keys", path)
	}

	if _, ok := m["$elemMatch"]; ok {
		return q.compileElemMatch(path, m)
	}

	if !isObj || operatorKeys(m) == 0 {
		val, err := q.value(v, f.isTime)

		f.ops = []op{{name: "eq", vals: []Value{val}}}

		f.textID()

		return f, err
	}

	ops, err := q.compileOps(m, f.isTime)
	if err != nil {
		return nil, err
	}

	f.ops = ops

	f.textID()

	return f, nil
}

// operatorKeys counts the "$" keys of m: all of them is an operator
// object ({"$gt": 1}), none is an object literal, some is a mistake.
func operatorKeys(m map[string]any) int {
	n := 0

	for k := range m {
		if strings.HasPrefix(k, "$") {
			n++
		}
	}

	return n
}

func (q *Query) compileOps(m map[string]any, isTime bool) ([]op, error) {
	ops := make([]op, 0, len(m))

	if _, ok := m["$options"]; ok {
		if _, ok := m["$regex"]; !ok {
			return nil, invalid("$options needs $regex")
		}
	}

	for _, k := range sortedKeys(m) {
		if k == "$options" {
			continue
		}

		if k == "$regex" {
			o, err := compileRegexOp(m)
			if err != nil {
				return nil, err
			}

			ops = append(ops, o)

			continue
		}

		o, err := q.compileOp(k, m[k], isTime)
		if err != nil {
			return nil, err
		}

		ops = append(ops, o)
	}

	return ops, nil
}

func (q *Query) compileOp(key string, v any, isTime bool) (op, error) {
	name := strings.TrimPrefix(key, "$")

	switch name {
	case "eq", "ne":
		val, err := q.value(v, isTime)

		return op{name: name, vals: []Value{val}}, err
	case "gt", "gte", "lt", "lte":
		val, err := q.value(v, isTime)
		if err != nil {
			return op{}, err
		}

		if !val.Scalar() {
			return op{}, invalid("%s needs a scalar operand", key)
		}

		return op{name: name, vals: []Value{val}}, nil
	case "in", "nin":
		list, ok := v.([]any)
		if !ok {
			return op{}, invalid("%s needs an array", key)
		}

		vals := make([]Value, len(list))

		for i, e := range list {
			val, err := q.value(e, isTime)
			if err != nil {
				return op{}, err
			}

			vals[i] = val
		}

		return op{name: name, vals: vals}, nil
	case "exists":
		b, ok := v.(bool)
		if !ok {
			return op{}, invalid("$exists needs a boolean")
		}

		return op{name: name, flag: b}, nil
	case "not":
		m, ok := v.(map[string]any)
		if !ok || len(m) == 0 || operatorKeys(m) != len(m) {
			return op{}, invalid("$not needs an operator object")
		}

		sub, err := q.compileOps(m, isTime)

		return op{name: name, sub: sub}, err
	case "elemMatch":
		return op{}, invalid("$elemMatch must be the only operator of its field")
	}

	return op{}, invalid("unknown operator %s", key)
}

// value is the Value of an operand. On the time field, a string that starts
// with "now" is date math, resolved with the clock of the query.
func (q *Query) value(v any, isTime bool) (Value, error) {
	s, ok := v.(string)

	if !ok || !isTime || !isDateMath(s) {
		return FromJSON(v, isTime), nil
	}

	t, err := resolveDateMath(s, q.clock())
	if err != nil {
		return Value{}, err
	}

	return timeValue(t), nil
}

// clock is the instant of date math: read once per query.
func (q *Query) clock() time.Time {
	if q.parent != nil {
		return q.parent.clock()
	}

	if q.now.IsZero() {
		q.now = time.Now()

		if q.opts.Now != nil {
			q.now = q.opts.Now()
		}
	}

	return q.now
}

// compileElemMatch compiles {"$elemMatch": {...}}: one element of the array
// at path must match the whole inner filter. Field keys (and $and, $or,
// $nor) make an object filter; operators only ($gt, $in...) make a filter on
// the element itself.
func (q *Query) compileElemMatch(path string, m map[string]any) (Node, error) {
	if len(m) != 1 {
		return nil, invalid("$elemMatch must be the only operator of %s", path)
	}

	inner, ok := m["$elemMatch"].(map[string]any)
	if !ok || len(inner) == 0 {
		return nil, invalid("$elemMatch needs a non-empty object")
	}

	q.root().needsDoc = true

	n := &elemNode{parts: splitPath(path), isTime: q.IsTime(path)}

	if operatorKeys(inner) == len(inner) && !hasLogical(inner) {
		ops, err := q.compileOps(inner, n.isTime)
		n.ops = ops

		return n, err
	}

	sub := &Query{opts: Options{Now: q.opts.Now}, parent: q}

	obj, err := sub.compileWhere(inner)
	n.obj = obj

	return n, err
}

func hasLogical(m map[string]any) bool {
	for _, k := range []string{"$and", "$or", "$nor"} {
		if _, ok := m[k]; ok {
			return true
		}
	}

	return false
}

func (q *Query) root() *Query {
	if q.parent != nil {
		return q.parent.root()
	}

	return q
}

// NeedsDoc tells if the where needs the decoded document: the column
// values of its paths cannot answer it.
func (q *Query) NeedsDoc() bool {
	return q.needsDoc
}

func compileRegexOp(m map[string]any) (op, error) {
	pattern, ok := m["$regex"].(string)
	if !ok {
		return op{}, invalid("$regex needs a string pattern")
	}

	options := ""

	if v, has := m["$options"]; has {
		if options, ok = v.(string); !ok {
			return op{}, invalid("$options needs a string")
		}
	}

	re, prefix, err := compileRegex(pattern, options)
	if err != nil {
		return op{}, err
	}

	return op{name: "regex", re: re, prefix: prefix}, nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))

	for k := range m {
		keys = append(keys, k)
	}

	slices.Sort(keys)

	return keys
}

// slot numbers the where paths in order of first use: the index of a path
// in the values that MatchCols takes.
func (q *Query) slot(path string) int {
	if i := slices.Index(q.paths, path); i >= 0 {
		return i
	}

	q.paths = append(q.paths, path)

	return len(q.paths) - 1
}

// Paths lists the distinct paths of where. MatchCols takes one value list
// per path, in this order.
// HasWhere tells if the query text has conditions (Hide adds none).
func (q *Query) HasWhere() bool {
	m, ok := q.raw.(map[string]any)

	return ok && len(m) > 0
}

// Exact tells whether the only condition of the request is an eq or an in
// on path, with string or number values: the rows that an index on path
// gives for it are the matches, with nothing left to check but the hidden
// rows (a scan checks those by group, allVisible).
func (q *Query) Exact(path string) bool {
	n := q.where

	if q.user != nil {
		n = q.user
	}

	if a, ok := n.(andNode); ok && len(a) == 1 {
		n = a[0]
	}

	f, ok := n.(*fieldNode)
	if !ok || q.needsDoc || f.path != path || f.isTime || len(f.ops) != 1 {
		return false
	}

	o := f.ops[0]

	if o.name != "eq" && o.name != "in" {
		return false
	}

	return !slices.ContainsFunc(o.vals, func(v Value) bool { return v.Kind != Number && v.Kind != String })
}

// TimeOnly tells whether the only condition of the request is a range (or
// an equality) on the time field, with time values: a row group whose
// instants are all inside it matches with every live row, unread.
func (q *Query) TimeOnly() bool {
	n := q.where

	if q.user != nil {
		n = q.user
	}

	if a, ok := n.(andNode); ok && len(a) == 1 {
		n = a[0]
	}

	f, ok := n.(*fieldNode)
	if !ok || q.needsDoc || !f.isTime || len(f.ops) == 0 {
		return false
	}

	for _, o := range f.ops {
		switch o.name {
		case "gt", "gte", "lt", "lte", "eq", "in":
		default:
			return false
		}

		if slices.ContainsFunc(o.vals, func(v Value) bool { return v.Kind != Time }) {
			return false
		}
	}

	return true
}

func (q *Query) Paths() []string {
	return slices.Clone(q.paths)
}

// Match reports whether the decoded document satisfies where.
func (q *Query) Match(m map[string]any) bool {
	return q.where.match(m)
}

// MatchCols evaluates where on the candidate values of each path, by the
// order of Paths. Every path must have its values: a column scan passes the
// values of a document only when it has them, and matches the rest on the
// decoded document.
func (q *Query) MatchCols(cands [][]Value) bool {
	return q.where.matchCols(cands)
}

// IsTime tells if path is the collection time field, or its TTL field.
func (q *Query) IsTime(path string) bool {
	return path == q.opts.TimeField || (q.opts.ExpiresField != "" && path == q.opts.ExpiresField)
}

// Indexable lists the conditions an index can answer. The caller still
// runs Match on every candidate, so an index only has to return a superset.
func (q *Query) Indexable() []Cond {
	var out []Cond

	collect(q.where, &out)

	return out
}

func collect(n Node, out *[]Cond) {
	switch x := n.(type) {
	case andNode:
		for _, c := range x {
			collect(c, out)
		}
	case *fieldNode:
		for _, o := range x.ops {
			if indexableOp(o) {
				*out = append(*out, Cond{Path: x.path, Op: o.name, Vals: o.vals})
			}

			if o.name == "regex" && o.prefix != "" && !x.isTime {
				*out = append(*out, prefixConds(x.path, o.prefix)...)
			}
		}
	}
}

func indexableOp(o op) bool {
	switch o.name {
	case "eq", "in", "gt", "gte", "lt", "lte":
		return !slices.ContainsFunc(o.vals, func(v Value) bool { return !v.Scalar() })
	}

	return false
}

// prefixConds is the range of the strings that start with prefix, as gte
// and lt conditions: an index or a zone map can answer them.
func prefixConds(path, prefix string) []Cond {
	out := []Cond{{Path: path, Op: "gte", Vals: []Value{{Kind: String, Str: prefix, Raw: prefix}}}}

	if hi := prefixSuccessor(prefix); hi != "" {
		out = append(out, Cond{Path: path, Op: "lt", Vals: []Value{{Kind: String, Str: hi, Raw: hi}}})
	}

	return out
}
