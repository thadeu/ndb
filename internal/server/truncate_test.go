package server_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestTruncateRoute(t *testing.T) {
	h := seedTwo(t)

	rec := call(h, "POST", "/posts/_truncate", ``)

	expectStatus(t, rec, http.StatusBadRequest)

	if got := errorCode(t, rec); got != "confirm_required" {
		t.Fatalf("code = %q", got)
	}

	expectStatus(t, call(h, "POST", "/posts/_truncate?confirm=other", ``), http.StatusBadRequest)
	expectStatus(t, call(h, "POST", "/nope/_truncate?confirm=nope", ``), http.StatusNotFound)

	rec = call(h, "POST", "/posts/_truncate?confirm=posts", ``)

	expectStatus(t, rec, http.StatusOK)

	if !strings.Contains(rec.Body.String(), `"truncated":true`) {
		t.Fatalf("body = %s", rec.Body.String())
	}

	rec = call(h, "POST", "/posts/_search", `{}`)

	if strings.TrimSpace(rec.Body.String()) != "" {
		t.Fatalf("find after truncate = %q", rec.Body.String())
	}

	rec = call(h, "GET", "/posts", ``)

	expectStatus(t, rec, http.StatusOK)

	if !strings.Contains(rec.Body.String(), "slug") {
		t.Fatalf("the definition must stay: %s", rec.Body.String())
	}
}

func TestDropRoute(t *testing.T) {
	h := seedTwo(t)

	expectStatus(t, call(h, "DELETE", "/posts", ``), http.StatusBadRequest)

	rec := call(h, "DELETE", "/posts?confirm=posts", ``)

	expectStatus(t, rec, http.StatusOK)

	if !strings.Contains(rec.Body.String(), `"dropped":true`) {
		t.Fatalf("body = %s", rec.Body.String())
	}

	expectStatus(t, call(h, "GET", "/posts", ``), http.StatusNotFound)
	expectStatus(t, call(h, "DELETE", "/posts?confirm=posts", ``), http.StatusNotFound)
}

func TestRenameRoute(t *testing.T) {
	h := seedTwo(t)

	expectStatus(t, call(h, "PUT", "/other", `{}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/posts/_rename", `{}`), http.StatusBadRequest)
	expectStatus(t, call(h, "POST", "/nope/_rename", `{"to":"x"}`), http.StatusNotFound)

	rec := call(h, "POST", "/posts/_rename", `{"to":"other"}`)

	expectStatus(t, rec, http.StatusConflict)

	if got := errorCode(t, rec); got != "collection_exists" {
		t.Fatalf("code = %q", got)
	}

	rec = call(h, "POST", "/posts/_rename", `{"to":"Bad Name"}`)

	if got := errorCode(t, rec); got != "catalog_invalid" {
		t.Fatalf("code = %q", got)
	}

	expectStatus(t, call(h, "POST", "/posts/_rename", `{"to":"other","replace":true}`), http.StatusOK)
	expectStatus(t, call(h, "GET", "/posts", ``), http.StatusNotFound)

	rec = call(h, "GET", "/other/_doc/p1", ``)

	expectStatus(t, rec, http.StatusOK)
}
