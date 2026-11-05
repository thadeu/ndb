package collection

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/thadeu/ndb/internal/wal"
)

// entry is immutable; head and durable states share entries.
type entry struct {
	v   uint64
	doc json.RawMessage
}

// state is documents by id plus the unique indexes (field → value → id).
type state struct {
	docs   map[string]*entry
	unique map[string]map[string]string
}

func newState(uniqueFields []string) *state {
	s := &state{docs: map[string]*entry{}, unique: map[string]map[string]string{}}

	for _, f := range uniqueFields {
		s.unique[f] = map[string]string{}
	}

	return s
}

func (s *state) apply(op wal.Op) {
	if old, ok := s.docs[op.ID]; ok {
		s.unindex(op.ID, old.doc)
	}

	if op.Kind == wal.Del {
		delete(s.docs, op.ID)

		return
	}

	s.docs[op.ID] = &entry{v: op.V, doc: op.Doc}
	s.index(op.ID, op.Doc)
}

func (s *state) index(id string, doc json.RawMessage) {
	for f, idx := range s.unique {
		if key, ok := uniqueKey(doc, f); ok {
			idx[key] = id
		}
	}
}

func (s *state) unindex(id string, doc json.RawMessage) {
	for f, idx := range s.unique {
		if key, ok := uniqueKey(doc, f); ok && idx[key] == id {
			delete(idx, key)
		}
	}
}

// uniqueKey is the canonical JSON of the field value. Missing or null
// values are not indexed, so many docs can lack a unique field.
func uniqueKey(doc json.RawMessage, path string) (string, bool) {
	var m map[string]any

	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()

	if dec.Decode(&m) != nil {
		return "", false
	}

	v, ok := getPath(m, path)
	if !ok || v == nil {
		return "", false
	}

	b, err := json.Marshal(v)
	if err != nil {
		return "", false
	}

	return string(b), true
}

func getPath(m map[string]any, path string) (any, bool) {
	parts := strings.Split(path, ".")

	var cur any = m

	for _, p := range parts {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}

		cur, ok = obj[p]
		if !ok {
			return nil, false
		}
	}

	return cur, true
}

// view is one transaction's look at head: its own changes first, then head.
// Nothing touches head until the writer stages the transaction.
type view struct {
	head   *state
	docs   map[string]*entry
	unique map[string]map[string]string
}

func newView(head *state) *view {
	return &view{head: head, docs: map[string]*entry{}, unique: map[string]map[string]string{}}
}

func (v *view) get(id string) (*entry, bool) {
	if e, ok := v.docs[id]; ok {
		return e, e != nil
	}

	e, ok := v.head.docs[id]

	return e, ok
}

func (v *view) uniqueOwner(field, key string) string {
	if owners, ok := v.unique[field]; ok {
		if id, ok := owners[key]; ok {
			return id
		}
	}

	return v.head.unique[field][key]
}

func (v *view) setOwner(field, key, id string) {
	if v.unique[field] == nil {
		v.unique[field] = map[string]string{}
	}

	v.unique[field][key] = id
}

// apply records op in the view, after the caller checked it.
func (v *view) apply(op wal.Op) {
	if old, ok := v.get(op.ID); ok {
		for f := range v.head.unique {
			if key, ok := uniqueKey(old.doc, f); ok && v.uniqueOwner(f, key) == op.ID {
				v.setOwner(f, key, "")
			}
		}
	}

	if op.Kind == wal.Del {
		v.docs[op.ID] = nil

		return
	}

	v.docs[op.ID] = &entry{v: op.V, doc: op.Doc}

	for f := range v.head.unique {
		if key, ok := uniqueKey(op.Doc, f); ok {
			v.setOwner(f, key, op.ID)
		}
	}
}

// checkUnique fails when another live doc owns a unique value of doc.
func (v *view) checkUnique(id string, doc json.RawMessage) error {
	for f := range v.head.unique {
		key, ok := uniqueKey(doc, f)
		if !ok {
			continue
		}

		if owner := v.uniqueOwner(f, key); owner != "" && owner != id {
			return errf("unique_violation", "%s=%s is used by %s", f, key, owner)
		}
	}

	return nil
}
