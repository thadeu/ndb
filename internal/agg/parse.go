package agg

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"

	"github.com/thadeu/ndb/internal/query"
)

// Options are the collection facts the parser needs.
type Options struct {
	// TimeField is the collection time field: RFC 3339 strings compare as time.
	TimeField string

	// ExpiresField is the TTL field of the collection (plan §11.5).
	ExpiresField string
}

// Pipeline is a validated aggregate request.
type Pipeline struct {
	// Query is the where of the leading $match stages, plus the $sort that
	// orders $first and $last. It is never nil. The collection plans it
	// like a find.
	Query *query.Query

	Limit   int
	Skip    int
	After   string
	Count   bool
	Explain bool

	opts   Options
	group  *group
	post   []stage
	hash   string
	offset int
}

var topKeys = []string{"pipeline", "limit", "skip", "after", "count", "explain"}

var knownStages = []string{"$match", "$group", "$sort", "$limit", "$skip", "$project"}

// Parse validates the body of POST /{coll}/_aggregate. Every error has
// the code invalid_pipeline.
func Parse(body []byte, opts Options) (*Pipeline, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, invalid("body is empty")
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

	raw, ok := top["pipeline"]
	if !ok {
		return nil, invalid("pipeline is required")
	}

	var stages []json.RawMessage

	if err := json.Unmarshal(raw, &stages); err != nil || len(stages) == 0 {
		return nil, invalid("pipeline must be a non-empty array of stages")
	}

	p := &Pipeline{opts: opts, hash: hashOf(raw)}

	if err := p.parseScalars(top); err != nil {
		return nil, err
	}

	if err := p.parseStages(stages); err != nil {
		return nil, err
	}

	return p, nil
}

func hashOf(raw json.RawMessage) string {
	var buf bytes.Buffer

	if json.Compact(&buf, raw) != nil {
		buf.Write(raw)
	}

	sum := sha256.Sum256(buf.Bytes())

	return hex.EncodeToString(sum[:8])
}

func (p *Pipeline) parseScalars(top map[string]json.RawMessage) error {
	for key, dst := range map[string]*int{"limit": &p.Limit, "skip": &p.Skip} {
		raw, ok := top[key]
		if !ok {
			continue
		}

		if err := json.Unmarshal(raw, dst); err != nil || *dst < 0 {
			return invalid("%s must be a non-negative integer", key)
		}
	}

	for key, dst := range map[string]*bool{"count": &p.Count, "explain": &p.Explain} {
		if raw, ok := top[key]; ok && json.Unmarshal(raw, dst) != nil {
			return invalid("%s must be a boolean", key)
		}
	}

	if raw, ok := top["after"]; ok && json.Unmarshal(raw, &p.After) != nil {
		return invalid("after must be a string")
	}

	return nil
}

// parseStages walks the stages in order: the ones before the $group feed the
// query, the ones after it become post stages.
func (p *Pipeline) parseStages(stages []json.RawMessage) error {
	var (
		matches []json.RawMessage
		sortArg json.RawMessage
	)

	for _, raw := range stages {
		name, arg, err := stageOf(raw)
		if err != nil {
			return err
		}

		switch {
		case name == "$group":
			if p.group != nil {
				return invalid("only one $group stage is allowed")
			}

			if p.group, err = compileGroup(arg, p.opts); err != nil {
				return err
			}
		case p.group == nil:
			matches, sortArg, err = beforeGroup(name, arg, matches, sortArg)
		default:
			var st stage

			if st, err = compileStage(name, arg); err == nil {
				p.post = append(p.post, st)
			}
		}

		if err != nil {
			return err
		}
	}

	if p.group == nil {
		return invalid("the pipeline needs a $group stage")
	}

	return p.buildQuery(matches, sortArg)
}

// stageOf splits {"$name": arg}.
func stageOf(raw json.RawMessage) (string, json.RawMessage, error) {
	var m map[string]json.RawMessage

	if err := json.Unmarshal(raw, &m); err != nil || len(m) != 1 {
		return "", nil, invalid("a stage must be an object with one $stage key")
	}

	for name, arg := range m {
		if !slices.Contains(knownStages, name) {
			return "", nil, invalid("unknown stage %s", name)
		}

		return name, arg, nil
	}

	return "", nil, nil
}

func beforeGroup(name string, arg json.RawMessage, matches []json.RawMessage, sortArg json.RawMessage) ([]json.RawMessage, json.RawMessage, error) {
	switch name {
	case "$match":
		return append(matches, arg), sortArg, nil
	case "$sort":
		if sortArg != nil {
			return nil, nil, invalid("only one $sort stage is allowed before $group")
		}

		return matches, arg, nil
	}

	return nil, nil, invalid("%s before $group is not supported: use $match and $sort", name)
}

// buildQuery makes the find that selects the documents of the group. The
// sort is kept only when $first or $last need an order.
func (p *Pipeline) buildQuery(matches []json.RawMessage, sortArg json.RawMessage) error {
	body := map[string]any{}

	switch len(matches) {
	case 0:
	case 1:
		body["query"] = matches[0]
	default:
		body["query"] = map[string]any{"$and": matches}
	}

	if sortArg != nil {
		body["sort"] = sortArg

		if _, err := p.parseQuery(body); err != nil {
			return err
		}
	}

	if !p.group.needPos {
		delete(body, "sort")
	}

	q, err := p.parseQuery(body)
	p.Query = q

	return err
}

func (p *Pipeline) parseQuery(body map[string]any) (*query.Query, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, invalid("%v", err)
	}

	q, err := query.Parse(raw, query.Options{TimeField: p.opts.TimeField, ExpiresField: p.opts.ExpiresField})
	if err != nil {
		return nil, invalid("%s", strings.TrimPrefix(err.Error(), "invalid_query: "))
	}

	return q, nil
}

func invalid(format string, args ...any) *query.Error {
	return query.NewError("invalid_pipeline", format, args...)
}

// Paths lists the document paths the group reads, in the order that Values
// returns them. The where and sort paths are read through Query.
func (p *Pipeline) Paths() []string {
	return slices.Clone(p.group.paths)
}

// IsTime tells if path is the collection time field.
func (p *Pipeline) IsTime(path string) bool {
	return path == p.opts.TimeField
}

// Values reads the group paths of a decoded document.
func (p *Pipeline) Values(m map[string]any) []query.Value {
	out := make([]query.Value, len(p.group.paths))

	for i, path := range p.group.paths {
		out[i] = ValueOf(m, path, p.IsTime(path))
	}

	return out
}
