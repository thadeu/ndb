// Package lookup reads the documents of other collections that the rows of
// an answer point to: the lookup key of _search and the $lookup stage of
// _aggregate (M18). This package is the pure part: the spec, the body split
// and the JSON of a row. The engine runs it (engine/lookup.go).
//
//	"lookup": {
//	  "customer": { "belongs_to": "customers", "fk": "customer_id" },
//	  "items":    { "has_many": "order_items", "fk": "order_id", "limit": 50 }
//	}
//
// belongs_to: the fk is here (here.fk == there.pk), one query per page for
// every key ($in), an object or null. has_many and has_one: the fk is there
// (there.fk == here.pk), one query per parent with its own query, sort and
// limit; has_many gives an array, has_one an object or null.
package lookup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/thadeu/ndb/internal/catalog"
)

const (
	// MaxLevels is how deep lookups nest (a lookup inside a relation).
	MaxLevels = 2

	// DefaultLimit and MaxLimit bound a has_many (and a MongoDB $lookup).
	DefaultLimit = 100
	MaxLimit     = 1000

	// MaxAttached is how many documents one answer may attach in all.
	MaxAttached = 10_000
)

// Spec is one relation.
type Spec struct {
	// Name is the field of the row that gets the result.
	Name string

	// From is the other collection.
	From string

	// Local is the field of the row, Foreign the field of the other
	// collection; they are equal for a match.
	Local   string
	Foreign string

	// Many gives an array; else an object or null.
	Many bool

	// Batched reads every key of a page in one query ($in on Foreign);
	// else one query per row, with Query, Sort and Limit.
	Batched bool

	Query  json.RawMessage
	Sort   json.RawMessage
	Limit  int
	Fields []string

	// Nested are the lookups of the documents found.
	Nested []Spec
}

// Error is a lookup error with its code (lookup_invalid, lookup_unindexed,
// lookup_too_large).
type Error struct {
	code string
	msg  string
}

func (e *Error) Error() string { return e.code + ": " + e.msg }

func (e *Error) Code() string { return e.code }

// NewError makes an Error.
func NewError(code, format string, args ...any) *Error {
	return &Error{code: code, msg: fmt.Sprintf(format, args...)}
}

func invalid(format string, args ...any) *Error {
	return NewError("lookup_invalid", format, args...)
}

var nameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

var keys = []string{"belongs_to", "has_many", "has_one", "foreign_key", "fk", "primary_key", "pk", "query", "sort", "limit", "fields", "lookup"}

// Parse reads the value of the lookup key: an object of relations by name.
func Parse(raw json.RawMessage) ([]Spec, error) {
	return parseLevel(raw, 1)
}

func parseLevel(raw json.RawMessage, level int) ([]Spec, error) {
	if level > MaxLevels {
		return nil, invalid("lookups nest %d levels at most", MaxLevels)
	}

	var rels map[string]json.RawMessage

	if err := json.Unmarshal(raw, &rels); err != nil || rels == nil {
		return nil, invalid("lookup must be an object of relations by name")
	}

	names := make([]string, 0, len(rels))

	for name := range rels {
		names = append(names, name)
	}

	slices.Sort(names)

	out := make([]Spec, 0, len(names))

	for _, name := range names {
		spec, err := parseOne(name, rels[name], level)
		if err != nil {
			return nil, err
		}

		out = append(out, spec)
	}

	return out, nil
}

func parseOne(name string, raw json.RawMessage, level int) (Spec, error) {
	if !nameRe.MatchString(name) {
		return Spec{}, invalid("%q is not a field name for a lookup (letters, digits and _; not _id)", name)
	}

	var m map[string]json.RawMessage

	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return Spec{}, invalid("%s: a relation is an object", name)
	}

	for k := range m {
		if !slices.Contains(keys, k) {
			return Spec{}, invalid("%s: unknown key %q", name, k)
		}
	}

	kind, from, err := kindOf(name, m)
	if err != nil {
		return Spec{}, err
	}

	fk, err := either(name, m, "foreign_key", "fk", "")
	if err != nil {
		return Spec{}, err
	}

	if fk == "" {
		return Spec{}, invalid("%s: foreign_key (fk) is required", name)
	}

	pk, err := either(name, m, "primary_key", "pk", "_id")
	if err != nil {
		return Spec{}, err
	}

	s := Spec{Name: name, From: from, Query: m["query"], Sort: m["sort"]}

	switch kind {
	case "belongs_to":
		s.Local, s.Foreign, s.Batched = fk, pk, true

		if m["query"] != nil || m["sort"] != nil || m["limit"] != nil {
			return Spec{}, invalid("%s: query, sort and limit are for has_many and has_one", name)
		}
	case "has_one":
		s.Local, s.Foreign, s.Limit = pk, fk, 1

		if m["limit"] != nil {
			return Spec{}, invalid("%s: has_one is one document; use has_many with a limit", name)
		}
	default:
		s.Local, s.Foreign, s.Many, s.Limit = pk, fk, true, DefaultLimit

		if raw := m["limit"]; raw != nil {
			if err := json.Unmarshal(raw, &s.Limit); err != nil || s.Limit < 1 || s.Limit > MaxLimit {
				return Spec{}, invalid("%s: limit is 1 to %d", name, MaxLimit)
			}
		}
	}

	if err := checkField(name, s.Local, s.Foreign); err != nil {
		return Spec{}, err
	}

	if raw := m["fields"]; raw != nil {
		if err := json.Unmarshal(raw, &s.Fields); err != nil {
			return Spec{}, invalid("%s: fields is a list of field names", name)
		}
	}

	if raw := m["lookup"]; raw != nil {
		nested, err := parseLevel(raw, level+1)
		if err != nil {
			return Spec{}, err
		}

		s.Nested = nested
	}

	return s, nil
}

func kindOf(name string, m map[string]json.RawMessage) (string, string, error) {
	var kind, from string

	for _, k := range []string{"belongs_to", "has_many", "has_one"} {
		raw, ok := m[k]
		if !ok {
			continue
		}

		if kind != "" {
			return "", "", invalid("%s: one of belongs_to, has_many or has_one", name)
		}

		if err := json.Unmarshal(raw, &from); err != nil || !catalog.ValidName(from) {
			return "", "", invalid("%s: %s is a collection name (not a pattern)", name, k)
		}

		kind = k
	}

	if kind == "" {
		return "", "", invalid("%s: say belongs_to, has_many or has_one and the collection", name)
	}

	return kind, from, nil
}

// either reads one of two names of a string key (foreign_key or fk).
func either(name string, m map[string]json.RawMessage, long, short, def string) (string, error) {
	if m[long] != nil && m[short] != nil {
		return "", invalid("%s: %s and %s are the same key; send one", name, long, short)
	}

	raw := m[long]

	if raw == nil {
		raw = m[short]
	}

	if raw == nil {
		return def, nil
	}

	var v string

	if err := json.Unmarshal(raw, &v); err != nil {
		return "", invalid("%s: %s is a field name", name, long)
	}

	return v, nil
}

func checkField(name string, fields ...string) error {
	for _, f := range fields {
		if f == "" || strings.HasPrefix(f, "$") {
			return invalid("%s: %q is not a field name", name, f)
		}
	}

	return nil
}

// ParseStage reads the value of a $lookup stage: the MongoDB names (from,
// localField, foreignField, as: always an array) or one relation of the
// Rails names.
func ParseStage(raw json.RawMessage) (Spec, error) {
	var m map[string]json.RawMessage

	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return Spec{}, invalid("$lookup is an object")
	}

	if _, ok := m["from"]; !ok {
		specs, err := Parse(raw)
		if err != nil {
			return Spec{}, err
		}

		if len(specs) != 1 {
			return Spec{}, invalid("$lookup has one relation; send one stage for each")
		}

		return specs[0], nil
	}

	var s struct {
		From         string `json:"from"`
		LocalField   string `json:"localField"`
		ForeignField string `json:"foreignField"`
		As           string `json:"as"`
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&s); err != nil {
		return Spec{}, invalid("$lookup takes from, localField, foreignField and as (pipeline and let are not supported)")
	}

	switch {
	case !catalog.ValidName(s.From):
		return Spec{}, invalid("$lookup: from is a collection name")
	case !nameRe.MatchString(s.As):
		return Spec{}, invalid("$lookup: as is a field name")
	}

	if err := checkField("$lookup", s.LocalField, s.ForeignField); err != nil {
		return Spec{}, err
	}

	spec := Spec{Name: s.As, From: s.From, Local: s.LocalField, Foreign: s.ForeignField, Many: true, Limit: DefaultLimit}

	// On _id one query per page finds every match.
	spec.Batched = s.ForeignField == "_id"

	return spec, nil
}

// Split takes the lookup key out of a _search body. When the body lists
// fields, the local fields of the relations are added (added lists them, to
// be removed from the rows after the lookup).
func Split(body []byte) (rest []byte, specs []Spec, added []string, err error) {
	if !bytes.Contains(body, []byte(`"lookup"`)) {
		return body, nil, nil, nil
	}

	var top map[string]json.RawMessage

	if err := json.Unmarshal(body, &top); err != nil {
		return body, nil, nil, nil
	}

	raw, ok := top["lookup"]
	if !ok {
		return body, nil, nil, nil
	}

	delete(top, "lookup")

	specs, err = Parse(raw)
	if err != nil {
		return nil, nil, nil, err
	}

	if f, ok := top["fields"]; ok {
		var fields []string

		if json.Unmarshal(f, &fields) == nil && len(fields) > 0 {
			fields, added = Need(fields, Locals(specs)...)
			top["fields"], _ = json.Marshal(fields)
		}
	}

	rest, err = json.Marshal(top)

	return rest, specs, added, err
}

// Locals are the local fields of specs.
func Locals(specs []Spec) []string {
	out := make([]string, 0, len(specs))

	for _, s := range specs {
		out = append(out, s.Local)
	}

	return out
}

// Need adds to a fields list the paths it does not keep, and returns them
// as added. An empty list keeps every field: nothing is added.
func Need(fields []string, paths ...string) ([]string, []string) {
	if len(fields) == 0 {
		return fields, nil
	}

	out := slices.Clone(fields)

	var added []string

	for _, p := range paths {
		if !covered(out, p) {
			out = append(out, p)
			added = append(added, p)
		}
	}

	return out, added
}

// covered tells if fields keep path (the path or one of its parents).
func covered(fields []string, path string) bool {
	for _, f := range fields {
		if f == path || strings.HasPrefix(path, f+".") {
			return true
		}
	}

	return false
}

// KeyVal is one value of a field: Key compares (1 and 1.0 are the same),
// Raw goes into a query.
type KeyVal struct {
	Key string
	Raw json.RawMessage
}

// Keys are the scalar values of a dot path in a row: one, or one per
// element of an array; none for a missing field, null or an object.
func Keys(row json.RawMessage, path string) []KeyVal {
	keys, _ := KeysOf(row, path)

	return keys
}

// KeysOf is Keys, and tells if the field is an array (a belongs_to then
// gives an array).
func KeysOf(row json.RawMessage, path string) ([]KeyVal, bool) {
	var v any = decode(row)

	for _, part := range strings.Split(path, ".") {
		obj, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}

		v = obj[part]
	}

	if arr, ok := v.([]any); ok {
		var out []KeyVal

		for _, el := range arr {
			if kv, ok := keyOf(el); ok {
				out = append(out, kv)
			}
		}

		return out, true
	}

	if kv, ok := keyOf(v); ok {
		return []KeyVal{kv}, false
	}

	return nil, false
}

func keyOf(v any) (KeyVal, bool) {
	raw, _ := json.Marshal(v)

	switch x := v.(type) {
	case string:
		return KeyVal{Key: "s:" + x, Raw: raw}, true
	case bool:
		return KeyVal{Key: "b:" + strconv.FormatBool(x), Raw: raw}, true
	case json.Number:
		f, err := x.Float64()
		if err != nil {
			return KeyVal{}, false
		}

		return KeyVal{Key: "n:" + strconv.FormatFloat(f, 'g', -1, 64), Raw: raw}, true
	}

	return KeyVal{}, false
}

func decode(row json.RawMessage) map[string]any {
	dec := json.NewDecoder(bytes.NewReader(row))
	dec.UseNumber()

	var m map[string]any

	_ = dec.Decode(&m)

	return m
}

type member struct {
	key string
	raw json.RawMessage
}

// members are the top level fields of an object, in their order.
func members(row json.RawMessage) ([]member, bool) {
	dec := json.NewDecoder(bytes.NewReader(row))

	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, false
	}

	var out []member

	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, false
		}

		key, _ := tok.(string)

		var raw json.RawMessage

		if err := dec.Decode(&raw); err != nil {
			return nil, false
		}

		out = append(out, member{key, raw})
	}

	return out, true
}

func build(ms []member) json.RawMessage {
	var b bytes.Buffer

	b.WriteByte('{')

	for i, m := range ms {
		if i > 0 {
			b.WriteByte(',')
		}

		key, _ := json.Marshal(m.key)

		b.Write(key)
		b.WriteByte(':')
		b.Write(m.raw)
	}

	b.WriteByte('}')

	return b.Bytes()
}

// Set gives the row with the field name set to value: replaced in its
// place, or added at the end.
func Set(row json.RawMessage, name string, value json.RawMessage) json.RawMessage {
	ms, ok := members(row)
	if !ok {
		return row
	}

	for i := range ms {
		if ms[i].key == name {
			ms[i].raw = value

			return build(ms)
		}
	}

	return build(append(ms, member{name, value}))
}

// Without gives the row without the top level fields of paths (the first
// part of a dot path).
func Without(row json.RawMessage, paths []string) json.RawMessage {
	if len(paths) == 0 {
		return row
	}

	ms, ok := members(row)
	if !ok {
		return row
	}

	drop := map[string]bool{}

	for _, p := range paths {
		drop[strings.SplitN(p, ".", 2)[0]] = true
	}

	return build(slices.DeleteFunc(ms, func(m member) bool { return drop[m.key] }))
}

// SplitStages takes the $lookup stages off the end of an _aggregate
// pipeline: they run on the output rows. A $lookup before another stage is
// lookup_invalid.
func SplitStages(body []byte) (rest []byte, specs []Spec, added []string, err error) {
	if !bytes.Contains(body, []byte(`"$lookup"`)) {
		return body, nil, nil, nil
	}

	var top map[string]json.RawMessage

	if err := json.Unmarshal(body, &top); err != nil {
		return body, nil, nil, nil
	}

	var stages []map[string]json.RawMessage

	if err := json.Unmarshal(top["pipeline"], &stages); err != nil {
		return body, nil, nil, nil
	}

	cut := len(stages)

	for cut > 0 && stages[cut-1]["$lookup"] != nil {
		cut--
	}

	for _, st := range stages[:cut] {
		if st["$lookup"] != nil {
			return nil, nil, nil, invalid("$lookup goes at the end of the pipeline, after $group and the stages that follow it")
		}
	}

	for _, st := range stages[cut:] {
		if len(st) != 1 {
			return nil, nil, nil, invalid("a $lookup stage has one key")
		}

		spec, err := ParseStage(st["$lookup"])
		if err != nil {
			return nil, nil, nil, err
		}

		specs = append(specs, spec)
	}

	top["pipeline"], err = json.Marshal(stages[:cut])
	if err != nil {
		return nil, nil, nil, err
	}

	rest, err = json.Marshal(top)

	return rest, specs, nil, err
}
