package server_test

import (
	"context"
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
)

// tokenServer serves one server with a token on both a unix socket and a TCP
// port, and returns a client for each.
func tokenServer(t *testing.T, token string) (unix, tcp *http.Client, tcpAddr string) {
	t.Helper()

	db, err := engine.Open(config.Config{DataDir: t.TempDir(), AllowImplicit: true}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	sockDir, err := os.MkdirTemp("", "ndbs")
	if err != nil {
		t.Fatal(err)
	}

	sock := filepath.Join(sockDir, "ndb.sock")

	ul, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}

	tl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	srv := server.New()
	srv.SetToken(token)
	srv.Attach(db)

	go srv.Serve(ul)
	go srv.Serve(tl)

	t.Cleanup(func() {
		srv.Shutdown(context.Background())
		db.Close()
		os.RemoveAll(sockDir)
	})

	unix = &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer

			return d.DialContext(ctx, "unix", sock)
		},
	}}

	return unix, &http.Client{}, tl.Addr().String()
}

func TestTokenRequiredOnTCP(t *testing.T) {
	_, tcp, addr := tokenServer(t, "s3cret")
	url := "http://" + addr

	cases := []struct {
		name string
		auth string
		want int
	}{
		{"missing", "", 401},
		{"wrong", "Bearer nope", 401},
		{"wrong scheme", "Basic s3cret", 401},
		{"right", "Bearer s3cret", 200},
	}

	for _, c := range cases {
		req, _ := http.NewRequest("GET", url+"/v1/c", nil)

		if c.auth != "" {
			req.Header.Set("Authorization", c.auth)
		}

		resp, err := tcp.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		var body strings.Builder

		buf := make([]byte, 512)
		n, _ := resp.Body.Read(buf)
		body.Write(buf[:n])
		resp.Body.Close()

		if resp.StatusCode != c.want {
			t.Fatalf("%s: status %d, want %d: %s", c.name, resp.StatusCode, c.want, body.String())
		}

		if c.want == 401 && !strings.Contains(body.String(), `"code":"unauthorized"`) {
			t.Fatalf("%s: body %s", c.name, body.String())
		}
	}
}

func TestTokenNotRequiredOnUnixSocket(t *testing.T) {
	unix, _, _ := tokenServer(t, "s3cret")

	resp, body := do(t, unix, "GET", "/v1/c", "", nil)

	if resp.StatusCode != 200 {
		t.Fatalf("unix without token: %d %s", resp.StatusCode, body)
	}
}

func TestBackupStillForbiddenOverTCPWithToken(t *testing.T) {
	_, tcp, addr := tokenServer(t, "s3cret")

	req, _ := http.NewRequest("POST", "http://"+addr+"/v1/backup", strings.NewReader(`{"dest":"`+filepath.Join(t.TempDir(), "bk")+`"}`))
	req.Header.Set("Authorization", "Bearer s3cret")

	resp, err := tcp.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403", resp.StatusCode)
	}
}
