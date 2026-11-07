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

	"github.com/bytedance/sonic"
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
