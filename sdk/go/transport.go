package ndb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// DB is a handle to one ndb daemon. It is safe for concurrent use.
type DB struct {
	base   string
	client *http.Client

	mu      sync.Mutex
	ensured map[string]bool
}

// Open returns a handle for addr: unix:///path/to/ndb.sock or
// tcp://host:port. It does not connect; the first call does.
func Open(addr string) (*DB, error) {
	u, err := url.Parse(addr)
	if err != nil {
		return nil, fmt.Errorf("ndb: bad address %q: %w", addr, err)
	}

	db := &DB{ensured: map[string]bool{}}
	tr := http.DefaultTransport.(*http.Transport).Clone()

	tr.MaxIdleConnsPerHost = 16

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
	default:
		return nil, fmt.Errorf("ndb: address %q must be unix:///path or tcp://host:port", addr)
	}

	db.client = &http.Client{Transport: tr}

	return db, nil
}

// Close closes the idle connections. The handle stays usable.
func (db *DB) Close() {
	db.client.CloseIdleConnections()
}

// do sends one request. A status of 300 or more becomes an *Error. The
// caller closes the body of a successful response.
func (db *DB) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, db.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := db.client.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < http.StatusMultipleChoices {
		return resp, nil
	}

	return nil, apiError(resp)
}

func (db *DB) find(ctx context.Context, coll string, body []byte) (*http.Response, error) {
	return db.do(ctx, http.MethodPost, collPath(coll)+"/find", body)
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
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
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
	return "/v1/c/" + url.PathEscape(coll)
}
