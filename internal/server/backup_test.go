package server_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thadeu/ndb/internal/config"
	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/server"
)

func TestBackupEndpointStreamsTheFileOnUnixSocket(t *testing.T) {
	c := unixServer(t)

	do(t, c, "POST", "/b/_bulk", `{"docs":[{"_id":"1"}]}`, nil)

	resp, body := do(t, c, "POST", "/_backup", `{}`, nil)

	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("backup: %d %s", resp.StatusCode, body)
	}

	lines := strings.Split(strings.TrimSpace(body), "\n")

	if !strings.HasPrefix(lines[0], `{"ndb_backup":1`) || !strings.HasPrefix(lines[len(lines)-1], `{"_section":"end"`) {
		t.Fatalf("not a backup file:\n%s", body)
	}

	if !strings.Contains(body, `{"_section":"wal","collection":"b"}`) || !strings.Contains(body, `"_id":"1"`) {
		t.Fatalf("the document is not in the file:\n%s", body)
	}

	resp, _ = do(t, c, "POST", "/_backup", `{"nope":1}`, nil)

	if resp.StatusCode != 400 {
		t.Fatalf("unknown field: %d", resp.StatusCode)
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

	resp, err := http.Post(ts.URL+"/_backup", "application/json", strings.NewReader(`{"dest":"`+filepath.Join(t.TempDir(), "bk")+`"}`))
	if err != nil {
		t.Fatal(err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 403: %s", resp.StatusCode, b)
	}
}
