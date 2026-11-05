package collection

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

// withID returns the doc id, adding a generated _id when the collection
// uses _id and the doc has none. Docs that already carry their id keep
// their original bytes (compacted).
func (c *Collection) withID(raw []byte) (string, json.RawMessage, error) {
	m, err := decodeObject(raw)
	if err != nil {
		return "", nil, err
	}

	val, present := m[c.def.ID]

	if !present {
		if c.def.ID != "_id" {
			return "", nil, errf("missing_id", "field %q is required", c.def.ID)
		}

		id := newID()
		m["_id"] = id

		doc, err := json.Marshal(m)

		return id, doc, err
	}

	id, ok := val.(string)
	if !ok || id == "" {
		return "", nil, errf("invalid_id", "field %q must be a non-empty string", c.def.ID)
	}

	var buf bytes.Buffer

	if err := json.Compact(&buf, raw); err != nil {
		return "", nil, errf("invalid_doc", "%v", err)
	}

	return id, buf.Bytes(), nil
}

func decodeObject(raw []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	var m map[string]any

	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, errf("invalid_doc", "a document must be a JSON object")
	}

	return m, nil
}

// applyChange returns cur with $set and $unset applied.
func applyChange(cur json.RawMessage, ch Change, idField string) (json.RawMessage, error) {
	if _, ok := ch.Set[idField]; ok {
		return nil, errf("immutable_id", "%s cannot change", idField)
	}

	for _, p := range ch.Unset {
		if p == idField {
			return nil, errf("immutable_id", "%s cannot be removed", idField)
		}
	}

	m, err := decodeObject(cur)
	if err != nil {
		return nil, err
	}

	for path, val := range ch.Set {
		if err := setPath(m, path, val); err != nil {
			return nil, err
		}
	}

	for _, path := range ch.Unset {
		unsetPath(m, path)
	}

	return json.Marshal(m)
}

func setPath(m map[string]any, path string, val any) error {
	parts := strings.Split(path, ".")
	obj := m

	for _, p := range parts[:len(parts)-1] {
		next, exists := obj[p]

		if !exists {
			child := map[string]any{}
			obj[p] = child
			obj = child

			continue
		}

		child, ok := next.(map[string]any)
		if !ok {
			return errf("invalid_path", "%s: %s is not an object", path, p)
		}

		obj = child
	}

	obj[parts[len(parts)-1]] = val

	return nil
}

func unsetPath(m map[string]any, path string) {
	parts := strings.Split(path, ".")
	obj := m

	for _, p := range parts[:len(parts)-1] {
		child, ok := obj[p].(map[string]any)
		if !ok {
			return
		}

		obj = child
	}

	delete(obj, parts[len(parts)-1])
}

// newID is 24 hex chars: 4 bytes of unix seconds + 8 random bytes, so ids
// sort roughly by creation time.
func newID() string {
	var b [12]byte

	binary.BigEndian.PutUint32(b[:4], uint32(time.Now().Unix()))
	_, _ = rand.Read(b[4:])

	return hex.EncodeToString(b[:])
}
