package ndb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
)

// DefaultReadyTimeout is how long a call retries while the daemon is
// starting or recovering.
const DefaultReadyTimeout = 10 * time.Second

// DefaultMaxConns is the most connections a DB opens to the daemon. Over
// it, a call waits for a free connection. Without a limit, a peak of
// goroutines opened hundreds of connections at once: in the fsw-esl test,
// over a Docker network, calls then took 1 s and 4 s, and a limit of 64
// took the run from 6.2 s to 2.9 s.
const DefaultMaxConns = 64

// DB is a handle to one ndb daemon. It is safe for concurrent use.
type DB struct {
	base   string
	client *http.Client
	token  string

	// watches is the client of _watch: a watch holds its connection for as
	// long as it runs, so it is out of the maxConns of the other calls.
	watches *http.Client

	readyTimeout time.Duration
	maxConns     int
	idleTimeout  time.Duration

	// prefix goes before the name of every Collection (WithPrefix).
	prefix string

	mu      sync.Mutex
	ensured map[string]bool

	// used are the collections used since the last Testing.Clean.
	used map[string]bool
}

// ConnectOption tunes Connect.
type ConnectOption func(*DB)

// WithToken sets the bearer token for the TCP listener. Without it Connect
// reads NDB_TOKEN. The token goes only in the Authorization header.
func WithToken(token string) ConnectOption {
	return func(db *DB) { db.token = token }
}

// WithReadyTimeout sets how long a call retries while the daemon is not
// ready (HTTP 503 not_ready, or the socket refuses or is missing). The
// default is 10 s. Zero turns the retry off.
func WithReadyTimeout(d time.Duration) ConnectOption {
	return func(db *DB) { db.readyTimeout = d }
}

// WithMaxConns sets the most connections the DB opens to the daemon (the
// default is DefaultMaxConns). Over it, a call waits for a free connection,
// until its context ends. All of them are kept open for reuse. Zero is no
// limit.
func WithMaxConns(n int) ConnectOption {
	return func(db *DB) { db.maxConns = n }
}

// WithIdleTimeout sets how long an unused connection stays open. The
// default is 90 s.
func WithIdleTimeout(d time.Duration) ConnectOption {
	return func(db *DB) { db.idleTimeout = d }
}

// Connect returns a handle for addr: unix:///path/to/ndb.sock,
// tcp://host:port, or https://host[:port] (ndb behind a TLS proxy such as
// Caddy; http:// works too). An empty addr reads NDB_URL. It does not dial;
// the first call does, and waits while the daemon starts (WithReadyTimeout).
func Connect(addr string, opts ...ConnectOption) (*DB, error) {
	if addr == "" {
		addr = os.Getenv("NDB_URL")
	}

	u, err := url.Parse(addr)
	if err != nil {
		return nil, fmt.Errorf("ndb: bad address %q: %w", addr, err)
	}

	db := &DB{ensured: map[string]bool{}, token: os.Getenv("NDB_TOKEN"), readyTimeout: DefaultReadyTimeout, maxConns: DefaultMaxConns}

	for _, opt := range opts {
		opt(db)
	}

	tr := http.DefaultTransport.(*http.Transport).Clone()

	tr.MaxConnsPerHost = db.maxConns
	tr.MaxIdleConnsPerHost = max(db.maxConns, 16)
	tr.MaxIdleConns = max(tr.MaxIdleConns, tr.MaxIdleConnsPerHost)

	if db.idleTimeout > 0 {
		tr.IdleConnTimeout = db.idleTimeout
	}

	switch {
	case u.Scheme == "unix" && u.Path != "":
		path := u.Path

		tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer

			return d.DialContext(ctx, "unix", path)
		}

		db.base = "http://ndb"
	case u.Scheme == "tcp" && u.Host != "":
		db.base = "http://" + u.Host
	case (u.Scheme == "https" || u.Scheme == "http") && u.Host != "":
		db.base = u.Scheme + "://" + u.Host
	default:
		return nil, fmt.Errorf("ndb: address %q must be unix:///path, tcp://host:port or https://host", addr)
	}

	db.client = &http.Client{Transport: tr}

	wt := tr.Clone()
	wt.MaxConnsPerHost = 0
	db.watches = &http.Client{Transport: wt}

	return db, nil
}

// Close closes the idle connections. The handle stays usable.
func (db *DB) Close() {
	db.client.CloseIdleConnections()
	db.watches.CloseIdleConnections()
}

// do sends one request and retries it while the daemon is not ready. A
// status of 300 or more becomes an *Error. The caller closes the body of a
// successful response.
func (db *DB) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	deadline := time.Now().Add(db.readyTimeout)
	idempotent := isIdempotent(method, path)
	delay := retryBase

	for {
		resp, err := db.doOnce(ctx, method, path, body)
		if err == nil {
			return resp, nil
		}

		if db.readyTimeout <= 0 || !retryable(err, idempotent) || ctx.Err() != nil {
			return nil, err
		}

		wait := delay/2 + rand.N(delay/2+1)

		if time.Now().Add(wait).After(deadline) {
			return nil, err
		}

		timer := time.NewTimer(wait)

		select {
		case <-ctx.Done():
			timer.Stop()

			return nil, err
		case <-timer.C:
		}

		delay = min(delay*2, retryCap)
	}
}

const (
	retryBase = 50 * time.Millisecond
	retryCap  = time.Second
)

// isIdempotent reports whether repeating the request is harmless: the reads.
func isIdempotent(method, path string) bool {
	if method == http.MethodGet {
		return true
	}

	for _, op := range []string{"/_search", "/_count", "/_aggregate"} {
		if strings.HasSuffix(path, op) {
			return true
		}
	}

	return false
}

// retryable reports whether err means the request was certainly not
// processed (not_ready, a failed connect), or, for a read, may be repeated.
func retryable(err error, idempotent bool) bool {
	var apiErr *Error

	if errors.As(err, &apiErr) {
		return apiErr.Code == ErrNotReady.Code
	}

	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	var opErr *net.OpError

	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNRESET)
	}

	return idempotent && (errors.Is(err, syscall.ECONNRESET) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF))
}

// doOnce sends one request, without retry.
func (db *DB) doOnce(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, db.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	// Rows as NDJSON: the SDK reads them one by one (ndjsonRows).
	req.Header.Set("Accept", "application/x-ndjson")

	db.authorize(req)

	client := db.client

	// A watch and a group read (block_ms) hold the connection while they
	// wait: they do not count against WithMaxConns.
	if strings.HasSuffix(path, "/_watch") || isGroupRead(path) {
		client = db.watches
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < http.StatusMultipleChoices {
		return resp, nil
	}

	return nil, apiError(resp)
}

// authorize adds the bearer token, when the handle has one.
func (db *DB) authorize(req *http.Request) {
	if db.token != "" {
		req.Header.Set("Authorization", "Bearer "+db.token)
	}
}

// decodeBody reads a JSON answer into out (nil: discard) and closes it.
func decodeBody(resp *http.Response, out any) error {
	if out == nil {
		discard(resp)

		return nil
	}

	defer resp.Body.Close()

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("ndb: %s answer: %w", resp.Request.URL.Path, err)
	}

	return nil
}

// ndjsonRows yields each JSON value of r. A value cut short ends the stream
// with an error.
func ndjsonRows(r io.Reader) iter.Seq2[json.RawMessage, error] {
	return func(yield func(json.RawMessage, error) bool) {
		dec := json.NewDecoder(r)

		for {
			var raw json.RawMessage

			err := dec.Decode(&raw)

			if errors.Is(err, io.EOF) {
				return
			}

			if err != nil {
				yield(nil, err)

				return
			}

			if !yield(raw, nil) {
				return
			}
		}
	}
}

// apiError reads the error body of a failed response and closes it.
func apiError(resp *http.Response) error {
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}

	var body struct {
		Status string `json:"status"`
		Error  struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	if json.Unmarshal(raw, &body) == nil && body.Status == "recovering" && resp.StatusCode == http.StatusServiceUnavailable {
		return &Error{Code: ErrNotReady.Code, Message: "ndb is recovering", Status: resp.StatusCode}
	}

	if json.Unmarshal(raw, &body) != nil || body.Error.Code == "" {
		return &Error{Code: "http_status", Message: strings.TrimSpace(string(raw)), Status: resp.StatusCode}
	}

	return &Error{Code: body.Error.Code, Message: body.Error.Message, Status: resp.StatusCode}
}

// discard reads and closes a small response, so the connection can be reused.
func discard(resp *http.Response) {
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

func collPath(coll string) string {
	return "/" + url.PathEscape(coll)
}

// isGroupRead reports whether path is a stream group read.
func isGroupRead(path string) bool {
	return strings.Contains(path, "/_streams/groups/") && strings.HasSuffix(path, "/read")
}
