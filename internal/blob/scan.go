package blob

import (
	"bytes"
	"encoding/json"
	"strings"
)

var marker = []byte(`"$blob"`)

// Refs returns the hash of every {"$blob":"sha256:..."} reference in a JSON
// document, at any depth. A document that is not JSON has none.
func Refs(doc []byte) []string {
	if !bytes.Contains(doc, marker) {
		return nil
	}

	var v any

	if err := json.Unmarshal(doc, &v); err != nil {
		return nil
	}

	var out []string

	collect(v, &out)

	return out
}

// RefInfos is Refs with the size and type that the reference carries. The
// Blob field of each result is the full "sha256:<hex>" form.
func RefInfos(doc []byte) []Ref {
	if !bytes.Contains(doc, marker) {
		return nil
	}

	var v any

	if err := json.Unmarshal(doc, &v); err != nil {
		return nil
	}

	var out []Ref

	collectInfos(v, &out)

	return out
}

func collectInfos(v any, out *[]Ref) {
	switch x := v.(type) {
	case map[string]any:
		if s, ok := x["$blob"].(string); ok {
			if _, err := ParseHash(s); err == nil && strings.HasPrefix(s, prefix) {
				size, _ := x["size"].(float64)
				typ, _ := x["type"].(string)

				*out = append(*out, Ref{Blob: s, Size: int64(size), Type: typ})
			}
		}

		for _, child := range x {
			collectInfos(child, out)
		}
	case []any:
		for _, child := range x {
			collectInfos(child, out)
		}
	}
}

func collect(v any, out *[]string) {
	switch x := v.(type) {
	case map[string]any:
		if s, ok := x["$blob"].(string); ok {
			if h, err := ParseHash(s); err == nil && strings.HasPrefix(s, prefix) {
				*out = append(*out, h)
			}
		}

		for _, child := range x {
			collect(child, out)
		}
	case []any:
		for _, child := range x {
			collect(child, out)
		}
	}
}

// Externalize replaces every string value longer than threshold bytes by a
// reference to a blob that put stored. It returns doc itself when nothing
// changed. Keys are never replaced. The document is written again with
// sorted keys when a value moved.
func Externalize(doc []byte, threshold int64, put func(value string) (Ref, error)) ([]byte, error) {
	if threshold <= 0 || int64(len(doc)) <= threshold {
		return doc, nil
	}

	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()

	var v any

	if err := dec.Decode(&v); err != nil {
		return nil, err
	}

	changed := false

	v, err := move(v, threshold, put, &changed)
	if err != nil {
		return nil, err
	}

	if !changed {
		return doc, nil
	}

	var buf bytes.Buffer

	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(v); err != nil {
		return nil, err
	}

	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func move(v any, threshold int64, put func(string) (Ref, error), changed *bool) (any, error) {
	switch x := v.(type) {
	case string:
		if int64(len(x)) <= threshold {
			return x, nil
		}

		ref, err := put(x)
		if err != nil {
			return nil, err
		}

		*changed = true

		out := map[string]any{"$blob": ref.Blob, "size": ref.Size}

		if ref.Type != "" {
			out["type"] = ref.Type
		}

		return out, nil
	case map[string]any:
		for k, child := range x {
			moved, err := move(child, threshold, put, changed)
			if err != nil {
				return nil, err
			}

			x[k] = moved
		}
	case []any:
		for i, child := range x {
			moved, err := move(child, threshold, put, changed)
			if err != nil {
				return nil, err
			}

			x[i] = moved
		}
	}

	return v, nil
}
