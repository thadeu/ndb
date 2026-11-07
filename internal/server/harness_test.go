package server_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thadeu/ndb/internal/config"
	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/server"
	"github.com/thadeu/ndb/internal/spec"
)

// unixServer runs a real server on a unix socket and returns an HTTP client
// that dials it. The socket dir is short: macOS caps unix socket paths.
func unixServer(tb testing.TB) *http.Client {
	tb.Helper()

	return unixServerWith(tb, nil)
}

// unixServerWith is unixServer; tune, if set, configures the server before it
// listens.
func unixServerWith(tb testing.TB, tune func(*server.Server)) *http.Client {
	tb.Helper()

	db, err := engine.Open(config.Config{DataDir: tb.TempDir(), AllowImplicit: true}, slog.Default())
	if err != nil {
		tb.Fatal(err)
	}

	sockDir, err := os.MkdirTemp("", "ndbs")
	if err != nil {
		tb.Fatal(err)
	}

	sock := filepath.Join(sockDir, "ndb.sock")

	l, err := net.Listen("unix", sock)
	if err != nil {
		tb.Fatal(err)
	}

	srv := server.New()
	srv.Attach(db)

	if tune != nil {
		tune(srv)
	}

	go srv.Serve(l)

	tb.Cleanup(func() {
		srv.Shutdown(context.Background())
		db.Close()
		os.RemoveAll(sockDir)
	})

	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer

			return d.DialContext(ctx, "unix", sock)
		},
	}}
}

// httpEngine runs the shared spec cases over HTTP.
type httpEngine struct {
	t      *testing.T
	client *http.Client
}

func newHTTPEngine(t *testing.T) spec.Engine {
	return &httpEngine{t: t, client: unixServer(t)}
}

func (e *httpEngine) EnsureCollection(ctx context.Context, name string, def json.RawMessage) error {
	if len(def) == 0 {
		def = json.RawMessage(`{}`)
	}

	_, err := e.send(ctx, "PUT", "/"+name, def)

	return err
}

func (e *httpEngine) Do(ctx context.Context, collection string, step spec.Step) ([]json.RawMessage, error) {
	return e.send(ctx, "POST", "/"+collection+"/_"+step.Op, step.Body)
}

// send returns the rows of a 2xx answer (a JSON array for _search and
// _aggregate, an object for a write), or a CodeError built from the error
// body.
func (e *httpEngine) send(ctx context.Context, method, path string, body []byte) ([]json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://ndb"+path, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		var eb struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}

		if err := json.Unmarshal(data, &eb); err != nil {
			return nil, err
		}

		return nil, &spec.CodeError{Code: eb.Error.Code}
	}

	var rows []json.RawMessage

	if err := json.Unmarshal(data, &rows); err != nil {
		return []json.RawMessage{data}, nil
	}

	return rows, nil
}

func TestSharedCasesOverHTTP(t *testing.T) {
	cases, err := spec.Load("../../spec/cases")
	if err != nil {
		t.Fatal(err)
	}

	if len(cases) == 0 {
		t.Fatal("no shared cases found")
	}

	spec.Run(t, cases, newHTTPEngine)
}
