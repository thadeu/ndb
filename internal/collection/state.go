package collection

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/thadeu/ndb/internal/segment"
	"github.com/thadeu/ndb/internal/wal"
)

// entry is immutable. A memtable entry holds doc. A sealed entry (a stub
// made by a lookup, sealed.go) has doc nil: its bytes are in the segment at
// loc.
type entry struct {
	v   uint64
	doc json.RawMessage

	// seq is the insertion position (read.go). It is set once, before the
	// entry is visible to readers, and kept across updates.
	seq uint64

	// tx is the WAL transaction of the last change.
	tx uint64

	loc *location
}

type location struct {
	seg segment.Reader
	row int
}

// raw returns the document. Only a sealed entry reads from a segment.
func (e *entry) raw() (json.RawMessage, error) {
	if e.doc != nil {
		return e.doc, nil
	}

	b, err := e.loc.seg.Raw(e.loc.row)

	return b, err
}

// key returns the canonical value of a unique field.
func (e *entry) key(field string) (string, bool) {
	raw, err := e.raw()
	if err != nil {
		return "", false
	}

	return uniqueKey(raw, field)
}

func uniqueKeys(doc json.RawMessage, fields []string) map[string]string {
	out := map[string]string{}

	for _, f := range fields {
		if k, ok := uniqueKey(doc, f); ok {
			out[f] = k
		}
	}

	return out
}

// state is the memtable documents by id plus their unique values (field →
// value → id). With segments, a document that is not in docs is looked up in
// them (sealed), unless it was deleted since the last seal (gone).
type state struct {
	docs   map[string]*entry
	unique map[string]map[string]string

	gone   map[string]struct{}
	sealed func(id string) (*entry, bool)
	owners func(field, key string) []string
}

func newState(uniqueFields []string) *state {
	s := &state{docs: map[string]*entry{}, unique: map[string]map[string]string{}, gone: map[string]struct{}{}}

	for _, f := range uniqueFields {
		s.unique[f] = map[string]string{}
	}

	return s
}

func (s *state) apply(op wal.Op) {
	if op.Kind == wal.Del {
		s.remove(op.ID)

		return
	}

	s.put(op.ID, &entry{v: op.V, doc: op.Doc})
}

// get returns the current version of id: the memtable, then the segments.
func (s *state) get(id string) (*entry, bool) {
	if e, ok := s.docs[id]; ok {
		return e, true
	}

	if _, deleted := s.gone[id]; deleted || s.sealed == nil {
		return nil, false
	}

	return s.sealed(id)
}

// put sets the entry of id, replacing the old one and its unique keys.
func (s *state) put(id string, e *entry) {
	if old, ok := s.docs[id]; ok {
		s.unindex(id, old)
	}

	delete(s.gone, id)

	s.docs[id] = e
	s.index(id, e)
}

func (s *state) remove(id string) {
	if old, ok := s.docs[id]; ok {
		s.unindex(id, old)
	}

	delete(s.docs, id)

	if s.sealed != nil {
		s.gone[id] = struct{}{}
	}
}

// release takes a memtable entry out after a seal wrote it: its id is now
// found in the segment. The unique values are rebuilt by shrink, from the
// few entries that stay, instead of decoding every sealed one.
func (s *state) release(id string) {
	delete(s.docs, id)
}

// shrink copies the maps into new ones after a seal: a Go map keeps its
// buckets when entries leave, and a seal takes almost every entry out. The
// unique values are rebuilt from the entries that stay.
func (s *state) shrink() {
	s.docs = clone(s.docs)
	s.gone = clone(s.gone)

	for f := range s.unique {
		s.unique[f] = map[string]string{}
	}

	for id, e := range s.docs {
		s.index(id, e)
	}
}

func clone[K comparable, V any](m map[K]V) map[K]V {
	out := make(map[K]V, len(m))

	for k, v := range m {
		out[k] = v
	}

	return out
}

// owner returns the id whose current version holds key in the unique field:
// the memtable first, then the sealed rows, checked against get.
func (s *state) owner(field, key string, get func(string) (*entry, bool)) string {
	if id := s.unique[field][key]; id != "" {
		return id
	}

	if s.owners == nil {
		return ""
	}

	for _, id := range s.owners(field, key) {
		if cur, ok := get(id); ok {
			if k, ok := cur.key(field); ok && k == key {
				return id
			}
		}
	}

	return ""
}

func (s *state) index(id string, e *entry) {
	for f, idx := range s.unique {
		if key, ok := e.key(f); ok {
			idx[key] = id
		}
	}
}

func (s *state) unindex(id string, e *entry) {
	for f, idx := range s.unique {
		if key, ok := e.key(f); ok && idx[key] == id {
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

	return v.head.get(id)
}

func (v *view) uniqueOwner(field, key string) string {
	if owners, ok := v.unique[field]; ok {
		if id, ok := owners[key]; ok {
			return id
		}
	}

	return v.head.owner(field, key, v.get)
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
			if key, ok := old.key(f); ok && v.uniqueOwner(f, key) == op.ID {
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
