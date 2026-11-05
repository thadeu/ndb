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
// with an error line (watch_lagged: the consumer must reconnect).
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

	stream, err := db.Watch(name, req.Consumer, req.From)
	if err != nil {
		writeError(w, err)

		return
	}

	defer stream.Close()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	defer context.AfterFunc(s.stopCtx, cancel)()

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)

	s.pump(ctx, w, stream, req.Limit)
}

func validateWatch(req watchReq) error {
	switch {
	case req.Consumer == "":
		return badRequest("consumer is required")
	case req.From != "" && req.From != "beginning" && req.From != "now":
		return badRequest("from must be beginning or now")
	case req.Limit < 0:
		return badRequest("limit must be zero or more")
	}

	return nil
}

// pump writes the events of stream until the client leaves or the stream
// ends. After heartbeat of silence it writes a heartbeat line.
func (s *Server) pump(ctx context.Context, w http.ResponseWriter, stream feed.Stream, limit int) {
	line := lineWriter{w: w, rc: http.NewResponseController(w), enc: json.NewEncoder(w)}

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

	if code == "internal" {
		slog.Error("watch failed", "err", err)

		msg = "internal error"
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

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
