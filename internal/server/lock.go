package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/engine"
)

// The pessimistic lock of a document. See docs/dev/query.md (Locks).
const (
	defaultLockTTL  = 30 * time.Second
	maxLockTTL      = 5 * time.Minute
	defaultLockWait = 10 * time.Second
	maxLockWait     = time.Minute
)

type lockReq struct {
	TTL  json.RawMessage `json:"ttl"`
	Wait json.RawMessage `json:"wait"`
}

type unlockReq struct {
	Token string `json:"token"`
}

// lock is POST /{coll}/_lock/{id}: {"ttl", "wait"}. It answers the token,
// when the lock expires, and the document as it is now.
func (s *Server) lock(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	name := r.PathValue("coll")

	var req lockReq

	body, err := readBody(w, r, db.MaxLine(name))
	if err == nil && len(bytes.TrimSpace(body)) > 0 {
		err = decode(body, &req)
	}

	if err != nil {
		writeError(w, err)

		return
	}

	ttl, err := lockSpan("ttl", req.TTL, defaultLockTTL, maxLockTTL, false)
	if err != nil {
		writeError(w, err)

		return
	}

	wait, err := lockSpan("wait", req.Wait, defaultLockWait, maxLockWait, true)
	if err != nil {
		writeError(w, err)

		return
	}

	col, err := db.Get(name)
	if err != nil {
		writeError(w, err)

		return
	}

	held, err := col.Lock(r.Context(), r.PathValue("id"), ttl, wait)
	if err != nil {
		writeError(w, err)

		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"token": held.Token, "expires_at": held.Expires.UTC().Format(time.RFC3339Nano), "doc": held.Doc})
}

// unlock is POST /{coll}/_unlock/{id}: {"token"}.
func (s *Server) unlock(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	name := r.PathValue("coll")

	var req unlockReq

	body, err := readBody(w, r, db.MaxLine(name))
	if err == nil {
		err = decode(body, &req)
	}

	if err == nil && req.Token == "" {
		err = badRequest("unlock needs the token of the lock")
	}

	if err != nil {
		writeError(w, err)

		return
	}

	col, err := db.Get(name)
	if err != nil {
		writeError(w, err)

		return
	}

	if err := col.Unlock(r.PathValue("id"), req.Token); err != nil {
		writeError(w, err)

		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"unlocked": true})
}

// lockSpan reads ttl or wait: a duration string ("30s") or a number of
// seconds. Absent is def. Zero is allowed for wait only (do not wait).
func lockSpan(name string, raw json.RawMessage, def, limit time.Duration, zero bool) (time.Duration, error) {
	raw = bytes.TrimSpace(raw)

	if len(raw) == 0 || string(raw) == "null" {
		return def, nil
	}

	var d time.Duration

	if raw[0] == '"' {
		var text string

		if err := json.Unmarshal(raw, &text); err != nil {
			return 0, badRequest("%s is not valid: %v", name, err)
		}

		parsed, err := catalog.ParseDuration(text)
		if err != nil {
			return 0, badRequest("%s: %v", name, err)
		}

		d = parsed
	} else {
		secs, err := strconv.ParseFloat(string(raw), 64)
		if err != nil || secs < 0 || secs > limit.Seconds() {
			return 0, badRequest("%s must be a duration string or a number of seconds, up to %s", name, limit)
		}

		d = time.Duration(secs * float64(time.Second))
	}

	if d > limit || d < 0 || d == 0 && !zero {
		return 0, badRequest("%s must be more than 0 and up to %s", name, limit)
	}

	return d, nil
}
