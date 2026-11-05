package server_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thadeu/ndb/internal/config"
	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/server"
)

func TestBackupEndpointOnUnixSocket(t *testing.T) {
	c := unixServer(t)
	dest := filepath.Join(t.TempDir(), "bk")

	do(t, c, "POST", "/v1/c/b/insert", `{"insert":[{"id":"1"}]}`, nil)

	resp, body := do(t, c, "POST", "/v1/backup", `{"dest":"`+dest+`"}`, nil)

	if resp.StatusCode != 200 || !strings.Contains(body, `"b":1`) {
		t.Fatalf("backup: %d %s", resp.StatusCode, body)
	}

	if _, err := os.Stat(filepath.Join(dest, "catalog.bolt")); err != nil {
		t.Fatal(err)
	}

	resp, body = do(t, c, "POST", "/v1/backup", `{"dest":"`+dest+`"}`, nil)

	if resp.StatusCode != 400 {
		t.Fatalf("second backup into the same dir: %d %s", resp.StatusCode, body)
	}

	resp, _ = do(t, c, "POST", "/v1/backup", `{}`, nil)

	if resp.StatusCode != 400 {
		t.Fatalf("missing dest: %d", resp.StatusCode)
	}
}

// A backup writes to the server disk, so only the local socket may ask.
func TestBackupEndpointRefusesTCP(t *testing.T) {
	db, err := engine.Open(config.Config{DataDir: t.TempDir(), AllowImplicit: true}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	defer db.Close()

	srv := server.New()
	srv.Attach(db)

	ts := httptest.NewServer(srv.Handler())

	defer ts.Close()

	resp, err := http.Post(ts.URL+"/v1/backup", "application/json", strings.NewReader(`{"dest":"`+filepath.Join(t.TempDir(), "bk")+`"}`))
	if err != nil {
		t.Fatal(err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 403: %s", resp.StatusCode, b)
	}
}
