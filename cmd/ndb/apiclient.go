package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"
)

const defaultCallTimeout = 30 * time.Second

// apiClient talks to a running daemon over a unix socket or TCP.
type apiClient struct {
	base  string
	token string
	hc    *http.Client

	// database is the database of the URL, sent as Ndb-Database; "" is the
	// default database.
	database string

	// timeout bounds one call. A compaction raises it.
	timeout time.Duration
}

// apiError is an error answer of the daemon: {"error":{"code","message"}}.
type apiError struct {
	Status  int
	Code    string
	Message string
	Method  string
	Path    string
}

func (e *apiError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("%s %s: HTTP %d %s", e.Method, e.Path, e.Status, e.Message)
	}

	return fmt.Sprintf("%s (%s, HTTP %d)", e.Message, e.Code, e.Status)
}

// reply is the answer of one call.
type reply struct {
	Header http.Header
	Body   []byte
}

// newAPIClient reads unix:///path/to.sock, tcp://host:port,
// tcp+tls://host:port (the same as https), http://host:port or
// https://host[:port]. The token is the one of the URL
// (tcp://:<token>@host:port, as in a Redis URL), else NDB_TOKEN. The path
// names a database (tcp://host:port/billion; ?db=billion on a unix socket).
func newAPIClient(rawURL string) (*apiClient, error) {
	token := os.Getenv("NDB_TOKEN")
	shown := redactURL(rawURL)

	if sock, ok := strings.CutPrefix(rawURL, "unix://"); ok {
		sock, query, _ := strings.Cut(sock, "?")

		if err := checkSocketPath(sock); err != nil {
			return nil, err
		}

		values, err := url.ParseQuery(query)
		if err != nil {
			return nil, fmt.Errorf("url %q: %w", shown, err)
		}

		c := newUnixClient(sock, token)
		c.database = values.Get("db")

		return c, nil
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		var ue *url.Error

		if errors.As(err, &ue) {
			ue.URL = shown
		}

		return nil, fmt.Errorf("url %q: %w", shown, err)
	}

	if (u.Scheme != "tcp" && u.Scheme != "tcp+tls" && u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("url %q: want unix:///path, tcp://host:port, tcp+tls://host:port, http://host:port or https://host", shown)
	}

	if u.User != nil {
		pass, ok := u.User.Password()

		if !ok && u.User.Username() != "" {
			return nil, fmt.Errorf("url %q: put the token after a colon: tcp://:<token>@host:port", shown)
		}

		if pass != "" {
			token = pass
		}
	}

	database, err := urlDatabase(u)
	if err != nil {
		return nil, fmt.Errorf("url %q: %w", shown, err)
	}

	scheme := "http"

	if u.Scheme == "https" || u.Scheme == "tcp+tls" {
		scheme = "https"
	}

	c := &apiClient{base: scheme + "://" + u.Host, token: token, hc: &http.Client{Transport: withUserAgent(http.DefaultTransport)}, timeout: defaultCallTimeout}
	c.database = database

	return c, nil
}

// urlDatabase is the database of a tcp or https URL: its path
// (tcp://host:7070/billion) or the query db. None is the default database.
func urlDatabase(u *url.URL) (string, error) {
	query := u.Query().Get("db")
	path := strings.Trim(u.Path, "/")

	if strings.Contains(path, "/") {
		return "", errors.New("the path names one database: tcp://host:port/<database>")
	}

	if path != "" && query != "" && path != query {
		return "", errors.New("the path and ?db= name two databases")
	}

	if path != "" {
		return path, nil
	}

	return query, nil
}

// authorize adds the token and the database of the client to req.
func (c *apiClient) authorize(req *http.Request) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	if c.database != "" {
		req.Header.Set("Ndb-Database", c.database)
	}
}

// redactURL hides the userinfo (the token) of rawURL, for messages and the
// source line of stats.
func redactURL(rawURL string) string {
	scheme, rest, ok := strings.Cut(rawURL, "://")

	if !ok {
		return rawURL
	}

	authority, path, slash := strings.Cut(rest, "/")
	at := strings.LastIndex(authority, "@")

	if at < 0 {
		return rawURL
	}

	shown := scheme + "://***@" + authority[at+1:]

	if slash {
		shown += "/" + path
	}

	return shown
}

// String shows the address of the client, never its token.
func (c *apiClient) String() string {
	return "apiClient(" + c.base + ")"
}

func newUnixClient(sock, token string) *apiClient {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer

			return d.DialContext(ctx, "unix", sock)
		},
	}

	return &apiClient{base: "http://ndb", token: token, hc: &http.Client{Transport: withUserAgent(tr)}, timeout: defaultCallTimeout}
}

// call sends one request. A status other than 200 is an *apiError.
func (c *apiClient) call(method, path string, body []byte) (reply, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)

	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if err == nil {
		// Rows as NDJSON, one per line: the tables and -json read them so.
		req.Header.Set("Accept", "application/x-ndjson")
	}
	if err != nil {
		return reply{}, err
	}

	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}

	c.authorize(req)

	resp, err := c.hc.Do(req)
	if err != nil {
		return reply{}, err
	}

	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return reply{}, err
	}

	if resp.StatusCode != http.StatusOK {
		return reply{}, parseAPIError(method, path, resp.StatusCode, data)
	}

	return reply{Header: resp.Header, Body: data}, nil
}

// stream sends one request and returns the body as a stream, with no time
// limit: a backup of a large data dir takes as long as it takes.
func (c *apiClient) stream(method, path string, body []byte) (io.ReadCloser, error) {
	req, err := http.NewRequest(method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}

	c.authorize(req)

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusOK {
		return resp.Body, nil
	}

	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)

	return nil, parseAPIError(method, path, resp.StatusCode, data)
}

func parseAPIError(method, path string, status int, data []byte) *apiError {
	e := &apiError{Status: status, Method: method, Path: path, Message: strings.TrimSpace(string(data))}

	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	if json.Unmarshal(data, &body) == nil && body.Error.Code != "" {
		e.Code, e.Message = body.Error.Code, body.Error.Message
	}

	return e
}

// get decodes the JSON answer of GET path into v.
func (c *apiClient) get(path string, v any) error {
	r, err := c.call(http.MethodGet, path, nil)
	if err != nil {
		return err
	}

	return json.Unmarshal(r.Body, v)
}

// post decodes the JSON answer of POST path into v (v may be nil).
func (c *apiClient) post(path string, body []byte, v any) error {
	r, err := c.call(http.MethodPost, path, body)
	if err != nil {
		return err
	}

	if v == nil {
		return nil
	}

	return json.Unmarshal(r.Body, v)
}

// del decodes the JSON answer of DELETE path into v.
func (c *apiClient) del(path string, v any) error {
	r, err := c.call(http.MethodDelete, path, nil)
	if err != nil {
		return err
	}

	return json.Unmarshal(r.Body, v)
}

// alive tells whether anything answers HTTP on the other end. A daemon that
// still recovers (503) is alive.
func (c *apiClient) alive() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)

	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/_health", nil)
	if err != nil {
		return false
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return false
	}

	resp.Body.Close()

	return true
}

var errNoDaemon = errors.New("no running daemon answers")

// userAgent names the CLI in the requests it sends, as the SDKs do:
// ndb-cli/<version> (<go version>; <os>/<arch>).
func userAgent() string {
	return fmt.Sprintf("ndb-cli/%s (%s; %s/%s)", version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

// agentTransport sets the User-Agent of every request of the CLI.
type agentTransport struct {
	next http.RoundTripper
}

func withUserAgent(next http.RoundTripper) http.RoundTripper {
	return agentTransport{next: next}
}

func (t agentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("User-Agent", userAgent())

	return t.next.RoundTrip(req)
}
