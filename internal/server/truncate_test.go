package server_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestTruncateRoute(t *testing.T) {
	h := seedTwo(t)

	rec := call(h, "POST", "/v1/c/posts/truncate", ``)

	expectStatus(t, rec, http.StatusBadRequest)

	if got := errorCode(t, rec); got != "confirm_required" {
		t.Fatalf("code = %q", got)
	}

	expectStatus(t, call(h, "POST", "/v1/c/posts/truncate?confirm=other", ``), http.StatusBadRequest)
	expectStatus(t, call(h, "POST", "/v1/c/nope/truncate?confirm=nope", ``), http.StatusNotFound)

	rec = call(h, "POST", "/v1/c/posts/truncate?confirm=posts", ``)

	expectStatus(t, rec, http.StatusOK)

	if !strings.Contains(rec.Body.String(), `"truncated":true`) {
		t.Fatalf("body = %s", rec.Body.String())
	}

	rec = call(h, "POST", "/v1/c/posts/find", `{}`)

	if strings.TrimSpace(rec.Body.String()) != "" {
		t.Fatalf("find after truncate = %q", rec.Body.String())
	}

	rec = call(h, "GET", "/v1/c/posts", ``)

	expectStatus(t, rec, http.StatusOK)

	if !strings.Contains(rec.Body.String(), "slug") {
		t.Fatalf("the definition must stay: %s", rec.Body.String())
	}
}

func TestDropRoute(t *testing.T) {
	h := seedTwo(t)

	expectStatus(t, call(h, "DELETE", "/v1/c/posts", ``), http.StatusBadRequest)

	rec := call(h, "DELETE", "/v1/c/posts?confirm=posts", ``)

	expectStatus(t, rec, http.StatusOK)

	if !strings.Contains(rec.Body.String(), `"dropped":true`) {
		t.Fatalf("body = %s", rec.Body.String())
	}

	expectStatus(t, call(h, "GET", "/v1/c/posts", ``), http.StatusNotFound)
	expectStatus(t, call(h, "DELETE", "/v1/c/posts?confirm=posts", ``), http.StatusNotFound)
}
