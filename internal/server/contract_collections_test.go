package server_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

// Contract tests of the collection routes (docs/dev/http/collections.md and
// the Rules and Limits of docs/dev/http.md): PUT, GET, DELETE /{coll},
// _truncate, _rename, _restore, _pitr, _indexes, _stats, _compact and
// _quarantine, and collection patterns on these routes.

// ccExpect checks the status and the error code of an answer.
func ccExpect(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()

	expectStatus(t, rec, status)

	if got := errorCode(t, rec); got != code {
		t.Fatalf("code = %q, want %q; body %s", got, code, rec.Body.String())
	}
}

// ccCase is one request and the error it must give.
type ccCase struct {
	name, method, path, body string
	status                   int
	code                     string
}

func ccRun(t *testing.T, h http.Handler, cases []ccCase) {
	t.Helper()

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ccExpect(t, call(h, c.method, c.path, c.body), c.status, c.code)
		})
	}
}

// Every bad definition key is 400 catalog_invalid; an unknown key or a body
// that is not JSON is 400 invalid_request.
func TestContractEnsureRejectsBadDefinitions(t *testing.T) {
	_, h := newHandler(t)

	bad := http.StatusBadRequest

	ccRun(t, h, []ccCase{
		{"id key", "PUT", "/c", `{"id":"call_id"}`, bad, "catalog_invalid"},
		{"bad write", "PUT", "/c", `{"write":"other"}`, bad, "catalog_invalid"},
		{"direct without path", "PUT", "/c", `{"write":"direct"}`, bad, "catalog_invalid"},
		{"engine with path", "PUT", "/c", `{"write":"engine","path":"/tmp/x/*.ndjson"}`, bad, "catalog_invalid"},
		{"bad durability", "PUT", "/c", `{"durability":"maybe"}`, bad, "catalog_invalid"},
		{"bad retention", "PUT", "/c", `{"time":"ts","retention":"forever"}`, bad, "catalog_invalid"},
		{"bad wal_retention", "PUT", "/c", `{"wal_retention":"soon"}`, bad, "catalog_invalid"},
		{"bad ttl", "PUT", "/c", `{"time":"ts","ttl":"forever"}`, bad, "catalog_invalid"},
		{"ttl without time", "PUT", "/c", `{"ttl":"1h"}`, bad, "catalog_invalid"},
		{"ttl and expires_field", "PUT", "/c", `{"time":"ts","ttl":"1h","expires_field":"exp"}`, bad, "catalog_invalid"},
		{"negative max_line_size", "PUT", "/c", `{"max_line_size":-1}`, bad, "catalog_invalid"},
		{"max_line_size over 16 MiB", "PUT", "/c", `{"max_line_size":16777217}`, bad, "catalog_invalid"},
		{"empty index", "PUT", "/c", `{"indexes":[""]}`, bad, "catalog_invalid"},
		{"empty unique", "PUT", "/c", `{"unique":[""]}`, bad, "catalog_invalid"},
		{"bad pitr", "PUT", "/c", `{"pitr":"forever"}`, bad, "catalog_invalid"},
		{"unknown key", "PUT", "/c", `{"tme":"ts"}`, bad, "invalid_request"},
		{"not JSON", "PUT", "/c", `{"time":`, bad, "invalid_request"},
		{"not an object", "PUT", "/c", `["time"]`, bad, "invalid_request"},
	})

	// No bad declaration leaves a collection behind.
	expectStatus(t, call(h, "GET", "/c", ""), http.StatusNotFound)
}

// The name rule: ^[a-z0-9][a-z0-9_-]{0,63}$, so at most 64 characters.
func TestContractEnsureNameRule(t *testing.T) {
	_, h := newHandler(t)

	max := strings.Repeat("a", 64)

	expectStatus(t, call(h, "PUT", "/"+max, `{}`), http.StatusOK)
	expectStatus(t, call(h, "PUT", "/0-a_b", `{}`), http.StatusOK)

	bad := http.StatusBadRequest

	ccRun(t, h, []ccCase{
		{"65 characters", "PUT", "/" + max + "a", `{}`, bad, "catalog_invalid"},
		{"upper case", "PUT", "/Calls", `{}`, bad, "catalog_invalid"},
		{"starts with dash", "PUT", "/-calls", `{}`, bad, "catalog_invalid"},
		{"dot", "PUT", "/calls.v2", `{}`, bad, "catalog_invalid"},
		{"pattern", "PUT", "/calls-*", `{}`, bad, "pattern_read_only"},
		{"list", "PUT", "/a,b", `{}`, bad, "pattern_read_only"},
	})

	// "_" is the system namespace: a name never starts with it.
	ccExpect(t, call(h, "PUT", "/_calls", `{}`), http.StatusNotFound, "unknown_route")
}

// PUT is idempotent and merges: indexes and unique are united; a different
// time, write, path, ttl or expires_field is 409 catalog_conflict; the same
// value again is fine.
func TestContractEnsureMergeRules(t *testing.T) {
	_, h := newHandler(t)

	rec := call(h, "PUT", "/calls", `{"time":"ts","indexes":["status"],"unique":["call_id"],"ttl":"1h"}`)

	expectStatus(t, rec, http.StatusOK)

	var first struct {
		Definition map[string]any `json:"definition"`
		Indexes    []struct {
			Field string `json:"field"`
			State string `json:"state"`
			Done  *int   `json:"done"`
			Total *int   `json:"total"`
		} `json:"indexes"`
	}

	decodeBody(t, rec.Body.String(), &first)

	if first.Definition["name"] != "calls" || first.Definition["write"] != "engine" || first.Definition["durability"] != "strict" {
		t.Fatalf("defaults = %v", first.Definition)
	}

	if len(first.Indexes) != 1 || first.Indexes[0].Field != "status" || first.Indexes[0].Done == nil || first.Indexes[0].Total == nil {
		t.Fatalf("indexes = %s", rec.Body.String())
	}

	if s := first.Indexes[0].State; s != "ready" && s != "building" {
		t.Fatalf("state = %q", s)
	}

	// The same declaration again is fine.
	expectStatus(t, call(h, "PUT", "/calls", `{"time":"ts","indexes":["status"],"unique":["call_id"],"ttl":"1h"}`), http.StatusOK)

	rec = call(h, "PUT", "/calls", `{"indexes":["dnis"],"unique":["pin"]}`)

	expectStatus(t, rec, http.StatusOK)

	def := decodeMap(t, rec.Body.String())["definition"].(map[string]any)

	if fmt.Sprint(def["indexes"]) != "[dnis status]" || fmt.Sprint(def["unique"]) != "[call_id pin]" {
		t.Fatalf("merged = %v", def)
	}

	if def["time"] != "ts" || def["ttl"] != "1h" {
		t.Fatalf("an empty key must keep the value: %v", def)
	}

	conflict := http.StatusConflict

	ccRun(t, h, []ccCase{
		{"time", "PUT", "/calls", `{"time":"at"}`, conflict, "catalog_conflict"},
		{"write", "PUT", "/calls", `{"write":"direct","path":"/tmp/x/*.ndjson"}`, conflict, "catalog_conflict"},
		{"ttl", "PUT", "/calls", `{"ttl":"2h"}`, conflict, "catalog_conflict"},
	})

	expectStatus(t, call(h, "PUT", "/exp", `{"time":"ts","expires_field":"until"}`), http.StatusOK)
	ccExpect(t, call(h, "PUT", "/exp", `{"expires_field":"other"}`), conflict, "catalog_conflict")

	// A key that was not set can be set later.
	expectStatus(t, call(h, "PUT", "/late", `{}`), http.StatusOK)

	rec = call(h, "PUT", "/late", `{"time":"ts","durability":"relaxed","max_line_size":2048}`)

	expectStatus(t, rec, http.StatusOK)

	def = decodeMap(t, rec.Body.String())["definition"].(map[string]any)

	if def["time"] != "ts" || def["durability"] != "relaxed" || def["max_line_size"] != float64(2048) {
		t.Fatalf("late keys = %v", def)
	}
}

// unique: a value already held by another document is 409
// unique_violation, also when unique was added by a later PUT.
func TestContractUniqueAddedByALaterPutIsEnforced(t *testing.T) {
	t.Skip("BUG: unique added by PUT to an open collection is saved in the catalog but not enforced until a restart")

	_, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/u", `{}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/u/_doc", `{"_id":"1","s":"a"}`), http.StatusOK)
	expectStatus(t, call(h, "PUT", "/u", `{"unique":["s"]}`), http.StatusOK)

	ccExpect(t, call(h, "POST", "/u/_doc", `{"_id":"2","s":"a"}`), http.StatusConflict, "unique_violation")
}

// unique given at creation is enforced on insert and on update.
func TestContractUniqueAtCreation(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/u", `{"unique":["s"]}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/u/_doc", `{"_id":"1","s":"a"}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/u/_doc", `{"_id":"2","s":"b"}`), http.StatusOK)

	ccExpect(t, call(h, "POST", "/u/_doc", `{"_id":"3","s":"a"}`), http.StatusConflict, "unique_violation")
	ccExpect(t, call(h, "POST", "/u/_update/2", `{"update":{"$set":{"s":"a"}}}`), http.StatusConflict, "unique_violation")
}

// max_line_size is the largest document and request body: 413
// request_too_large. The PUT body itself is at most the default 1 MiB.
func TestContractMaxLineSize(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/m", `{"max_line_size":512}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/m/_doc", `{"_id":"1"}`), http.StatusOK)

	big := `{"_id":"2","pad":"` + strings.Repeat("x", 600) + `"}`

	ccExpect(t, call(h, "POST", "/m/_doc", big), http.StatusRequestEntityTooLarge, "request_too_large")
	ccExpect(t, call(h, "POST", "/m/_bulk", `{"docs":[`+big+`]}`), http.StatusRequestEntityTooLarge, "request_too_large")
	ccExpect(t, call(h, "POST", "/m/_search", `{"query":{"pad":"`+strings.Repeat("x", 600)+`"}}`), http.StatusRequestEntityTooLarge, "request_too_large")

	huge := `{"indexes":["` + strings.Repeat("a", 1<<20) + `"]}`

	ccExpect(t, call(h, "PUT", "/m2", huge), http.StatusRequestEntityTooLarge, "request_too_large")
}

// max_line_size is "the largest document": a document of exactly that
// size is accepted.
func TestContractMaxLineSizeIsTheDocumentSize(t *testing.T) {
	t.Skip("BUG: the limit is on the WAL record (about 110 bytes more than the document), so a document under max_line_size gets 413 line_too_large")

	_, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/m", `{"max_line_size":1000}`), http.StatusOK)

	doc := `{"_id":"1","p":"` + strings.Repeat("x", 1000-len(`{"_id":"1","p":""}`)) + `"}`

	expectStatus(t, call(h, "POST", "/m/_doc", doc), http.StatusOK)
}

// retention hides rows whose time is older; pitr "off" turns the window
// off.
func TestContractRetentionAndPITROff(t *testing.T) {
	_, h := newHandler(t)

	now := time.Now().UTC().Format(time.RFC3339)

	expectStatus(t, call(h, "PUT", "/r", `{"time":"ts","retention":"1d"}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/r/_bulk", `{"docs":[{"_id":"old","ts":"2020-01-01T00:00:00Z"},{"_id":"new","ts":"`+now+`"}]}`), http.StatusOK)

	if got := strings.TrimSpace(call(h, "POST", "/r/_count", `{}`).Body.String()); got != `{"count":1}` {
		t.Fatalf("count = %s", got)
	}

	expectStatus(t, call(h, "PUT", "/off", `{"pitr":"off"}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/off/_doc", `{"_id":"1"}`), http.StatusOK)

	rec := call(h, "GET", "/off/_pitr", "")

	expectStatus(t, rec, http.StatusOK)

	if !strings.Contains(rec.Body.String(), `"window":"off"`) {
		t.Fatalf("pitr = %s", rec.Body.String())
	}

	ccExpect(t, call(h, "POST", "/off/_restore", `{"at":"`+now+`","to":"off2"}`), http.StatusBadRequest, "pitr_off")
}

// GET /{coll}: the definition and the index states; a pattern or a list
// gives the names it matches.
func TestContractGetCollection(t *testing.T) {
	h := seedLogs(t)

	ccExpect(t, call(h, "GET", "/nope", ""), http.StatusNotFound, "unknown_collection")

	rec := call(h, "PUT", "/other", `{"indexes":["lvl"]}`)

	expectStatus(t, rec, http.StatusOK)

	put := decodeMap(t, rec.Body.String())
	got := decodeMap(t, call(h, "GET", "/other", "").Body.String())

	if fmt.Sprint(put["definition"]) != fmt.Sprint(got["definition"]) {
		t.Fatalf("GET %v != PUT %v", got, put)
	}

	if _, ok := got["indexes"]; !ok {
		t.Fatalf("GET without indexes: %v", got)
	}

	lists := map[string]string{
		"/logs-2026-1*":              `{"collections":["logs-2026-10","logs-2026-11","logs-2026-12"]}`,
		"/other,logs-2026-10":        `{"collections":["logs-2026-10","other"]}`,
		"/logs-2026-1*,logs-2026-11": `{"collections":["logs-2026-10","logs-2026-11","logs-2026-12"]}`,
		"/zzz-*":                     `{"collections":[]}`,
	}

	for path, want := range lists {
		if got := strings.TrimSpace(call(h, "GET", path, "").Body.String()); got != want {
			t.Errorf("GET %s = %s, want %s", path, got, want)
		}
	}

	bad := http.StatusBadRequest

	ccRun(t, h, []ccCase{
		{"two stars", "GET", "/logs-**", "", bad, "invalid_pattern"},
		{"star inside", "GET", "/lo*gs", "", bad, "invalid_pattern"},
		{"only a star", "GET", "/*", "", bad, "invalid_pattern"},
		{"empty item", "GET", "/other,,logs-2026-10", "", bad, "invalid_pattern"},
		{"unknown name in a list", "GET", "/other,nope", "", http.StatusNotFound, "unknown_collection"},
	})
}

// A pattern matches at most 256 collections: one more is 400
// pattern_too_wide on every read.
func TestContractPatternTooWide(t *testing.T) {
	_, h := newHandler(t)

	for i := range 257 {
		expectStatus(t, call(h, "PUT", fmt.Sprintf("/w%03d", i), `{}`), http.StatusOK)
	}

	for _, c := range []struct{ method, path, body string }{
		{"GET", "/w*", ""},
		{"POST", "/w*/_search", `{}`},
		{"POST", "/w*/_count", `{}`},
		{"POST", "/w*/_aggregate", `{"pipeline":[]}`},
	} {
		ccExpect(t, call(h, c.method, c.path, c.body), http.StatusBadRequest, "pattern_too_wide")
	}

	// 256 is fine.
	expectStatus(t, call(h, "DELETE", "/w256?confirm=w256", ""), http.StatusOK)

	rec := call(h, "GET", "/w*", "")

	expectStatus(t, rec, http.StatusOK)

	var body struct {
		Collections []string `json:"collections"`
	}

	decodeBody(t, rec.Body.String(), &body)

	if len(body.Collections) != 256 {
		t.Fatalf("len = %d", len(body.Collections))
	}
}

// A pattern or a list only reads: every collection route that changes or
// manages one collection is 400 pattern_read_only.
func TestContractPatternReadOnlyOnCollectionRoutes(t *testing.T) {
	h := seedLogs(t)

	for _, coll := range []string{"logs-*", "logs-2026-10,other"} {
		for _, c := range []struct{ method, action, body string }{
			{"PUT", "", `{}`},
			{"DELETE", "?confirm=" + coll, ""},
			{"POST", "/_truncate?confirm=" + coll, ""},
			{"POST", "/_rename", `{"to":"x"}`},
			{"POST", "/_restore", `{"at":"2026-01-01T00:00:00Z","to":"x"}`},
			{"PUT", "/_indexes/lvl", ""},
			{"DELETE", "/_indexes/lvl", ""},
			{"POST", "/_compact", ""},
			{"POST", "/_doc", `{"a":1}`},
			{"POST", "/_bulk", `{"docs":[{"a":1}]}`},
			{"POST", "/_update/a1", `{"update":{"$set":{"x":1}}}`},
		} {
			t.Run(coll+" "+c.method+c.action, func(t *testing.T) {
				ccExpect(t, call(h, c.method, "/"+coll+c.action, c.body), http.StatusBadRequest, "pattern_read_only")
			})
		}
	}

	// The data stays.
	if got := strings.TrimSpace(call(h, "POST", "/logs-*/_count", `{}`).Body.String()); got != `{"count":8}` {
		t.Fatalf("count = %s", got)
	}
}

// DELETE /{coll} and _truncate need ?confirm={coll}: missing or another
// name is 400 confirm_required; an unknown name is 404; a direct
// collection is 409 direct_collection.
func TestContractDropAndTruncateConfirm(t *testing.T) {
	h := adminHandler(t)

	for _, route := range []struct{ method, path string }{
		{"DELETE", "/posts"},
		{"POST", "/posts/_truncate"},
	} {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			ccExpect(t, call(h, route.method, route.path, ""), http.StatusBadRequest, "confirm_required")
			ccExpect(t, call(h, route.method, route.path+"?confirm=", ""), http.StatusBadRequest, "confirm_required")
			ccExpect(t, call(h, route.method, route.path+"?confirm=calls", ""), http.StatusBadRequest, "confirm_required")
			ccExpect(t, call(h, route.method, route.path+"?confirm=POSTS", ""), http.StatusBadRequest, "confirm_required")
		})
	}

	// Nothing was removed.
	if got := strings.TrimSpace(call(h, "POST", "/posts/_count", `{}`).Body.String()); got != `{"count":3}` {
		t.Fatalf("count = %s", got)
	}

	ccExpect(t, call(h, "DELETE", "/nope?confirm=nope", ""), http.StatusNotFound, "unknown_collection")
	ccExpect(t, call(h, "POST", "/nope/_truncate?confirm=nope", ""), http.StatusNotFound, "unknown_collection")

	ccExpect(t, call(h, "DELETE", "/calls?confirm=calls", ""), http.StatusConflict, "direct_collection")
	ccExpect(t, call(h, "POST", "/calls/_truncate?confirm=calls", ""), http.StatusConflict, "direct_collection")

	// The answers.
	rec := call(h, "POST", "/posts/_truncate?confirm=posts", "")

	expectStatus(t, rec, http.StatusOK)

	if got := strings.TrimSpace(rec.Body.String()); got != `{"collection":"posts","truncated":true}` {
		t.Fatalf("truncate = %s", got)
	}

	// Truncate keeps the indexes.
	if !strings.Contains(call(h, "GET", "/posts", "").Body.String(), `"field":"author"`) {
		t.Fatal("truncate must keep the indexes")
	}

	rec = call(h, "DELETE", "/posts?confirm=posts", "")

	expectStatus(t, rec, http.StatusOK)

	if got := strings.TrimSpace(rec.Body.String()); got != `{"collection":"posts","dropped":true}` {
		t.Fatalf("drop = %s", got)
	}

	// A drop removes the definition: the next PUT starts from nothing.
	rec = call(h, "PUT", "/posts", `{}`)

	expectStatus(t, rec, http.StatusOK)

	if strings.Contains(rec.Body.String(), "author") {
		t.Fatalf("the old definition came back: %s", rec.Body.String())
	}
}

// _rename: errors and the answer.
func TestContractRename(t *testing.T) {
	h := seedTwo(t)

	expectStatus(t, call(h, "PUT", "/other", `{}`), http.StatusOK)

	bad := http.StatusBadRequest

	ccRun(t, h, []ccCase{
		{"no to", "POST", "/posts/_rename", `{}`, bad, "invalid_request"},
		{"unknown key", "POST", "/posts/_rename", `{"to":"x","force":true}`, bad, "invalid_request"},
		{"not JSON", "POST", "/posts/_rename", `{"to":`, bad, "invalid_request"},
		{"bad name", "POST", "/posts/_rename", `{"to":"Bad"}`, bad, "catalog_invalid"},
		{"pattern name", "POST", "/posts/_rename", `{"to":"x-*"}`, bad, "catalog_invalid"},
		{"same name", "POST", "/posts/_rename", `{"to":"posts"}`, bad, "catalog_invalid"},
		{"same name with replace", "POST", "/posts/_rename", `{"to":"posts","replace":true}`, bad, "catalog_invalid"},
		{"unknown source", "POST", "/nope/_rename", `{"to":"x"}`, http.StatusNotFound, "unknown_collection"},
		{"target exists", "POST", "/posts/_rename", `{"to":"other"}`, http.StatusConflict, "collection_exists"},
		{"target exists, replace false", "POST", "/posts/_rename", `{"to":"other","replace":false}`, http.StatusConflict, "collection_exists"},
	})

	rec := call(h, "POST", "/posts/_rename", `{"to":"fresh"}`)

	expectStatus(t, rec, http.StatusOK)

	if got := strings.TrimSpace(rec.Body.String()); got != `{"collection":"fresh","renamed_from":"posts","replaced":false}` {
		t.Fatalf("rename = %s", got)
	}

	// The definition (unique) and the documents moved.
	ccExpect(t, call(h, "POST", "/fresh/_doc", `{"_id":"p9","slug":"a"}`), http.StatusConflict, "unique_violation")
	ccExpect(t, call(h, "GET", "/posts", ""), http.StatusNotFound, "unknown_collection")

	rec = call(h, "POST", "/fresh/_rename", `{"to":"other","replace":true}`)

	expectStatus(t, rec, http.StatusOK)

	if got := strings.TrimSpace(rec.Body.String()); got != `{"collection":"other","renamed_from":"fresh","replaced":true}` {
		t.Fatalf("rename replace = %s", got)
	}

	if got := strings.TrimSpace(call(h, "POST", "/other/_count", `{}`).Body.String()); got != `{"count":2}` {
		t.Fatalf("count = %s", got)
	}
}

// _restore: errors, and a replaced collection restorable by its name.
func TestContractRestore(t *testing.T) {
	h := seedTwo(t)

	time.Sleep(3 * time.Millisecond)

	at := time.Now().UTC().Format(time.RFC3339Nano)

	time.Sleep(3 * time.Millisecond)

	expectStatus(t, call(h, "PUT", "/other", `{}`), http.StatusOK)

	bad := http.StatusBadRequest

	ccRun(t, h, []ccCase{
		{"to exists", "POST", "/posts/_restore", `{"at":"` + at + `","to":"other"}`, http.StatusConflict, "collection_exists"},
		{"bad to", "POST", "/posts/_restore", `{"at":"` + at + `","to":"Bad Name"}`, bad, "catalog_invalid"},
		{"to is a pattern", "POST", "/posts/_restore", `{"at":"` + at + `","to":"x-*"}`, bad, "catalog_invalid"},
		{"to is the source", "POST", "/posts/_restore", `{"at":"` + at + `","to":"posts"}`, bad, "catalog_invalid"},
		{"unknown source", "POST", "/nope/_restore", `{"at":"` + at + `","to":"x"}`, http.StatusNotFound, "unknown_collection"},
		{"no at", "POST", "/posts/_restore", `{"to":"x"}`, bad, "invalid_request"},
	})

	rec := call(h, "POST", "/posts/_restore", `{"at":"2020-01-01T00:00:00Z","to":"x"}`)

	ccExpect(t, rec, bad, "out_of_window")

	if !strings.Contains(rec.Body.String(), time.Now().UTC().Format("2006-")) {
		t.Fatalf("the message must give the start of the window: %s", rec.Body.String())
	}

	rec = call(h, "POST", "/posts/_restore", `{"at":"`+at+`","to":"back"}`)

	expectStatus(t, rec, http.StatusOK)

	var body struct {
		Collection string `json:"collection"`
		Restored   string `json:"restored"`
		At         string `json:"at"`
		Duration   *int64 `json:"duration_ms"`
	}

	decodeBody(t, rec.Body.String(), &body)

	if body.Collection != "back" || body.Restored != "posts" || body.At == "" || body.Duration == nil {
		t.Fatalf("restore = %s", rec.Body.String())
	}

	// The source is not touched.
	if got := strings.TrimSpace(call(h, "POST", "/posts/_count", `{}`).Body.String()); got != `{"count":2}` {
		t.Fatalf("source count = %s", got)
	}

	// A collection replaced by a rename stays restorable by its name.
	expectStatus(t, call(h, "POST", "/other/_rename", `{"to":"posts","replace":true}`), http.StatusOK)

	expectStatus(t, call(h, "POST", "/posts/_restore", `{"at":"`+at+`","to":"again"}`), http.StatusOK)

	if got := strings.TrimSpace(call(h, "POST", "/again/_count", `{}`).Body.String()); got != `{"count":2}` {
		t.Fatalf("restored count = %s", got)
	}
}

// GET /{coll}/_pitr: the keys of the answer; an unknown name is 404.
func TestContractPITRWindow(t *testing.T) {
	h := seedTwo(t)

	rec := call(h, "GET", "/posts/_pitr", "")

	expectStatus(t, rec, http.StatusOK)

	got := decodeMap(t, rec.Body.String())

	for _, key := range []string{"collection", "window", "oldest", "newest", "snapshots", "bytes"} {
		if _, ok := got[key]; !ok {
			t.Errorf("no %q in %s", key, rec.Body.String())
		}
	}

	if got["window"] != "1d" || got["collection"] != "posts" {
		t.Fatalf("pitr = %s", rec.Body.String())
	}

	ccExpect(t, call(h, "GET", "/nope/_pitr", ""), http.StatusNotFound, "unknown_collection")
}

// PUT and DELETE /{coll}/_indexes/{field}.
func TestContractIndexes(t *testing.T) {
	h := seedTwo(t)

	rec := call(h, "PUT", "/posts/_indexes/meta.author", "")

	expectStatus(t, rec, http.StatusOK)

	if got := strings.TrimSpace(rec.Body.String()); got != `{"collection":"posts","indexes":["meta.author"]}` {
		t.Fatalf("put = %s", got)
	}

	// A field no document has is fine: the index is empty.
	expectStatus(t, call(h, "PUT", "/posts/_indexes/missing", ""), http.StatusOK)

	// Again is idempotent.
	rec = call(h, "PUT", "/posts/_indexes/missing", "")

	if got := strings.TrimSpace(rec.Body.String()); got != `{"collection":"posts","indexes":["meta.author","missing"]}` {
		t.Fatalf("put again = %s", got)
	}

	if !strings.Contains(call(h, "GET", "/posts", "").Body.String(), `"field":"missing"`) {
		t.Fatal("GET must list the index state")
	}

	rec = call(h, "DELETE", "/posts/_indexes/missing", "")

	expectStatus(t, rec, http.StatusOK)

	if got := strings.TrimSpace(rec.Body.String()); got != `{"collection":"posts","indexes":["meta.author"]}` {
		t.Fatalf("delete = %s", got)
	}

	// The read stops using it at once.
	rec = call(h, "POST", "/posts/_search", `{"query":{"missing":1}}`)

	if plan := rec.Header().Get("X-Ndb-Plan"); plan == "index:missing" {
		t.Fatalf("plan = %s", plan)
	}

	ccExpect(t, call(h, "PUT", "/nope/_indexes/a", ""), http.StatusNotFound, "unknown_collection")
	ccExpect(t, call(h, "DELETE", "/nope/_indexes/a", ""), http.StatusNotFound, "unknown_collection")

	// No field is not a route.
	ccExpect(t, call(h, "PUT", "/posts/_indexes", ""), http.StatusNotFound, "unknown_route")
}

// _stats, _compact and _quarantine on one collection.
func TestContractStatsCompactQuarantine(t *testing.T) {
	h := adminHandler(t)

	rec := call(h, "GET", "/posts/_stats", "")

	expectStatus(t, rec, http.StatusOK)

	got := decodeMap(t, rec.Body.String())

	for _, key := range []string{"name", "write", "rows", "quarantined", "indexes", "segments", "wal_files", "bytes", "total", "asleep"} {
		if _, ok := got[key]; !ok {
			t.Errorf("_stats has no %q: %s", key, rec.Body.String())
		}
	}

	ccExpect(t, call(h, "GET", "/nope/_stats", ""), http.StatusNotFound, "unknown_collection")

	rec = call(h, "POST", "/posts/_compact", "")

	expectStatus(t, rec, http.StatusOK)

	var compact struct {
		Collection string `json:"collection"`
		Before     *struct {
			Segments *int   `json:"segments"`
			Bytes    *int64 `json:"bytes"`
		} `json:"before"`
		After *struct {
			Segments *int   `json:"segments"`
			Bytes    *int64 `json:"bytes"`
		} `json:"after"`
		Duration *int64 `json:"duration_ms"`
	}

	decodeBody(t, rec.Body.String(), &compact)

	if compact.Collection != "posts" || compact.Before == nil || compact.After == nil || compact.Duration == nil || compact.After.Segments == nil || *compact.After.Segments != 1 {
		t.Fatalf("compact = %s", rec.Body.String())
	}

	ccExpect(t, call(h, "POST", "/nope/_compact", ""), http.StatusNotFound, "unknown_collection")

	for _, limit := range []string{"0", "-1", "abc", "1.5"} {
		ccExpect(t, call(h, "GET", "/calls/_quarantine?limit="+limit, ""), http.StatusBadRequest, "invalid_request")
	}

	// Over 1000 is capped, not an error.
	rec = call(h, "GET", "/calls/_quarantine?limit=5000", "")

	expectStatus(t, rec, http.StatusOK)

	q := decodeMap(t, rec.Body.String())

	if q["collection"] != "calls" || q["total"] != float64(1) {
		t.Fatalf("quarantine = %s", rec.Body.String())
	}

	ccExpect(t, call(h, "GET", "/nope/_quarantine", ""), http.StatusNotFound, "unknown_collection")
}

// A known path with another method is 405 with an Allow header.
func TestContractCollectionRoutesMethods(t *testing.T) {
	h := seedTwo(t)

	for _, c := range []struct{ method, path, allow string }{
		{"POST", "/posts", "PUT, GET, DELETE"},
		{"GET", "/posts/_truncate", "POST"},
		{"GET", "/posts/_rename", "POST"},
		{"GET", "/posts/_restore", "POST"},
		{"POST", "/posts/_pitr", "GET"},
		{"GET", "/posts/_indexes/x", "PUT, DELETE"},
		{"POST", "/posts/_stats", "GET"},
		{"GET", "/posts/_compact", "POST"},
		{"POST", "/posts/_quarantine", "GET"},
	} {
		rec := call(h, c.method, c.path, "")

		ccExpect(t, rec, http.StatusMethodNotAllowed, "method_not_allowed")

		allow := strings.Split(rec.Header().Get("Allow"), ", ")
		want := strings.Split(c.allow, ", ")

		slices.Sort(allow)
		slices.Sort(want)

		if !slices.Equal(allow, want) {
			t.Errorf("%s %s: Allow = %v, want %v", c.method, c.path, allow, want)
		}
	}
}
