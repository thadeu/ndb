package ndb

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// uaRecorder is a fake daemon: it keeps the User-Agent of each request.
type uaRecorder struct {
	mu  sync.Mutex
	got []string
}

func (r *uaRecorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.got = append(r.got, req.Header.Get("User-Agent"))
	r.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"$blob":"sha256:x","size":1}`))
}

func (r *uaRecorder) agents() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]string(nil), r.got...)
}

var sdkAgent = regexp.MustCompile(`^ndb-go/[^ ]+ \(go[^;]+; [a-z0-9]+/[a-z0-9]+\)`)

func TestUserAgentOverTCP(t *testing.T) {
	rec := &uaRecorder{}
	srv := httptest.NewServer(rec)

	t.Cleanup(srv.Close)

	db := openTest(t, "tcp://"+srv.Listener.Addr().String())

	resp, err := db.do(context.Background(), http.MethodGet, "/_health", nil)

	if err != nil {
		t.Fatal(err)
	}

	discard(resp)

	_, err = db.PutBlob(context.Background(), strings.NewReader("x"), "text/plain")

	if err != nil {
		t.Fatal(err)
	}

	got := rec.agents()

	if len(got) != 2 {
		t.Fatalf("requests: got %d, want 2", len(got))
	}

	want := "(" + runtime.Version() + "; " + runtime.GOOS + "/" + runtime.GOARCH + ")"

	for _, ua := range got {
		if !sdkAgent.MatchString(ua) || !strings.HasSuffix(ua, want) {
			t.Fatalf("User-Agent: got %q, want ndb-go/<version> %s", ua, want)
		}
	}
}

func TestUserAgentWithAppTokenOverUnixSocket(t *testing.T) {
	// t.TempDir is too long for a unix socket path on macOS.
	dir, err := os.MkdirTemp("", "ndbua")

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	sock := filepath.Join(dir, "ndb.sock")

	ln, err := net.Listen("unix", sock)

	if err != nil {
		t.Fatal(err)
	}

	rec := &uaRecorder{}
	srv := &http.Server{Handler: rec}

	go func() { _ = srv.Serve(ln) }()

	t.Cleanup(func() { _ = srv.Close() })

	db := openTest(t, "unix://"+sock, WithUserAgent("myapp/1.2"))

	resp, err := db.do(context.Background(), http.MethodGet, "/_health", nil)

	if err != nil {
		t.Fatal(err)
	}

	discard(resp)

	got := rec.agents()

	if len(got) != 1 || !sdkAgent.MatchString(got[0]) || !strings.HasSuffix(got[0], ") myapp/1.2") {
		t.Fatalf("User-Agent: got %q, want ndb-go/<version> (...) myapp/1.2", got)
	}
}

func TestModuleVersion(t *testing.T) {
	cases := map[string]string{"v1.17.2": "1.17.2", "": "dev", "(devel)": "dev", "v0.0.0-2026-abc": "0.0.0-2026-abc"}

	for in, want := range cases {
		if got := moduleVersion(in); got != want {
			t.Errorf("moduleVersion(%q): got %q, want %q", in, got, want)
		}
	}
}
