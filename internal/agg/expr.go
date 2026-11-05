package agg

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	_ "time/tzdata" // zone names must work on a host with no zoneinfo

	"github.com/thadeu/ndb/internal/query"
)

type exprKind uint8

const (
	exprPath exprKind = iota
	exprConst
	exprTrunc
)

// expr is a path, a constant or a $dateTrunc of a path.
type expr struct {
	kind  exprKind
	slot  int // index in group.paths
	cst   query.Value
	trunc truncSpec
}

type truncSpec struct {
	unit string
	loc  *time.Location
}

func (e *expr) eval(vals []query.Value) query.Value {
	switch e.kind {
	case exprPath:
		return vals[e.slot]
	case exprConst:
		return e.cst
	}

	return e.trunc.apply(vals[e.slot])
}

type keyExpr struct {
	name string
	expr *expr
}

type accOp uint8

const (
	opCount accOp = iota
	opSum
	opAvg
	opMin
	opMax
	opFirst
	opLast
)

var accOps = map[string]accOp{
	"$count": opCount, "$sum": opSum, "$avg": opAvg, "$min": opMin,
	"$max": opMax, "$first": opFirst, "$last": opLast,
}

type accDef struct {
	name string
	op   accOp
	arg  *expr // nil for $count
}

// group is a compiled $group stage.
type group struct {
	single  bool // _id is one expression, not an object
	keys    []keyExpr
	accs    []accDef
	paths   []string
	needPos bool // $first or $last: rows need their position
	opts    Options
}

func (g *group) slot(path string) int {
	if i := slices.Index(g.paths, path); i >= 0 {
		return i
	}

	g.paths = append(g.paths, path)

	return len(g.paths) - 1
}

func compileGroup(arg json.RawMessage, opts Options) (*group, error) {
	var top map[string]json.RawMessage

	if err := json.Unmarshal(arg, &top); err != nil {
		return nil, invalid("$group must be an object")
	}

	rawID, ok := top["_id"]
	if !ok {
		return nil, invalid("$group needs an _id")
	}

	g := &group{opts: opts}

	if err := g.compileID(rawID); err != nil {
		return nil, err
	}

	names := make([]string, 0, len(top))

	for name := range top {
		if name != "_id" {
			names = append(names, name)
		}
	}

	slices.Sort(names)

	for _, name := range names {
		if err := g.compileAcc(name, top[name]); err != nil {
			return nil, err
		}
	}

	return g, nil
}

// compileID reads _id: an expression, or an object of named expressions.
// The names keep the order they were written in.
func (g *group) compileID(raw json.RawMessage) error {
	if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		return g.singleID(raw)
	}

	pairs, err := orderedPairs(raw)
	if err != nil {
		return invalid("_id: %v", err)
	}

	if len(pairs) == 1 && strings.HasPrefix(pairs[0].name, "$") {
		return g.singleID(raw)
	}

	if len(pairs) == 0 {
		return invalid("_id: a composite key needs at least one field")
	}

	for i, pr := range pairs {
		if err := checkName(pr.name); err != nil {
			return invalid("_id: %v", err)
		}

		if slices.ContainsFunc(pairs[:i], func(o pair) bool { return o.name == pr.name }) {
			return invalid("_id: duplicate field %q", pr.name)
		}

		e, err := g.compileRaw(pr.value)
		if err != nil {
			return invalid("_id.%s: %v", pr.name, err)
		}

		g.keys = append(g.keys, keyExpr{name: pr.name, expr: e})
	}

	return nil
}

func (g *group) singleID(raw json.RawMessage) error {
	e, err := g.compileRaw(raw)
	if err != nil {
		return invalid("_id: %v", err)
	}

	g.single = true
	g.keys = []keyExpr{{expr: e}}

	return nil
}

func (g *group) compileAcc(name string, raw json.RawMessage) error {
	if err := checkName(name); err != nil {
		return invalid("%v", err)
	}

	var m map[string]json.RawMessage

	if err := json.Unmarshal(raw, &m); err != nil || len(m) != 1 {
		return invalid("accumulator %q must be an object with one operator", name)
	}

	for opName, arg := range m {
		op, ok := accOps[opName]
		if !ok {
			return invalid("accumulator %q: unknown operator %s", name, opName)
		}

		def := accDef{name: name, op: op}

		if op == opCount {
			var empty map[string]any

			if json.Unmarshal(arg, &empty) != nil || len(empty) != 0 || empty == nil {
				return invalid("accumulator %q: $count takes {}", name)
			}
		} else {
			e, err := g.compileRaw(arg)
			if err != nil {
				return invalid("accumulator %q: %v", name, err)
			}

			def.arg = e
		}

		g.needPos = g.needPos || op == opFirst || op == opLast
		g.accs = append(g.accs, def)
	}

	return nil
}

func checkName(name string) error {
	if name == "" || strings.HasPrefix(name, "$") || strings.Contains(name, ".") {
		return fmt.Errorf("bad field name %q: it must be non-empty, with no '$' prefix and no '.'", name)
	}

	return nil
}

type pair struct {
	name  string
	value json.RawMessage
}

// orderedPairs reads the keys of a JSON object in the order they appear.
func orderedPairs(raw json.RawMessage) ([]pair, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))

	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("not an object")
	}

	var out []pair

	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}

		var v json.RawMessage

		if err := dec.Decode(&v); err != nil {
			return nil, err
		}

		out = append(out, pair{name: tok.(string), value: v})
	}

	return out, nil
}

func (g *group) compileRaw(raw json.RawMessage) (*expr, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	var v any

	if err := dec.Decode(&v); err != nil {
		return nil, err
	}

	return g.compileExpr(v)
}

func (g *group) compileExpr(v any) (*expr, error) {
	switch x := v.(type) {
	case string:
		if !strings.HasPrefix(x, "$") {
			return &expr{kind: exprConst, cst: query.FromJSON(x, false)}, nil
		}

		path, err := pathOf(x)
		if err != nil {
			return nil, err
		}

		return &expr{kind: exprPath, slot: g.slot(path)}, nil
	case nil, bool, json.Number:
		return &expr{kind: exprConst, cst: query.FromJSON(x, false)}, nil
	case map[string]any:
		if spec, ok := x["$dateTrunc"]; ok && len(x) == 1 {
			return g.compileTrunc(spec)
		}
	}

	return nil, fmt.Errorf("unsupported expression %s", describe(v))
}

func describe(v any) string {
	b, _ := json.Marshal(v)

	return string(b)
}

// pathOf turns "$a.b" into "a.b".
func pathOf(s string) (string, error) {
	path := strings.TrimPrefix(s, "$")

	if path == "" || slices.Contains(strings.Split(path, "."), "") || strings.HasPrefix(path, "$") {
		return "", fmt.Errorf("bad field path %q", s)
	}

	return path, nil
}

var truncUnits = []string{"minute", "hour", "day", "week", "month"}

func (g *group) compileTrunc(spec any) (*expr, error) {
	m, ok := spec.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("$dateTrunc needs an object")
	}

	for k := range m {
		if k != "date" && k != "unit" && k != "timezone" {
			return nil, fmt.Errorf("$dateTrunc: unknown key %q", k)
		}
	}

	date, _ := m["date"].(string)

	if !strings.HasPrefix(date, "$") {
		return nil, fmt.Errorf("$dateTrunc: date must be a field path like \"$ts\"")
	}

	path, err := pathOf(date)
	if err != nil {
		return nil, err
	}

	unit, _ := m["unit"].(string)

	if !slices.Contains(truncUnits, unit) {
		return nil, fmt.Errorf("$dateTrunc: unit must be one of %s", strings.Join(truncUnits, ", "))
	}

	loc := time.UTC

	if zone, has := m["timezone"]; has {
		name, _ := zone.(string)

		if loc, err = time.LoadLocation(name); err != nil || name == "" || name == "Local" {
			return nil, fmt.Errorf("$dateTrunc: unknown timezone %q", zone)
		}
	}

	return &expr{kind: exprTrunc, slot: g.slot(path), trunc: truncSpec{unit: unit, loc: loc}}, nil
}

// apply cuts v down to the start of its bucket. A value that is not a time
// gives null.
func (s truncSpec) apply(v query.Value) query.Value {
	var t time.Time

	switch v.Kind {
	case query.Time:
		t = time.Unix(0, v.Int)
	case query.String:
		parsed, ok := parseTime(v.Str)
		if !ok {
			return query.Value{Kind: query.Null}
		}

		t = parsed
	default:
		return query.Value{Kind: query.Null}
	}

	t = s.cut(t.In(s.loc))

	if t.Year() < 1678 || t.Year() > 2261 {
		return query.Value{Kind: query.Null}
	}

	return query.Value{Kind: query.Time, Int: t.UnixNano(), Raw: t.UTC().Format(time.RFC3339Nano)}
}

func parseTime(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, s)

	if err != nil {
		t, err = time.Parse(time.DateOnly, s)
	}

	if err != nil || t.Year() < 1678 || t.Year() > 2261 {
		return time.Time{}, false
	}

	return t, true
}

func (s truncSpec) cut(t time.Time) time.Time {
	y, mo, d := t.Date()

	switch s.unit {
	case "minute":
		return time.Date(y, mo, d, t.Hour(), t.Minute(), 0, 0, s.loc)
	case "hour":
		return time.Date(y, mo, d, t.Hour(), 0, 0, 0, s.loc)
	case "day":
		return time.Date(y, mo, d, 0, 0, 0, 0, s.loc)
	case "week":
		return time.Date(y, mo, d-(int(t.Weekday())+6)%7, 0, 0, 0, 0, s.loc)
	}

	return time.Date(y, mo, 1, 0, 0, 0, 0, s.loc)
}

// ValueOf is the value of path in a decoded document (see the package notes).
func ValueOf(m map[string]any, path string, isTime bool) query.Value {
	var (
		leaves []any
		via    bool
	)

	if !resolve(m, strings.Split(path, "."), &leaves, &via) {
		return query.Value{}
	}

	if _, isArr := leaves[0].([]any); !via && len(leaves) == 1 && !isArr {
		return query.FromJSON(leaves[0], isTime)
	}

	raws := make([]any, 0, len(leaves))

	for _, l := range leaves {
		if arr, ok := l.([]any); ok {
			raws = append(raws, arr...)

			continue
		}

		raws = append(raws, l)
	}

	return query.FromJSON(raws, false)
}

// resolve collects the values at parts, walking through arrays of objects.
// via is set when an array was walked through.
func resolve(cur any, parts []string, out *[]any, via *bool) bool {
	if len(parts) == 0 {
		*out = append(*out, cur)

		return true
	}

	switch c := cur.(type) {
	case map[string]any:
		next, ok := c[parts[0]]
		if !ok {
			return false
		}

		return resolve(next, parts[1:], out, via)
	case []any:
		*via = true
		found := false

		for _, el := range c {
			if resolve(el, parts, out, via) {
				found = true
			}
		}

		return found
	}

	return false
}

// outRaw is the JSON value of v for the output rows. Numbers are float64 and
// times are UTC, so a row looks the same whichever engine made it.
func outRaw(v query.Value) any {
	switch v.Kind {
	case query.Number:
		return v.Num
	case query.String:
		return v.Str
	case query.Bool:
		return v.Bool
	case query.Time:
		return time.Unix(0, v.Int).UTC().Format(time.RFC3339Nano)
	case query.Object, query.Array:
		return normalize(v.Raw)
	}

	return nil
}

func normalize(v any) any {
	switch x := v.(type) {
	case json.Number:
		f, _ := x.Float64()

		return f + 0
	case map[string]any:
		out := make(map[string]any, len(x))

		for k, e := range x {
			out[k] = normalize(e)
		}

		return out
	case []any:
		out := make([]any, len(x))

		for i, e := range x {
			out[i] = normalize(e)
		}

		return out
	}

	return v
}
