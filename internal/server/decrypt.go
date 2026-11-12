package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/crypt"
	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/lookup"
)

var sealedMark = []byte(`"ndb:e1:`)

// mayDecrypt tells if the answer has the plain value of the encrypted
// fields of coll: the call asks with ?decrypt=1, and the caller has the
// right (the unix socket, a daemon with no tokens, the full token, or a
// token whose decrypt list covers coll). Any other call gets the stored,
// sealed value.
func mayDecrypt(r *http.Request, coll string) bool {
	if r.URL.Query().Get("decrypt") != "1" {
		return false
	}

	t, ok := r.Context().Value(tokenKey{}).(Token)
	if !ok || t.Full {
		return true
	}

	for _, glob := range t.Decrypt {
		if covers(glob, coll) {
			return true
		}
	}

	return false
}

// openDoc opens the encrypted fields of one document of coll for a caller
// that may decrypt. Any other document is returned as it is.
func openDoc(r *http.Request, db *engine.DB, coll string, doc json.RawMessage) json.RawMessage {
	if !bytes.Contains(doc, sealedMark) {
		return doc
	}

	if strings.ContainsAny(coll, "*,") {
		coll = rowColl(doc)
	}

	def, ok := db.Definition(coll)
	if !ok || len(def.Encrypted) == 0 {
		return doc
	}

	doc = crypt.DropFields(doc, hashFields(def))

	if !mayDecrypt(r, coll) {
		return doc
	}

	return db.Box().OpenDoc(def.Place(), def.Encrypted, doc)
}

// hashFields are the <field>_bidx names of the blind fields: never in an
// answer.
func hashFields(def catalog.Def) []string {
	out := make([]string, 0, len(def.Blind))

	for f := range def.Blind {
		out = append(out, f+crypt.HashSuffix)
	}

	return out
}

// openLookups opens the documents that a lookup put in a row, each with
// the definition and the right of the collection it comes from.
func openLookups(r *http.Request, db *engine.DB, row json.RawMessage, specs []lookup.Spec) json.RawMessage {
	if !bytes.Contains(row, sealedMark) {
		return row
	}

	var m map[string]json.RawMessage

	if json.Unmarshal(row, &m) != nil {
		return row
	}

	for _, sp := range specs {
		v, ok := m[sp.Name]
		if !ok {
			continue
		}

		if sp.Many {
			var list []json.RawMessage

			if json.Unmarshal(v, &list) != nil {
				continue
			}

			for i, d := range list {
				list[i] = openDoc(r, db, sp.From, openLookups(r, db, d, sp.Nested))
			}

			m[sp.Name], _ = json.Marshal(list)

			continue
		}

		if string(v) != "null" {
			m[sp.Name] = openDoc(r, db, sp.From, openLookups(r, db, v, sp.Nested))
		}
	}

	out, err := json.Marshal(m)
	if err != nil {
		return row
	}

	return out
}

// openRows is openDoc on each row of a page.
func openRows(r *http.Request, db *engine.DB, coll string, rows []json.RawMessage) {
	for i, row := range rows {
		rows[i] = openDoc(r, db, coll, row)
	}
}

// rowColl is the _coll of a row of a pattern read: its collection.
func rowColl(doc json.RawMessage) string {
	var row struct {
		Coll string `json:"_coll"`
	}

	_ = json.Unmarshal(doc, &row)

	return row.Coll
}
