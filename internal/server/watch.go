package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/feed"
	"github.com/thadeu/ndb/internal/streams"
)

const (
	defaultHeartbeat = 15 * time.Second

	// writeTimeout bounds one write to a watcher, so a client that stops
	// reading does not hold its stream forever.
	writeTimeout = 30 * time.Second
)

type watchReq struct {
	Consumer string `json:"consumer"`
	From     string `json:"from"`
	Limit    int    `json:"limit"`

	// Query is the optional filter, the query of _search.
	Query json.RawMessage `json:"query"`
}

type ackReq struct {
	Consumer string `json:"consumer"`
	Pos      string `json:"pos"`
}

// SetWatchHeartbeat sets how long a watch stream may be idle before it sends
// {"heartbeat":true} (default 15 s). Call it before Serve.
func (s *Server) SetWatchHeartbeat(d time.Duration) {
	s.heartbeat = d
}

// watch streams the changes of a collection to a consumer, from the position
// of its last ack: first the history, then the live changes. The stream ends
// when the client leaves, after limit events, when the server shuts down, or
// with an error line (watch_lagged: the consumer must reconnect). A where
// filter keeps only the matching events (plan §10.4); a start from a position
// whose WAL files were deleted is history_truncated (410).
func (s *Server) watch(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	name := r.PathValue("coll")

	var req watchReq

	body, err := readBody(w, r, db.MaxLine(name))
	if err != nil {
		writeError(w, err)

		return
	}

	if err := decode(body, &req); err != nil {
		writeError(w, err)

		return
	}

	if err := validateWatch(req); err != nil {
		writeError(w, err)

		return
	}

	if string(req.Query) == "null" {
		req.Query = nil
	}

	stream := isStream(db, name)

	if stream && req.Query != nil {
		if req.Query, err = streams.PadFilter(req.Query); err != nil {
			writeError(w, err)

			return
		}
	}

	if !s.watchStarted() {
		s.tooManyWatches(w)

		return
	}

	defer s.watchEnded()

	events, err := db.WatchWhere(name, req.Consumer, req.From, req.Query)

	if err != nil {
		writeError(w, err)

		return
	}

	defer events.Close()

	longPoll(w)

	if s.debugOn(r) {
		s.log.LogAttrs(r.Context(), slog.LevelDebug, "watch started",
			slog.String("request_id", logOf(w).idOrEmpty()),
			slog.String("coll", name),
			slog.String("consumer", req.Consumer),
			slog.String("from", req.From),
			slog.Int("limit", req.Limit),
			s.body("query", req.Query),
		)
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	defer context.AfterFunc(s.stopCtx, cancel)()

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)

	s.pump(ctx, w, events, req.Limit, stream, req.Consumer)
}

func validateWatch(req watchReq) error {
	switch {
	case req.Consumer == "":
		return badRequest("consumer is required")
	case req.From != "" && req.From != "beginning" && req.From != "now":
		return badRequest("from must be beginning or now")
	case req.Limit < 0:
		return badRequest("limit must be zero or more")
	case len(req.Query) > 0 && req.Query[0] != '{' && string(req.Query) != "null":
		return badRequest("query must be an object")
	}

	return nil
}

// pump writes the events of stream until the client leaves or the stream
// ends. After heartbeat of silence it writes a heartbeat line. On a stream
// collection (unpad), the _id of an event is <ms>-<seq>, not the padded
// _id on disk.
func (s *Server) pump(ctx context.Context, w http.ResponseWriter, stream feed.Stream, limit int, unpad bool, consumer string) {
	line := lineWriter{w: w, rc: http.NewResponseController(w), enc: json.NewEncoder(w), s: s, consumer: consumer}

	if !line.flush() {
		return
	}

	for sent := 0; limit == 0 || sent < limit; sent++ {
		ev, err := s.nextOrBeat(ctx, stream)

		if errors.Is(err, errBeat) {
			if !line.write(map[string]bool{"heartbeat": true}) {
				return
			}

			sent--

			continue
		}

		if err != nil {
			line.fail(ctx, err)

			return
		}

		if unpad {
			ev = unpadEvent(ev)
		}

		if !line.write(ev) {
			return
		}
	}
}

var errBeat = errors.New("heartbeat")

func (s *Server) nextOrBeat(ctx context.Context, stream feed.Stream) (feed.Event, error) {
	idle, cancel := context.WithTimeout(ctx, s.heartbeat)
	defer cancel()

	ev, err := stream.Next(idle)

	if err != nil && ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
		return feed.Event{}, errBeat
	}

	return ev, err
}

// lineWriter writes one NDJSON line per value and flushes it at once.
type lineWriter struct {
	w   http.ResponseWriter
	rc  *http.ResponseController
	enc *json.Encoder

	// s and consumer are for the log lines of a failed stream.
	s        *Server
	consumer string
}

func (l lineWriter) write(v any) bool {
	_ = l.rc.SetWriteDeadline(time.Now().Add(writeTimeout))

	if l.enc.Encode(v) != nil {
		return false
	}

	return l.flush()
}

func (l lineWriter) flush() bool {
	return l.rc.Flush() == nil
}

// fail ends the stream with an error line, unless the client already left.
func (l lineWriter) fail(ctx context.Context, err error) {
	if ctx.Err() != nil || errors.Is(err, io.EOF) {
		return
	}

	code := codeOf(err)
	msg := messageOf(err, code)

	switch code {
	case "internal":
		l.s.log.LogAttrs(ctx, slog.LevelError, "watch failed",
			slog.String("request_id", logOf(l.w).idOrEmpty()),
			slog.String("consumer", l.consumer),
			slog.String("err", err.Error()),
		)

		msg = "internal error"
	case "watch_lagged":
		l.s.logLagged(ctx, l.w, l.consumer, err)
	}

	l.write(map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

// ack stores the position of a consumer: the pos of an event it processed.
func (s *Server) ack(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	name := r.PathValue("coll")

	var req ackReq

	body, err := readBody(w, r, db.MaxLine(name))
	if err != nil {
		writeError(w, err)

		return
	}

	if err := decode(body, &req); err != nil {
		writeError(w, err)

		return
	}

	if req.Consumer == "" || req.Pos == "" {
		writeError(w, badRequest("consumer and pos are required"))

		return
	}

	if err := db.Ack(name, req.Consumer, req.Pos); err != nil {
		writeError(w, err)

		return
	}

	if s.debugOn(r) {
		s.logWrite(w, r, "ack", slog.String("consumer", req.Consumer), slog.String("pos", req.Pos))
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
