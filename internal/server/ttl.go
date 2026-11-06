package server

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/engine"
)

type expireReq struct {
	ID string          `json:"_id"`
	In json.RawMessage `json:"in"`
	At string          `json:"at"`
}

type persistReq struct {
	ID string `json:"_id"`
}

// parseExpireIn reads expire_in: a Go duration string ("90m", "1d") or a
// number of seconds. Absent or null is 0, which means no expiry.
func parseExpireIn(raw json.RawMessage) (time.Duration, error) {
	raw = bytes.TrimSpace(raw)

	if len(raw) == 0 || string(raw) == "null" {
		return 0, nil
	}

	if raw[0] == '"' {
		var s string

		if err := json.Unmarshal(raw, &s); err != nil {
			return 0, badRequest("expire_in is not valid: %v", err)
		}

		d, err := catalog.ParseDuration(s)
		if err != nil {
			return 0, badRequest("expire_in: %v", err)
		}

		return d, nil
	}

	secs, err := strconv.ParseFloat(string(raw), 64)
	if err != nil || secs <= 0 || secs > math.MaxInt64/float64(time.Second) {
		return 0, badRequest("expire_in must be a duration string or a positive number of seconds")
	}

	return time.Duration(secs * float64(time.Second)), nil
}

// expire is POST /v1/c/{coll}/expire: {"_id", "in" | "at"}.
func (s *Server) expire(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	name := r.PathValue("coll")

	var req expireReq

	body, err := readBody(w, r, db.MaxLine(name))
	if err != nil {
		writeError(w, err)

		return
	}

	if err := decode(body, &req); err != nil {
		writeError(w, err)

		return
	}

	e, err := req.expiry()
	if err != nil {
		writeError(w, err)

		return
	}

	col, err := db.Get(name)
	if err != nil {
		writeError(w, err)

		return
	}

	v, at, err := col.Expire(r.Context(), req.ID, e)
	if err != nil {
		writeError(w, err)

		return
	}

	s.metrics.writes.Add(1)

	writeJSON(w, http.StatusOK, map[string]any{"_id": req.ID, "v": v, "expires_at": at.UTC().Format(time.RFC3339Nano)})
}

// expiry is the "in" or "at" of the request. Exactly one must be set.
func (r expireReq) expiry() (collection.Expiry, error) {
	in, err := parseExpireIn(r.In)
	if err != nil {
		return collection.Expiry{}, err
	}

	if (in > 0) == (r.At != "") {
		return collection.Expiry{}, badRequest("expire needs exactly one of in or at")
	}

	if in > 0 {
		return collection.Expiry{In: in}, nil
	}

	at, err := time.Parse(time.RFC3339Nano, r.At)
	if err != nil {
		return collection.Expiry{}, badRequest("at must be an RFC 3339 time: %v", err)
	}

	return collection.Expiry{At: at}, nil
}

// persist is POST /v1/c/{coll}/persist: {"_id"}.
func (s *Server) persist(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	name := r.PathValue("coll")

	var req persistReq

	body, err := readBody(w, r, db.MaxLine(name))
	if err != nil {
		writeError(w, err)

		return
	}

	if err := decode(body, &req); err != nil {
		writeError(w, err)

		return
	}

	col, err := db.Get(name)
	if err != nil {
		writeError(w, err)

		return
	}

	v, err := col.Persist(r.Context(), req.ID)
	if err != nil {
		writeError(w, err)

		return
	}

	s.metrics.writes.Add(1)

	writeJSON(w, http.StatusOK, writeResult{ID: req.ID, V: v})
}

// ttl is GET /v1/c/{coll}/{id}/ttl: the whole seconds left (rounded up), or
// -1 when the document never expires.
func (s *Server) ttl(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	col, err := db.Get(r.PathValue("coll"))
	if err != nil {
		writeError(w, err)

		return
	}

	id := r.PathValue("id")

	info, err := col.TTL(id)
	if err != nil {
		writeError(w, err)

		return
	}

	if info.Never {
		writeJSON(w, http.StatusOK, map[string]any{"_id": id, "ttl": -1, "expires_at": nil})

		return
	}

	secs := int64(math.Ceil(info.Remaining.Seconds()))

	writeJSON(w, http.StatusOK, map[string]any{"_id": id, "ttl": secs, "expires_at": info.At.UTC().Format(time.RFC3339Nano)})
}
