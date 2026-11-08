package ndb

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

// An https:// address talks to ndb behind a TLS proxy: the same calls, over
// TLS, with the token.
func TestConnectHTTPS(t *testing.T) {
	var gotAuth string

	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer ts.Close()

	db, err := Connect(ts.URL, WithToken("t0k"))
	if err != nil {
		t.Fatal(err)
	}

	db.client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: ts.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs}

	if err := db.Health(context.Background()); err != nil {
		t.Fatal(err)
	}

	if gotAuth != "Bearer t0k" {
		t.Fatalf("authorization = %q", gotAuth)
	}

	for _, bad := range []string{"https://", "ftp://h", "h:1"} {
		if _, err := Connect(bad); err == nil {
			t.Fatalf("%q connected", bad)
		}
	}
}
