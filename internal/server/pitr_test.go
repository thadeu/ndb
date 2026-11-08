package server_test

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRestoreAndPITRRoutes(t *testing.T) {
	h := seedTwo(t)

	time.Sleep(3 * time.Millisecond)

	until := time.Now().UTC().Format(time.RFC3339Nano)

	time.Sleep(3 * time.Millisecond)

	expectStatus(t, call(h, "DELETE", "/posts/_doc/p1", ``), http.StatusOK)

	rec := call(h, "POST", "/posts/_restore", `{"until":"`+until+`","into":"posts_back"}`)

	expectStatus(t, rec, http.StatusOK)

	if !strings.Contains(rec.Body.String(), `"restored":"posts"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}

	expectStatus(t, call(h, "GET", "/posts_back/_doc/p1", ``), http.StatusOK)

	cases := []struct {
		path, body, code string
	}{
		{"/posts/_restore", `{"until":"yesterday","into":"x"}`, "invalid_request"},
		{"/posts/_restore", `{"until":"` + until + `"}`, "invalid_request"},
		{"/posts/_restore", `{"until":"2020-01-01T00:00:00Z","into":"x"}`, "out_of_window"},
		{"/posts/_restore", `{"until":"` + until + `","into":"posts_back"}`, "collection_exists"},
		{"/_restore", `{"until":"` + until + `","into":"x"}`, "invalid_request"},
	}

	for _, c := range cases {
		if got := errorCode(t, call(h, "POST", c.path, c.body)); got != c.code {
			t.Errorf("%s %s: code %q, want %q", c.path, c.body, got, c.code)
		}
	}

	expectStatus(t, call(h, "DELETE", "/posts?confirm=posts", ``), http.StatusOK)
	expectStatus(t, call(h, "POST", "/_restore", `{"collection":"posts","until":"`+until+`","into":"posts_again"}`), http.StatusOK)
	expectStatus(t, call(h, "GET", "/posts_again/_doc/p1", ``), http.StatusOK)

	rec = call(h, "GET", "/posts/_pitr", ``)

	expectStatus(t, rec, http.StatusOK)

	if body := rec.Body.String(); !strings.Contains(body, `"window":"1d"`) || !strings.Contains(body, `"dropped":true`) {
		t.Fatalf("pitr = %s", body)
	}

	rec = call(h, "GET", "/_pitr", ``)

	if body := rec.Body.String(); !strings.Contains(body, `"default":"1d"`) || !strings.Contains(body, `"collection":"posts_again"`) {
		t.Fatalf("all pitr = %s", body)
	}
}
