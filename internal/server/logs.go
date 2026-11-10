package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/thadeu/ndb/internal/logging"
)

// The request log. Each request gets an id (X-Request-Id), sent back in
// the answer and put on each line of the request. At the end of the
// request there is one line: error for a 5xx, warn for a slow request,
// else debug. A request that persists also writes a debug "write" line
// with what it wrote (logWrite). At info the cost is the id, two clock
// reads and the wrapped writer: no attr is made for a level that is off.

// requestIDMax is the longest X-Request-Id that ndb takes from a client.
const requestIDMax = 128

// userAgentMax is the longest User-Agent on a request line.
const userAgentMax = 256

// writeIDsMax and writeDocsMax limit a write line of a batch: the first
// ids and the first docs, then the counts.
const (
	writeIDsMax  = 100
	writeDocsMax = 3
)

// reqLog wraps the answer of a request: the status, the bytes and what the
// handlers tell the log (the token, the body, the rows, the 5xx error).
type reqLog struct {
	http.ResponseWriter

	status int
	out    int64
	began  time.Time
	id     string
	token  string

	// in and body are the request body that readBody read. body is the
	// same slice (no copy), for the request line: debug, slow or 5xx.
	in   int64
	body []byte

	rows     int
	hasRows  bool
	err      error
	wrote    bool
	longPoll bool
}

func (w *reqLog) WriteHeader(status int) {
	if !w.wrote {
		w.status = status
		w.wrote = true
	}

	w.ResponseWriter.WriteHeader(status)
}

func (w *reqLog) Write(p []byte) (int, error) {
	w.wrote = true

	n, err := w.ResponseWriter.Write(p)

	w.out += int64(n)

	return n, err
}

// Unwrap lets http.ResponseController reach the real writer (flush, deadlines).
func (w *reqLog) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// logOf finds the reqLog under w, or nil (a handler called without observe).
func logOf(w http.ResponseWriter) *reqLog {
	for {
		switch x := w.(type) {
		case *reqLog:
			return x
		case interface{ Unwrap() http.ResponseWriter }:
			w = x.Unwrap()
		default:
			return nil
		}
	}
}

// SetLogger sets the logger of the request lines. Call it before Serve.
func (s *Server) SetLogger(l *logging.Logger) {
	s.log = l
	s.http.ErrorLog = slog.NewLogLogger(l.Handler(), slog.LevelError)
}

// Logger is the logger of the server.
func (s *Server) Logger() *logging.Logger {
	return s.log
}

// observe is the outer wrapper of every request: request id, panics and
// the request line.
func (s *Server) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rl := &reqLog{
			ResponseWriter: w,
			status:         http.StatusOK,
			began:          time.Now(),
			id:             requestID(r.Header.Get("X-Request-Id")),
		}

		w.Header().Set("X-Request-Id", rl.id)

		defer s.recoverPanic(rl, r)

		next.ServeHTTP(rl, r)

		s.logRequest(rl, r)
	})
}

// recoverPanic logs a panic of a handler at error with its stack, and
// answers 500 when nothing was sent yet. A panic after the first byte
// aborts the connection, as net/http does.
func (s *Server) recoverPanic(rl *reqLog, r *http.Request) {
	v := recover()

	if v == nil {
		return
	}

	if v == http.ErrAbortHandler {
		panic(v)
	}

	s.log.LogAttrs(r.Context(), slog.LevelError, "panic",
		slog.String("request_id", rl.id),
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.Any("panic", v),
		slog.String("stack", string(debug.Stack())),
	)

	if rl.wrote {
		panic(http.ErrAbortHandler)
	}

	writeError(rl, fmt.Errorf("panic: %v", v))

	s.logRequest(rl, r)
}

// logRequest writes the line of a finished request at its level.
func (s *Server) logRequest(rl *reqLog, r *http.Request) {
	took := time.Since(rl.began)
	ctx := r.Context()

	level := slog.LevelDebug
	msg := "request"

	switch {
	case rl.status >= 500 && r.URL.Path != "/_health":
		level, msg = slog.LevelError, "request failed"
	case !rl.longPoll && s.log.Slow() > 0 && took > s.log.Slow():
		level, msg = slog.LevelWarn, "slow request"
	}

	if !s.log.Enabled(ctx, level) {
		return
	}

	attrs := make([]slog.Attr, 0, 16)

	attrs = append(attrs,
		slog.String("request_id", rl.id),
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
	)

	if coll := collOf(r.URL.EscapedPath()); coll != "" {
		attrs = append(attrs, slog.String("coll", coll))
	}

	in := rl.in

	if in == 0 && r.ContentLength > 0 {
		in = r.ContentLength
	}

	attrs = append(attrs,
		slog.Int("status", rl.status),
		slog.Float64("ms", float64(took.Microseconds())/1000),
		slog.Int64("bytes_in", in),
		slog.Int64("bytes_out", rl.out),
	)

	if rl.hasRows {
		attrs = append(attrs, slog.Int("rows", rl.rows))
	}

	if rl.token != "" {
		attrs = append(attrs, slog.String("token", rl.token))
	}

	if ua := r.UserAgent(); ua != "" {
		attrs = append(attrs, slog.String("user_agent", cut(ua, userAgentMax)))
	}

	if r.URL.RawQuery != "" {
		attrs = append(attrs, slog.String("query", r.URL.RawQuery))
	}

	if rl.err != nil {
		code := codeOf(rl.err)

		attrs = append(attrs,
			slog.String("code", code),
			slog.String("message", messageOf(rl.err, code)),
		)
	}

	if rl.body != nil {
		attrs = append(attrs, s.log.Body("body", rl.body))
	}

	s.log.LogAttrs(ctx, level, msg, attrs...)
}

// collOf is the collection of a path: the first segment when it does not
// start with "_".
func collOf(escaped string) string {
	first, _, _ := strings.Cut(strings.TrimPrefix(escaped, "/"), "/")

	if first == "" || strings.HasPrefix(first, "_") {
		return ""
	}

	return first
}

// requestID is the X-Request-Id of the client when it is safe (at most
// requestIDMax of A-Z a-z 0-9 - _ . :), else a new id.
func requestID(sent string) string {
	if sent != "" && len(sent) <= requestIDMax && safeID(sent) {
		return sent
	}

	n := idSeq.Add(1)

	buf := make([]byte, 0, len(idPrefix)+14)
	buf = append(buf, idPrefix...)
	buf = strconv.AppendUint(buf, n, 36)

	return string(buf)
}

func safeID(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]

		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == ':'

		if !ok {
			return false
		}
	}

	return true
}

// idPrefix makes the ids of this process differ from the ids of the last.
var (
	idPrefix = newIDPrefix()
	idSeq    atomic.Uint64
)

func newIDPrefix() string {
	b := make([]byte, 4)

	_, _ = rand.Read(b)

	return hex.EncodeToString(b) + "-"
}

// debugOn tells if the debug lines are on. Check it before making attrs.
func (s *Server) debugOn(r *http.Request) bool {
	return s.log.Enabled(r.Context(), slog.LevelDebug)
}

// keepBody keeps the request body for the request line. It does not
// copy: the handlers do not change the body.
func keepBody(w http.ResponseWriter, body []byte) {
	if rl := logOf(w); rl != nil {
		rl.in, rl.body = int64(len(body)), body
	}
}

// noteRows puts the row count of a read on the request line.
func noteRows(w http.ResponseWriter, n int) {
	if rl := logOf(w); rl != nil {
		rl.rows, rl.hasRows = n, true
	}
}

// noteError keeps the error of an answer for the request line.
func noteError(w http.ResponseWriter, err error) {
	if rl := logOf(w); rl != nil {
		rl.err = err
	}
}

// noteToken puts the name of the token (never its secret) on the lines.
func noteToken(w http.ResponseWriter, name string) {
	if rl := logOf(w); rl != nil {
		rl.token = name
	}
}

// longPoll marks a request whose time the client picks (a watch, a read
// that blocks): it is never a slow request.
func longPoll(w http.ResponseWriter) {
	if rl := logOf(w); rl != nil {
		rl.longPoll = true
	}
}

// logWrite writes the debug line of a request that persisted: op, the
// collection, attrs (ids, _v, the documents) and the request id. The
// request line then leaves out the body, as this line has it. Call it
// only when debugOn is true.
func (s *Server) logWrite(w http.ResponseWriter, r *http.Request, op string, attrs ...slog.Attr) {
	all := make([]slog.Attr, 0, len(attrs)+3)

	rl := logOf(w)

	if rl != nil {
		all = append(all, slog.String("request_id", rl.id))
	}

	all = append(all, slog.String("op", op))

	if coll := r.PathValue("coll"); coll != "" {
		all = append(all, slog.String("coll", coll))
	}

	all = append(all, attrs...)

	if rl != nil {
		rl.body = nil
	}

	s.log.LogAttrs(r.Context(), slog.LevelDebug, "write", all...)
}

// logInfo writes an info line of a request (a collection made, dropped,
// renamed...) with its request id.
func (s *Server) logInfo(w http.ResponseWriter, r *http.Request, msg string, attrs ...slog.Attr) {
	if !s.log.Enabled(r.Context(), slog.LevelInfo) {
		return
	}

	all := make([]slog.Attr, 0, len(attrs)+2)

	if rl := logOf(w); rl != nil {
		all = append(all, slog.String("request_id", rl.id))
	}

	if coll := r.PathValue("coll"); coll != "" {
		all = append(all, slog.String("coll", coll))
	}

	all = append(all, attrs...)

	s.log.LogAttrs(r.Context(), slog.LevelInfo, msg, all...)
}

// body is the attr of one body or document, cut to log_body_max.
func (s *Server) body(key string, b []byte) slog.Attr {
	return s.log.Body(key, b)
}

// docsAttr is the first writeDocsMax documents of a batch, each cut.
func (s *Server) docsAttr(docs [][]byte) slog.Attr {
	if s.log.BodyMax() <= 0 {
		return slog.Attr{}
	}

	n := min(len(docs), writeDocsMax)
	out := make([]string, n)

	for i := range n {
		out[i] = logging.Cut(docs[i], s.log.BodyMax())
	}

	return slog.Any("docs", out)
}

// resultsAttrs are the ids and _v of a batch (the first writeIDsMax) and
// the count.
func resultsAttrs(res []writeResult) []slog.Attr {
	n := min(len(res), writeIDsMax)
	ids := make([]string, n)
	vs := make([]uint64, n)

	for i := range n {
		ids[i], vs[i] = res[i].ID, res[i].V
	}

	return []slog.Attr{slog.Int("count", len(res)), slog.Any("ids", ids), slog.Any("_v", vs)}
}

// logReq is the body of PUT /_log. Each key is optional.
type logReq struct {
	Level   *string `json:"level"`
	BodyMax *int    `json:"body_max"`
	SlowMs  *int    `json:"slow_ms"`
}

// logState is GET /_log and the answer of PUT /_log.
func (s *Server) logState() map[string]any {
	return map[string]any{
		"level":    logging.LevelName(s.log.Level()),
		"format":   s.log.Format(),
		"body_max": s.log.BodyMax(),
		"slow_ms":  s.log.SlowMs(),
	}
}

func (s *Server) getLog(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.logState())
}

// putLog changes the log settings at run time, without a restart. The
// format stays the one of the start.
func (s *Server) putLog(w http.ResponseWriter, r *http.Request) {
	var req logReq

	body, err := readBody(w, r, 4096)

	if err == nil {
		err = decode(body, &req)
	}

	if err != nil {
		writeError(w, err)

		return
	}

	level := s.log.Level()

	if req.Level != nil {
		if level, err = logging.ParseLevel(*req.Level); err != nil || *req.Level == "" {
			writeError(w, badRequest("level must be debug, info, warn, error or quiet"))

			return
		}
	}

	if req.BodyMax != nil && *req.BodyMax < 0 || req.SlowMs != nil && *req.SlowMs < 0 {
		writeError(w, badRequest("body_max and slow_ms must be zero or more"))

		return
	}

	before := logging.LevelName(s.log.Level())

	s.log.SetLevel(level)

	if req.BodyMax != nil {
		s.log.SetBodyMax(*req.BodyMax)
	}

	if req.SlowMs != nil {
		s.log.SetSlowMs(*req.SlowMs)
	}

	s.logInfo(w, r, "log settings changed",
		slog.String("from", before),
		slog.String("level", logging.LevelName(level)),
		slog.Int("body_max", s.log.BodyMax()),
		slog.Int("slow_ms", s.log.SlowMs()),
	)

	writeJSON(w, http.StatusOK, s.logState())
}

// logLagged writes the warn line of a watch consumer that fell behind.
func (s *Server) logLagged(ctx context.Context, w http.ResponseWriter, consumer string, err error) {
	if !s.log.Enabled(ctx, slog.LevelWarn) {
		return
	}

	id := ""

	if rl := logOf(w); rl != nil {
		id = rl.id
	}

	s.log.LogAttrs(ctx, slog.LevelWarn, "watch consumer lagged", slog.String("request_id", id), slog.String("consumer", consumer), slog.String("err", err.Error()))
}

// idOrEmpty is the request id, or "" without a reqLog.
func (w *reqLog) idOrEmpty() string {
	if w == nil {
		return ""
	}

	return w.id
}

// firstIDs is the first writeIDsMax stream IDs as text.
func firstIDs[T fmt.Stringer](ids []T) []string {
	out := make([]string, min(len(ids), writeIDsMax))

	for i := range out {
		out[i] = ids[i].String()
	}

	return out
}
