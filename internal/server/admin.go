package server

import (
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/diskusage"
	"github.com/thadeu/ndb/internal/engine"
)

const (
	defaultQuarantineLimit = 100
	maxQuarantineLimit     = 1000

	// quarantineLineMax is how much of a bad line the API returns. The
	// tailer keeps the first 4 KiB.
	quarantineLineMax = 2048
)

// partBytes is the disk usage of one collection by part.
type partBytes struct {
	WAL      int64 `json:"wal"`
	Segments int64 `json:"segments"`
	Meta     int64 `json:"meta"`
}

// statsBody is the answer of GET /{coll}/_stats and one element of
// GET /_stats.
type statsBody struct {
	Name        string            `json:"name"`
	Write       catalog.WriteMode `json:"write"`
	Rows        int               `json:"rows"`
	Quarantined int64             `json:"quarantined"`
	Indexes     []indexBody       `json:"indexes"`
	Segments    int               `json:"segments"`
	WALFiles    int               `json:"wal_files"`
	Bytes       partBytes         `json:"bytes"`
	Total       int64             `json:"total"`
}

func collectionStats(st engine.Stats, du diskusage.Collection) statsBody {
	return statsBody{
		Name:        st.Name,
		Write:       st.Write,
		Rows:        st.Rows,
		Quarantined: st.Quarantined,
		Indexes:     indexBodies(st),
		Segments:    du.SegmentFiles,
		WALFiles:    du.WALFiles,
		Bytes:       partBytes{WAL: du.Bytes.WAL, Segments: du.Bytes.Segments, Meta: du.Bytes.Meta},
		Total:       du.Total,
	}
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	col, err := db.Get(r.PathValue("coll"))
	if err != nil {
		writeError(w, err)

		return
	}

	st := col.Stats()
	du := diskusage.ScanCollection(col.Dir(), st.Name)

	writeJSON(w, http.StatusOK, collectionStats(st, du))
}

// globalStats is GET /_stats: every collection plus totals, the shared
// files and the daemon identity.
func (s *Server) globalStats(w http.ResponseWriter, _ *http.Request, db *engine.DB) {
	cols := []statsBody{}

	var totals struct {
		Collections int   `json:"collections"`
		Rows        int   `json:"rows"`
		Quarantined int64 `json:"quarantined"`
		Segments    int   `json:"segments"`
		WALFiles    int   `json:"wal_files"`
		Bytes       int64 `json:"bytes"`
	}

	for _, st := range db.AllStats() {
		col, err := db.Get(st.Name)
		if err != nil {
			continue
		}

		body := collectionStats(st, diskusage.ScanCollection(col.Dir(), st.Name))

		cols = append(cols, body)

		totals.Collections++
		totals.Rows += body.Rows
		totals.Quarantined += body.Quarantined
		totals.Segments += body.Segments
		totals.WALFiles += body.WALFiles
		totals.Bytes += body.Total
	}

	shared := sharedUsage(db.Dir())

	totals.Bytes += shared.Total()

	writeJSON(w, http.StatusOK, map[string]any{
		"version":        s.version,
		"uptime_seconds": time.Since(s.started).Seconds(),
		"collections":    cols,
		"totals":         totals,
		"blobs_bytes":    shared.Blobs,
		"cursors_bytes":  shared.Cursors,
		"shared_bytes":   shared.Total(),
	})
}

// sharedUsage is the usage of the files that no collection owns: blobs,
// cursors and the catalog.
func sharedUsage(dataDir string) diskusage.Parts {
	u, err := diskusage.Scan(dataDir)
	if err != nil {
		return diskusage.Parts{}
	}

	return u.Shared
}

// size is GET /_size: the same disk scan as "ndb size".
func (s *Server) size(w http.ResponseWriter, _ *http.Request, db *engine.DB) {
	u, err := diskusage.Scan(db.Dir())
	if err != nil {
		writeError(w, err)

		return
	}

	if u.Collections == nil {
		u.Collections = []diskusage.Collection{}
	}

	writeJSON(w, http.StatusOK, u)
}

// getCollection is GET /{coll}: the catalog definition and the state of
// each index, in the shape of the PUT answer.
func (s *Server) getCollection(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	name := r.PathValue("coll")

	col, err := db.Get(name)
	if err != nil {
		writeError(w, err)

		return
	}

	def, ok := db.Definition(name)
	if !ok {
		writeError(w, catalog.ErrUnknown)

		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"definition": def,
		"indexes":    indexBodies(col.Stats()),
	})
}

// compact is POST /{coll}/_compact: seal, then merge every segment now.
func (s *Server) compact(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	name := r.PathValue("coll")

	col, err := db.Get(name)
	if err != nil {
		writeError(w, err)

		return
	}

	began := time.Now()

	res, err := col.Compact()
	if err != nil {
		writeError(w, err)

		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"collection":  name,
		"before":      res.Before,
		"after":       res.After,
		"duration_ms": time.Since(began).Milliseconds(),
	})
}

type quarantineBody struct {
	TS        string `json:"ts"`
	File      string `json:"file"`
	Offset    int64  `json:"offset"`
	Error     string `json:"error"`
	Line      string `json:"line"`
	Truncated bool   `json:"truncated,omitempty"`
}

// quarantine is GET /{coll}/_quarantine?limit=N: the last N bad lines of
// a direct collection (default 100, at most 1000), oldest first. An
// engine-mode collection has none.
func (s *Server) quarantine(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	name := r.PathValue("coll")

	limit, err := quarantineLimit(r.URL.Query().Get("limit"))
	if err != nil {
		writeError(w, err)

		return
	}

	col, err := db.Get(name)
	if err != nil {
		writeError(w, err)

		return
	}

	rows, total, err := col.Quarantine(limit)
	if err != nil {
		writeError(w, err)

		return
	}

	out := make([]quarantineBody, len(rows))

	for i, row := range rows {
		line, cut := cutLine(row.Line, quarantineLineMax)

		out[i] = quarantineBody{
			TS:        row.TS,
			File:      row.File,
			Offset:    row.Offset,
			Error:     row.Reason,
			Line:      line,
			Truncated: row.Truncated || cut,
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"collection": name, "total": total, "rows": out})
}

func quarantineLimit(raw string) (int, error) {
	if raw == "" {
		return defaultQuarantineLimit, nil
	}

	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, badRequest("limit must be a positive integer")
	}

	return min(n, maxQuarantineLimit), nil
}

// cutLine cuts s to at most max bytes, on a character boundary.
func cutLine(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}

	cut := max

	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}

	return s[:cut], true
}

// truncate is POST /{coll}/_truncate?confirm={coll}: remove every
// document, keep the definition and the indexes.
func (s *Server) truncate(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	name := r.PathValue("coll")

	if err := db.Truncate(name, r.URL.Query().Get("confirm")); err != nil {
		writeError(w, err)

		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"collection": name, "truncated": true})
}

// drop is DELETE /{coll}?confirm={coll}: remove the collection, its
// definition and the positions of its watch consumers.
func (s *Server) drop(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	name := r.PathValue("coll")

	if err := db.Drop(name, r.URL.Query().Get("confirm")); err != nil {
		writeError(w, err)

		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"collection": name, "dropped": true})
}
