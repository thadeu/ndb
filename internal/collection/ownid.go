package collection

import (
	"bytes"
	"encoding/json"
	"unicode/utf8"
)

var idKey = []byte(`"_id"`)

// ownID tells whether the top-level "_id" of the stored doc (valid JSON)
// is the string id. The common case, a doc that starts with "_id" and has
// it only once, is a scan of the bytes; any other doc is decoded.
func ownID(doc []byte, id string) bool {
	if idFirst(doc, id) {
		return true
	}

	var own struct {
		ID string `json:"_id"`
	}

	return json.Unmarshal(doc, &own) == nil && own.ID == id
}

// idFirst is true when the doc is {"_id":"<id>", ...} with the id in plain
// form and no other "_id" in its bytes. False means "not sure".
func idFirst(doc []byte, id string) bool {
	if !plainID(id) || bytes.Count(doc, idKey) != 1 {
		return false
	}

	rest, ok := skip(doc, '{')
	if !ok || !bytes.HasPrefix(rest, idKey) {
		return false
	}

	if rest, ok = skip(rest[len(idKey):], ':'); !ok {
		return false
	}

	if rest, ok = skip(rest, '"'); !ok || len(rest) < len(id)+1 || string(rest[:len(id)]) != id || rest[len(id)] != '"' {
		return false
	}

	rest = bytes.TrimLeft(rest[len(id)+1:], " \t\r\n")

	return len(rest) > 0 && (rest[0] == ',' || rest[0] == '}')
}

// skip drops the white space and then the byte c.
func skip(b []byte, c byte) ([]byte, bool) {
	b = bytes.TrimLeft(b, " \t\r\n")

	if len(b) == 0 || b[0] != c {
		return nil, false
	}

	return b[1:], true
}

// plainID is an id that JSON writes as it is: no quote, no backslash, no
// control byte, valid UTF-8.
func plainID(id string) bool {
	for i := range len(id) {
		if c := id[i]; c == '"' || c == '\\' || c < 0x20 {
			return false
		}
	}

	return utf8.ValidString(id)
}
