package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/thadeu/ndb/internal/engine"
)

var sealedMark = []byte(`"ndb:e1:`)

// mayDecrypt tells if the caller gets the plain value of the encrypted
// fields of coll: the unix socket, a daemon with no tokens, the full token
// and a token whose decrypt list covers coll.
func mayDecrypt(r *http.Request, coll string) bool {
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
	if !ok || len(def.Encrypted) == 0 || !mayDecrypt(r, coll) {
		return doc
	}

	return db.Box().OpenDoc(def.Place(), def.Encrypted, doc)
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
