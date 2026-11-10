package collection

import (
	"bytes"
	"encoding/json"
	"strings"
)

// idField is the primary key of every collection.
const idField = "_id"

// withID returns the doc id, adding a generated _id when the doc has none. Docs that already carry their id keep
// their original bytes (compacted).
func (c *Collection) withID(raw []byte) (string, json.RawMessage, error) {
	m, err := decodeObject(raw)

	if err != nil {
		return "", nil, err
	}

	if err := checkDocFields(m); err != nil {
		return "", nil, err
	}

	// The version belongs to the daemon: a _v that the client sends (a
	// document it read and sends back) is dropped, not stored.
	if _, ok := m[versionField]; ok {
		delete(m, versionField)

		raw = dropTopField(raw, versionField)
	}

	val, present := m[idField]

	if !present {
		id := NewID()
		m["_id"] = id

		doc, err := json.Marshal(m)

		return id, doc, err
	}

	id, ok := val.(string)
	if !ok || id == "" {
		return "", nil, errf("invalid_id", "field %q must be a non-empty string", idField)
	}

	var buf bytes.Buffer

	if err := json.Compact(&buf, raw); err != nil {
		return "", nil, errf("invalid_doc", "%v", err)
	}

	return id, buf.Bytes(), nil
}

// versionField is the document version. The daemon adds it to the answers;
// a written document never stores it (a sent _v is dropped).
const versionField = "_v"

// blobField is the key of a blob reference ({"$blob": "sha256:..."}), the
// one field name that can start with $.
const blobField = "$blob"

// checkDocFields refuses a field name that starts with $ at any depth (an
// operator), as $set does. A top-level _v is dropped by withID.
func checkDocFields(m map[string]any) error {
	return checkDollarKeys(m, "")
}

// checkDollarKeys walks the objects and arrays of v. prefix is the path of v.
func checkDollarKeys(v any, prefix string) error {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			path := k

			if prefix != "" {
				path = prefix + "." + k
			}

			if strings.HasPrefix(k, "$") && k != blobField {
				return errf("invalid_field", "field %q starts with $", path)
			}

			if err := checkDollarKeys(child, path); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range x {
			if err := checkDollarKeys(child, prefix); err != nil {
				return err
			}
		}
	}

	return nil
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
func applyChange(cur json.RawMessage, ch Change) (json.RawMessage, error) {
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
