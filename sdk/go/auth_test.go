package ndb

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

var (
	tokenOnce   sync.Once
	tokenDaemon struct{ unix, tcp string }
)

// extraStops stop the daemons that tests launched; runWithDaemon calls them.
var extraStops []func()

const testToken = "sdk-go-secret"

// startTokenDaemon starts one more daemon whose TCP port wants testToken. The
// TestMain cleanup removes its dir.
func startTokenDaemon(t *testing.T) (unix, tcp string) {
	t.Helper()

	tokenOnce.Do(func() {
		file := filepath.Join(daemon.dir, "token")

		if err := os.WriteFile(file, []byte(testToken+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		stop, err := launchDaemon(daemon.dir, file, false)
		if err != nil {
			t.Fatal(err)
		}

		extraStops = append(extraStops, stop)

		tokenDaemon.unix = lastLaunch.unix
		tokenDaemon.tcp = lastLaunch.tcp
	})

	return tokenDaemon.unix, tokenDaemon.tcp
}

// listCatalog sends GET /v1/c, the cheapest authenticated call.
func listCatalog(db *DB) error {
	resp, err := db.do(context.Background(), http.MethodGet, "/v1/c", nil)
	if err != nil {
		return err
	}

	discard(resp)

	return nil
}

func TestTokenOverTCP(t *testing.T) {
	t.Setenv("NDB_TOKEN", "")

	_, tcp := startTokenDaemon(t)

	cases := []struct {
		name  string
		token string
		want  error
	}{
		{"right token", testToken, nil},
		{"wrong token", "nope", ErrUnauthorized},
		{"missing token", "", ErrUnauthorized},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := openTest(t, tcp, WithToken(c.token))
			err := listCatalog(db)

			if c.want == nil && err != nil {
				t.Fatal(err)
			}

			if c.want != nil && !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
		})
	}
}

func TestTokenFromEnvAndOptionWins(t *testing.T) {
	_, tcp := startTokenDaemon(t)

	t.Setenv("NDB_TOKEN", testToken)

	if err := listCatalog(openTest(t, tcp)); err != nil {
		t.Fatalf("env token: %v", err)
	}

	if err := listCatalog(openTest(t, tcp, WithToken("nope"))); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("explicit token must win over env: %v", err)
	}
}

func TestUnixSocketNeedsNoToken(t *testing.T) {
	t.Setenv("NDB_TOKEN", "")

	unix, _ := startTokenDaemon(t)

	if err := listCatalog(openTest(t, unix)); err != nil {
		t.Fatal(err)
	}
}

func TestBlobUploadSendsToken(t *testing.T) {
	t.Setenv("NDB_TOKEN", "")

	_, tcp := startTokenDaemon(t)

	if _, err := openTest(t, tcp, WithToken(testToken)).PutBlob(context.Background(), bytes.NewReader([]byte("x")), "text/plain"); err != nil {
		t.Fatal(err)
	}

	if _, err := openTest(t, tcp).PutBlob(context.Background(), bytes.NewReader([]byte("x")), "text/plain"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("got %v", err)
	}
}

func TestOpenReadsNDBURL(t *testing.T) {
	t.Setenv("NDB_TOKEN", "")
	t.Setenv("NDB_URL", daemon.tcp)

	if err := listCatalog(openTest(t, "")); err != nil {
		t.Fatalf("NDB_URL tcp: %v", err)
	}

	t.Setenv("NDB_URL", daemon.unix)

	if err := listCatalog(openTest(t, "")); err != nil {
		t.Fatalf("NDB_URL unix: %v", err)
	}

	t.Setenv("NDB_URL", "tcp://127.0.0.1:1")

	if err := listCatalog(openTest(t, daemon.unix)); err != nil {
		t.Fatalf("explicit address must win: %v", err)
	}

	t.Setenv("NDB_URL", "")

	if _, err := Open(""); err == nil {
		t.Fatal("no address and no NDB_URL: want an error")
	}
}
