package server_test

import (
	"net/http"
	"slices"
	"testing"
)

// A pipeline with no $group runs as a find: the pages of a pipeline $limit
// stop at the limit, over the cursor too.
func TestAggregateWithNoGroupPagesInsideTheLimit(t *testing.T) {
	_, h := newHandler(t)

	seedPosts(t, h, 20)

	rows := walk(t, h, "/posts/_aggregate", `{"pipeline":[{"$sort":{"n":1}},{"$skip":2},{"$limit":7}],"limit":3}`)

	want := []string{"p002", "p003", "p004", "p005", "p006", "p007", "p008"}

	if got := ids(rows); !slices.Equal(got, want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}

	rec := call(h, "POST", "/posts/_aggregate", `{"pipeline":[{"$sort":{"n":1}},{"$skip":2},{"$limit":7}],"limit":3,"count":true}`)

	expectStatus(t, rec, http.StatusOK)

	if got := rec.Header().Get("X-Ndb-Total"); got != "7" {
		t.Fatalf("X-Ndb-Total = %q, want 7", got)
	}
}

// The $skip of the pipeline applies once: the next pages go on from the
// cursor.
func TestAggregateWithNoGroupSkipsOnce(t *testing.T) {
	_, h := newHandler(t)

	seedPosts(t, h, 20)

	body := `{"pipeline":[{"$match":{"k":0}},{"$sort":{"n":-1}},{"$skip":1}],"limit":2`

	rows := walk(t, h, "/posts/_aggregate", body+`}`)

	want := []string{"p010", "p005", "p000"}

	if got := ids(rows); !slices.Equal(got, want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}

	rec := call(h, "POST", "/posts/_aggregate", body+`,"count":true}`)

	if got := rec.Header().Get("X-Ndb-Total"); got != "3" {
		t.Fatalf("X-Ndb-Total = %q, want 3", got)
	}
}

// A request skip past the pipeline $limit gives no row, and the total is
// the limit.
func TestAggregateWithNoGroupSkipPastTheLimit(t *testing.T) {
	_, h := newHandler(t)

	seedPosts(t, h, 20)

	rec := call(h, "POST", "/posts/_aggregate", `{"pipeline":[{"$limit":2}],"skip":5,"count":true}`)

	expectStatus(t, rec, http.StatusOK)

	if rec.Body.Len() != 0 || rec.Header().Get("X-Ndb-Next") != "" {
		t.Fatalf("body = %q, next = %q: want no row", rec.Body.String(), rec.Header().Get("X-Ndb-Next"))
	}

	if got := rec.Header().Get("X-Ndb-Total"); got != "2" {
		t.Fatalf("X-Ndb-Total = %q, want 2", got)
	}
}

func TestAggregateWithNoGroupOverAPattern(t *testing.T) {
	h := seedLogs(t)

	rec := call(h, "POST", "/logs-2026-*/_aggregate", `{"pipeline":[{"$match":{"lvl":"error"}},{"$sort":{"at":1}}]}`)

	expectStatus(t, rec, http.StatusOK)

	rows := rowsOf(t, rec.Body.String())

	if got, want := ids(rows), []string{"b1", "a2", "b3"}; !slices.Equal(got, want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}

	if rows[0].Coll != "logs-2026-11" {
		t.Fatalf("_coll = %q, want logs-2026-11", rows[0].Coll)
	}
}

func TestAggregateWithNoGroupCursorOfAnotherPipeline(t *testing.T) {
	_, h := newHandler(t)

	seedPosts(t, h, 20)

	rec := call(h, "POST", "/posts/_aggregate", `{"pipeline":[{"$limit":5}],"limit":2}`)

	next := rec.Header().Get("X-Ndb-Next")

	if next == "" {
		t.Fatal("X-Ndb-Next missing")
	}

	rec = call(h, "POST", "/posts/_aggregate", `{"pipeline":[{"$limit":6}],"limit":2,"after":"`+next+`"}`)

	expectStatus(t, rec, http.StatusBadRequest)

	if got := errorCode(t, rec); got != "cursor_mismatch" {
		t.Fatalf("code = %q, want cursor_mismatch", got)
	}
}

func TestAggregateWithNoGroupThenLookup(t *testing.T) {
	_, h := newHandler(t)

	call(h, "PUT", "/customers", `{"indexes":["_id"]}`)

	expectStatus(t, call(h, "POST", "/customers/_bulk", `{"docs":[{"_id":"c1","name":"Ana"}]}`), http.StatusOK)

	expectStatus(t, call(h, "POST", "/orders/_bulk", `{"docs":[{"_id":"o1","customer_id":"c1"},{"_id":"o2","customer_id":"c9"}]}`), http.StatusOK)

	body := `{"pipeline":[{"$sort":{"_id":1}},{"$project":{"customer_id":1}},` +
		`{"$lookup":{"from":"customers","localField":"customer_id","foreignField":"_id","as":"customer"}}]}`

	rec := call(h, "POST", "/orders/_aggregate", body)

	expectStatus(t, rec, http.StatusOK)

	want := `{"_id":"o1","customer_id":"c1","customer":[{"_id":"c1","name":"Ana"}]}` + "\n" +
		`{"_id":"o2","customer_id":"c9","customer":[]}` + "\n"

	if rec.Body.String() != want {
		t.Fatalf("body = %q, want %q", rec.Body.String(), want)
	}
}
