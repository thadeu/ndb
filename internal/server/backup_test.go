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

// With backup_tcp and a token, a backup also answers over TCP, for a host
// that reaches the daemon only by the network (Ndb Cloud). Without a token,
// or without a good one, it is refused.
func TestBackupEndpointOverTCPWithBackupTCP(t *testing.T) {
	db, err := engine.Open(config.Config{DataDir: t.TempDir(), AllowImplicit: true}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	defer db.Close()

	post := func(srv *server.Server, token string) (int, string) {
		ts := httptest.NewServer(srv.Handler())

		defer ts.Close()

		req, _ := http.NewRequest("POST", ts.URL+"/_backup", strings.NewReader(`{}`))

		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		defer resp.Body.Close()

		b, _ := io.ReadAll(resp.Body)

		return resp.StatusCode, string(b)
	}

	open := server.New()
	open.SetSecurity(server.Security{BackupTCP: true})
	open.Attach(db)

	if code, body := post(open, ""); code != http.StatusForbidden {
		t.Fatalf("backup_tcp without a token: %d, want 403: %s", code, body)
	}

	srv := server.New()
	srv.SetSecurity(server.Security{BackupTCP: true})
	srv.SetToken("s3cret")
	srv.Attach(db)

	if code, _ := post(srv, "bad"); code != http.StatusUnauthorized {
		t.Fatalf("bad token: %d, want 401", code)
	}

	code, body := post(srv, "s3cret")

	if code != 200 || !strings.HasPrefix(body, `{"ndb_backup":1`) || !strings.Contains(body, `{"_section":"end"`) {
		t.Fatalf("backup over TCP: %d %s", code, body)
	}
}
