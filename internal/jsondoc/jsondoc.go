// Package jsondoc decodes the documents that ndb has stored, on the hot read
// paths: queries, aggregations, seals and compactions. It uses Sonic, which
// is several times faster than encoding/json for a map[string]any.
//
// Only documents that ndb already checked and wrote go through it. The input
// of the API (requests, new documents) and every write still use
// encoding/json, so what ndb accepts does not change.
package jsondoc

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/bytedance/sonic"
	"github.com/bytedance/sonic/ast"
)

// api decodes numbers as json.Number, like encoding/json with UseNumber, and
// copies strings, so no value keeps the bytes of the document alive.
var api = sonic.Config{UseNumber: true, CopyString: true}.Froze()

// Decode decodes a stored JSON object.
func Decode(raw []byte) (map[string]any, error) {
	var m map[string]any

	if err := api.Unmarshal(bytes.TrimSpace(raw), &m); err != nil {
		return nil, err
	}

	return m, nil
}

// Number is the type of the numbers that Decode returns.
type Number = json.Number

// Pick decodes only the top-level fields keys of a stored JSON object (each
// one whole), skipping the rest of the document: a read that needs three
// fields of a forty-field document decodes three. A key the document does
// not have is left out, as Decode would.
func Pick(raw []byte, keys []string) (map[string]any, error) {
	s := string(bytes.TrimSpace(raw))
	m := make(map[string]any, len(keys))

	for _, k := range keys {
		node, err := ast.NewSearcher(s).GetByPathCopy(k)
		if err != nil {
			if errors.Is(err, ast.ErrNotExist) {
				continue
			}

			return nil, err
		}

		v, err := node.InterfaceUseNumber()
		if err != nil {
			return nil, err
		}

		m[k] = v
	}

	return m, nil
}

// Lookup follows path, one key per step, through the objects of a decoded
// document. ok is false when a key is missing or a step is not an object
// (it does not walk into arrays).
func Lookup(m map[string]any, path []string) (val any, ok bool) {
	var cur any = m

	for _, key := range path {
		obj, isObj := cur.(map[string]any)
		if !isObj {
			return nil, false
		}

		if cur, ok = obj[key]; !ok {
			return nil, false
		}
	}

	return cur, true
}
