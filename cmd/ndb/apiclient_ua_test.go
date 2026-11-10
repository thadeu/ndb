package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The CLI names itself in each request, as the SDKs do.
func TestCLISendsItsUserAgent(t *testing.T) {
	got := make(chan string, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("User-Agent")

		w.Write([]byte(`{"status":"ok"}`))
	}))

	defer srv.Close()

	t.Setenv("NDB_URL", srv.URL)

	c, err := newAPIClient(srv.URL)

	if err != nil {
		t.Fatal(err)
	}

	c.alive()

	if ua := <-got; !strings.HasPrefix(ua, "ndb-cli/") {
		t.Fatalf("User-Agent = %q", ua)
	}
}
