package replication_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/config"
	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/logging"
	"github.com/thadeu/ndb/internal/replication"
	"github.com/thadeu/ndb/internal/server"
)

const (
	testKey   = "0101010101010101010101010101010101010101010101010101010101010101"
	otherKey  = "0202020202020202020202020202020202020202020202020202020202020202"
	appToken  = "app-token"
	replToken = "repl-token"
)

var bg = context.Background()

func tokens() []server.Token {
	return []server.Token{
		{Name: "app", Secret: appToken, Full: true},
		{Name: "repl", Secret: replToken, Replicate: true},
	}
}

// node is one daemon: an engine on a data dir and a server on TCP.
type node struct {
	t    *testing.T
	dir  string
	db   *engine.DB
	srv  *server.Server
	ln   net.Listener
	addr string
	cps  []*replication.Copy
}

func startNode(t *testing.T, dir, key string) *node {
	t.Helper()

	db, err := engine.Open(config.Config{DataDir: dir, AllowImplicit: true, PITR: "off", EncryptionKey: key}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("open %s: %v", dir, err)
	}

	srv := server.New()

	srv.SetTokens(tokens())
	srv.SetReplicationHeartbeat(50 * time.Millisecond)
	srv.SetLogger(quietLogger())

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	n := &node{t: t, dir: dir, db: db, srv: srv, ln: ln, addr: ln.Addr().String()}

	go srv.Serve(ln)

	t.Cleanup(n.stop)

	return n
}

// attach makes the node ready, after its copies are set.
func (n *node) attach() *node {
	n.srv.Attach(n.db)

	return n
}

// copyOf makes n a copy of the database remote of owner, into local.
func (n *node) copyOf(owner *node, remote, local string, opts replication.Options) *replication.Copy {
	n.t.Helper()

	db := n.db

	if local != replication.DefaultDatabase {
		var err error

		if db, err = n.db.Database(local, true); err != nil {
			n.t.Fatal(err)
		}
	}

	src := replication.Source{Base: "http://" + owner.addr, Token: replToken, Remote: remote, Local: local}

	if opts.Silence == 0 {
		opts.Silence = time.Second
	}

	if opts.Backoff == 0 {
		opts.Backoff = 10 * time.Millisecond
		opts.MaxBackoff = 50 * time.Millisecond
	}

	c, err := replication.New(src, db, db.Key().Fingerprint(), slog.New(slog.DiscardHandler), opts)
	if err != nil {
		n.t.Fatal(err)
	}

	n.cps = append(n.cps, c)
	n.srv.SetReplicas([]*replication.Copy{c})

	c.Start(bg)

	return c
}

func (n *node) stop() {
	if n.srv == nil {
		return
	}

	for _, c := range n.cps {
		c.Stop()
	}

	ctx, cancel := context.WithTimeout(bg, 5*time.Second)
	defer cancel()

	_ = n.srv.Shutdown(ctx)

	n.db.Close()
	n.srv = nil
}

// do sends a request with the app token and gives the status, the body and
// the headers.
func (n *node) do(method, path, database, body string) (int, []byte, http.Header) {
	n.t.Helper()

	req, err := http.NewRequest(method, "http://"+n.addr+path, strings.NewReader(body))
	if err != nil {
		n.t.Fatal(err)
	}

	req.Header.Set("Authorization", "Bearer "+appToken)
	req.Header.Set("Content-Type", "application/json")

	if database != "" {
		req.Header.Set("Ndb-Database", database)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		n.t.Fatalf("%s %s: %v", method, path, err)
	}

	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)

	return resp.StatusCode, data, resp.Header
}

// must is do that wants a 2xx.
func (n *node) must(method, path, database, body string) []byte {
	n.t.Helper()

	status, data, _ := n.do(method, path, database, body)

	if status >= 300 {
		n.t.Fatalf("%s %s: %d %s", method, path, status, data)
	}

	return data
}

// docs is every document of a collection of a local database, sorted by
// _id, read on the engine (not through the gate).
func docs(t *testing.T, db *engine.DB, name string) []string {
	t.Helper()

	col, err := db.Get(name)
	if err != nil {
		return nil
	}

	page, err := col.Find(bg, []byte(`{"limit":100000}`))
	if err != nil {
		t.Fatal(err)
	}

	out := make([]string, 0, len(page.Rows))

	for _, row := range page.Rows {
		var buf bytes.Buffer

		_ = json.Compact(&buf, row)

		out = append(out, buf.String())
	}

	sort.Strings(out)

	return out
}

func dbOf(t *testing.T, n *node, name string) *engine.DB {
	t.Helper()

	if name == replication.DefaultDatabase {
		return n.db
	}

	db, err := n.db.Database(name, true)
	if err != nil {
		t.Fatal(err)
	}

	return db
}

// eventually waits until cond is true.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)

	for time.Now().Before(deadline) {
		if cond() {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("timed out: %s", what)
}

// sameData waits until every collection of the owner database has the same
// documents in the copy database.
func sameData(t *testing.T, owner, copyDB *engine.DB) {
	t.Helper()

	eventually(t, "the copy has the data of the owner", func() bool {
		want := map[string]bool{}

		for _, def := range owner.EngineDefs() {
			want[def.Name] = true

			if !equal(docs(t, owner, def.Name), docs(t, copyDB, def.Name)) {
				return false
			}
		}

		for _, def := range copyDB.EngineDefs() {
			if !want[def.Name] {
				return false
			}
		}

		return true
	})
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

func quietLogger() *logging.Logger {
	return logging.New(io.Discard, logging.Options{Level: slog.LevelError})
}
