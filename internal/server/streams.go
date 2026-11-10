package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/streams"
)

// The routes of docs/dev/http/streams.md. The streams package does the
// work; these handlers read the body, call it, and write the answer with
// the IDs as <ms>-<seq>.

func streamRoutes(s *Server) map[string][]route {
	return map[string][]route{
		"_streams":                        {{"GET", s.ready(s.streamInfo)}},
		"_streams/append":                 {{"POST", s.ready(s.streamAppend)}},
		"_streams/trim":                   {{"POST", s.ready(s.streamTrim)}},
		"_streams/read":                   {{"POST", s.ready(s.streamRead)}},
		"_streams/groups":                 {{"GET", s.ready(s.streamGroups)}},
		"_streams/groups/{}":              {{"PUT", s.ready(s.groupCreate)}, {"DELETE", s.ready(s.groupDestroy)}},
		"_streams/groups/{}/setid":        {{"POST", s.ready(s.groupSetID)}},
		"_streams/groups/{}/consumers":    {{"GET", s.ready(s.groupConsumers)}},
		"_streams/groups/{}/consumers/{}": {{"PUT", s.ready(s.consumerCreate)}, {"DELETE", s.ready(s.consumerDelete)}},
		"_streams/groups/{}/read":         {{"POST", s.ready(s.groupRead)}},
		"_streams/groups/{}/ack":          {{"POST", s.ready(s.groupAck)}},
		"_streams/groups/{}/pending":      {{"POST", s.ready(s.groupPending)}},
		"_streams/groups/{}/claim":        {{"POST", s.ready(s.groupClaim)}},
	}
}

// streamBody reads and decodes an optional body into v.
func streamBody(w http.ResponseWriter, r *http.Request, v any) error {
	body, err := readBody(w, r, engine.DefaultMaxLine)
	if err != nil {
		return err
	}

	return decodeOptional(body, v)
}

// answer writes v, or the error.
func answer(w http.ResponseWriter, status int, v any, err error) {
	if err != nil {
		writeError(w, err)

		return
	}

	writeJSON(w, status, v)
}

func (s *Server) streamInfo(w http.ResponseWriter, r *http.Request, _ *engine.DB) {
	info, err := s.streams.Info(r.Context(), r.PathValue("coll"))

	answer(w, http.StatusOK, info, err)
}

func (s *Server) streamAppend(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	var req struct {
		Doc      map[string]any       `json:"doc"`
		Docs     []map[string]any     `json:"docs"`
		ID       string               `json:"id"`
		NoCreate bool                 `json:"no_create"`
		Trim     *streams.TrimOptions `json:"trim"`
	}

	body, err := readBody(w, r, db.MaxLine(r.PathValue("coll")))
	if err == nil {
		err = decode(body, &req)
	}

	if err != nil {
		writeError(w, err)

		return
	}

	docs := req.Docs

	if req.Doc != nil {
		docs = append([]map[string]any{req.Doc}, docs...)
	}

	ids, err := s.streams.Append(r.Context(), r.PathValue("coll"), docs, streams.AppendOptions{ID: req.ID, NoCreate: req.NoCreate, Trim: req.Trim})
	if err != nil {
		writeError(w, err)

		return
	}

	s.metrics.writes.Add(int64(len(ids)))

	if req.Doc != nil && len(req.Docs) == 0 {
		writeJSON(w, http.StatusCreated, map[string]string{"_id": ids[0].String()})

		return
	}

	out := make([]string, len(ids))

	for i, id := range ids {
		out[i] = id.String()
	}

	writeJSON(w, http.StatusCreated, map[string][]string{"ids": out})
}

func (s *Server) streamTrim(w http.ResponseWriter, r *http.Request, _ *engine.DB) {
	var req streams.TrimOptions

	if err := streamBody(w, r, &req); err != nil {
		writeError(w, err)

		return
	}

	n, err := s.streams.Trim(r.Context(), r.PathValue("coll"), req)

	answer(w, http.StatusOK, map[string]int{"deleted": n}, err)
}

func (s *Server) streamGroups(w http.ResponseWriter, r *http.Request, _ *engine.DB) {
	groups, err := s.streams.Groups(r.Context(), r.PathValue("coll"))

	answer(w, http.StatusOK, groups, err)
}

func (s *Server) groupCreate(w http.ResponseWriter, r *http.Request, _ *engine.DB) {
	var req struct {
		Start       string `json:"start"`
		EntriesRead *int64 `json:"entries_read"`
		NoCreate    bool   `json:"no_create"`
	}

	if err := streamBody(w, r, &req); err != nil {
		writeError(w, err)

		return
	}

	head, err := s.streams.CreateGroup(r.Context(), r.PathValue("coll"), r.PathValue("group"), req.Start, req.EntriesRead, req.NoCreate)

	answer(w, http.StatusCreated, head, err)
}

func (s *Server) groupDestroy(w http.ResponseWriter, r *http.Request, _ *engine.DB) {
	err := s.streams.DestroyGroup(r.Context(), r.PathValue("coll"), r.PathValue("group"))

	answer(w, http.StatusOK, map[string]bool{"deleted": true}, err)
}

func (s *Server) groupSetID(w http.ResponseWriter, r *http.Request, _ *engine.DB) {
	var req struct {
		ID          string `json:"id"`
		EntriesRead *int64 `json:"entries_read"`
	}

	if err := streamBody(w, r, &req); err != nil {
		writeError(w, err)

		return
	}

	head, err := s.streams.SetID(r.Context(), r.PathValue("coll"), r.PathValue("group"), req.ID, req.EntriesRead)

	answer(w, http.StatusOK, head, err)
}

func (s *Server) groupConsumers(w http.ResponseWriter, r *http.Request, _ *engine.DB) {
	cons, err := s.streams.Consumers(r.Context(), r.PathValue("coll"), r.PathValue("group"))

	answer(w, http.StatusOK, cons, err)
}

func (s *Server) consumerCreate(w http.ResponseWriter, r *http.Request, _ *engine.DB) {
	created, err := s.streams.CreateConsumer(r.Context(), r.PathValue("coll"), r.PathValue("group"), r.PathValue("consumer"))

	answer(w, http.StatusOK, map[string]bool{"created": created}, err)
}

func (s *Server) consumerDelete(w http.ResponseWriter, r *http.Request, _ *engine.DB) {
	n, err := s.streams.DeleteConsumer(r.Context(), r.PathValue("coll"), r.PathValue("group"), r.PathValue("consumer"))

	answer(w, http.StatusOK, map[string]int{"pending": n}, err)
}

func (s *Server) groupRead(w http.ResponseWriter, r *http.Request, _ *engine.DB) {
	var req streams.ReadOptions

	if err := streamBody(w, r, &req); err != nil {
		writeError(w, err)

		return
	}

	ctx, stop := context.WithCancel(r.Context())

	defer stop()

	go func() {
		select {
		case <-s.stopCtx.Done():
			stop()
		case <-ctx.Done():
		}
	}()

	rows, err := s.streams.Read(ctx, r.PathValue("coll"), r.PathValue("group"), req)
	if err != nil {
		writeError(w, err)

		return
	}

	writePage(w, r, collection.Page{Rows: unpadRows(rows), Plan: "stream"})
}

// streamRead is a read without a group (XREAD): it changes nothing.
func (s *Server) streamRead(w http.ResponseWriter, r *http.Request, _ *engine.DB) {
	var req streams.TailOptions

	if err := streamBody(w, r, &req); err != nil {
		writeError(w, err)

		return
	}

	ctx, stop := context.WithCancel(r.Context())

	defer stop()

	go func() {
		select {
		case <-s.stopCtx.Done():
			stop()
		case <-ctx.Done():
		}
	}()

	rows, err := s.streams.Tail(ctx, r.PathValue("coll"), req)
	if err != nil {
		writeError(w, err)

		return
	}

	writePage(w, r, collection.Page{Rows: unpadRows(rows), Plan: "stream"})
}

func (s *Server) groupAck(w http.ResponseWriter, r *http.Request, _ *engine.DB) {
	var req struct {
		IDs []string `json:"ids"`
	}

	if err := streamBody(w, r, &req); err != nil {
		writeError(w, err)

		return
	}

	n, err := s.streams.Ack(r.Context(), r.PathValue("coll"), r.PathValue("group"), req.IDs)

	answer(w, http.StatusOK, map[string]int{"acked": n}, err)
}

func (s *Server) groupPending(w http.ResponseWriter, r *http.Request, _ *engine.DB) {
	var req streams.PendingOptions

	if err := streamBody(w, r, &req); err != nil {
		writeError(w, err)

		return
	}

	out, err := s.streams.Pending(r.Context(), r.PathValue("coll"), r.PathValue("group"), req)

	answer(w, http.StatusOK, out, err)
}

func (s *Server) groupClaim(w http.ResponseWriter, r *http.Request, _ *engine.DB) {
	var req streams.ClaimOptions

	if err := streamBody(w, r, &req); err != nil {
		writeError(w, err)

		return
	}

	res, err := s.streams.Claim(r.Context(), r.PathValue("coll"), r.PathValue("group"), req)

	res.Entries = unpadRows(res.Entries)

	answer(w, http.StatusOK, res, err)
}

func unpadRows(rows []json.RawMessage) []json.RawMessage {
	for i, r := range rows {
		rows[i] = streams.Unpad(r)
	}

	return rows
}

// ---- the routes of every collection, on a stream

// notOnStream refuses a write route of every collection on a stream: an
// entry is added only by an append (it makes the ID and moves the groups),
// it never changes, and it is deleted one by one (DELETE /{s}/_doc/{id})
// or by a trim.
func notOnStream(fn handler) handler {
	return func(w http.ResponseWriter, r *http.Request, db *engine.DB) {
		name := r.PathValue("coll")

		if isStream(db, name) {
			writeError(w, &apiError{status: http.StatusBadRequest, code: "stream_append_only", msg: name + " is a stream: add entries with _streams/append, delete them with DELETE _doc/{id} or _streams/trim"})

			return
		}

		fn(w, r, db)
	}
}

// isStream tells whether name is a stream.
func isStream(db *engine.DB, name string) bool {
	def, ok := db.Definition(name)

	return ok && def.Stream
}

// streamFind wraps a find of a stream: the _id values of the query are
// padded, and the _id of the rows are given back as <ms>-<seq>.
func streamFind(run func(context.Context, []byte) (collection.Page, error)) func(context.Context, []byte) (collection.Page, error) {
	return func(ctx context.Context, body []byte) (collection.Page, error) {
		padded, err := streams.PadQuery(body)
		if err != nil {
			return collection.Page{}, err
		}

		page, err := run(ctx, padded)

		page.Rows = unpadRows(page.Rows)

		return page, err
	}
}

// streamDocID is the _id on disk of the id of a /_doc route on a stream.
func streamDocID(id string) string {
	parsed, err := streams.Parse(id, 0)
	if err != nil {
		return id
	}

	return parsed.Key()
}

// afterTruncate deletes the group store of a stream that was truncated: a
// truncated stream has no groups.
func (s *Server) afterTruncate(db *engine.DB, name string) {
	col, err := db.Get(name)
	if err != nil {
		return
	}

	_ = os.Remove(filepath.Join(col.Dir(), streams.StoreFile))
}
