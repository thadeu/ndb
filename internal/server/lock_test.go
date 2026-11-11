package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type lockAnswer struct {
	Token     string         `json:"token"`
	ExpiresAt time.Time      `json:"expires_at"`
	Doc       map[string]any `json:"doc"`
}

func takeLock(t *testing.T, h http.Handler, path, body string) lockAnswer {
	t.Helper()

	rec := call(h, "POST", path, body)
	expectStatus(t, rec, http.StatusOK)

	var out lockAnswer

	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Token == "" {
		t.Fatalf("lock answer %q: %v", rec.Body.String(), err)
	}

	return out
}

func expectCode(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()

	expectStatus(t, rec, status)

	if got := errorCode(t, rec); got != code {
		t.Fatalf("code %q, want %q", got, code)
	}
}

func TestLockGivesTheDocumentAndOnlyTheHolderWrites(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "POST", "/pay/_bulk", `{"docs":[{"_id":"p1","status":"pending"},{"_id":"p2","status":"pending"}]}`), http.StatusOK)

	lock := takeLock(t, h, "/pay/_lock/p1", `{"ttl":"30s"}`)

	if lock.Doc["status"] != "pending" || lock.Doc["_v"] != float64(1) || time.Until(lock.ExpiresAt) > 31*time.Second {
		t.Fatalf("lock %+v", lock)
	}

	expectCode(t, call(h, "POST", "/pay/_lock/p1", `{"wait":0}`), http.StatusLocked, "locked")
	expectCode(t, call(h, "POST", "/pay/_lock/nope", ``), http.StatusNotFound, "not_found")

	expectStatus(t, call(h, "POST", "/pay/_update/p2", `{"update":{"$set":{"status":"paid"}}}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/pay/_update/p1?lock="+lock.Token, `{"update":{"$set":{"status":"paid"}}}`), http.StatusOK)
	expectCode(t, call(h, "POST", "/pay/_update/p1?lock=stale", `{"update":{"$set":{"status":"x"}}}`), http.StatusConflict, "lock_expired")
	expectCode(t, call(h, "POST", "/pay/_update_by_query", `{"query":{"_id":{"$in":["p1","p2"]}},"update":{"$set":{"n":1}},"many":true}`), http.StatusLocked, "locked")

	expectCode(t, call(h, "POST", "/pay/_unlock/p1", `{"token":"stale"}`), http.StatusConflict, "lock_expired")
	expectStatus(t, call(h, "POST", "/pay/_unlock/p1", `{"token":"`+lock.Token+`"}`), http.StatusOK)
	expectCode(t, call(h, "POST", "/pay/_update/p1?lock="+lock.Token, `{"update":{"$set":{"status":"x"}}}`), http.StatusConflict, "lock_expired")

	again := takeLock(t, h, "/pay/_lock/p1", `{"wait":0}`)

	if again.Doc["status"] != "paid" || again.Token == lock.Token {
		t.Fatalf("second lock %+v", again)
	}
}

func TestAWriteWithoutTheTokenWaitsForTheLock(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "POST", "/pay/_bulk", `{"docs":[{"_id":"p1","n":0}]}`), http.StatusOK)

	lock := takeLock(t, h, "/pay/_lock/p1", ``)
	done := make(chan *httptest.ResponseRecorder, 1)

	go func() {
		done <- call(h, "POST", "/pay/_update/p1", `{"update":{"$inc":{"n":1}}}`)
	}()

	select {
	case rec := <-done:
		t.Fatalf("the write did not wait: %d %s", rec.Code, rec.Body.String())
	case <-time.After(50 * time.Millisecond):
	}

	expectStatus(t, call(h, "POST", "/pay/_update/p1?lock="+lock.Token, `{"update":{"$inc":{"n":10}}}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/pay/_unlock/p1", `{"token":"`+lock.Token+`"}`), http.StatusOK)

	select {
	case rec := <-done:
		expectStatus(t, rec, http.StatusOK)
	case <-time.After(5 * time.Second):
		t.Fatal("the write did not go on after the unlock")
	}

	var doc map[string]any

	_ = json.Unmarshal(call(h, "GET", "/pay/_doc/p1", "").Body.Bytes(), &doc)

	if doc["n"] != float64(11) {
		t.Fatalf("n = %v, want 11", doc["n"])
	}
}

func TestALockExpiresAndAWaiterTakesIt(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "POST", "/pay/_bulk", `{"docs":[{"_id":"p1"}]}`), http.StatusOK)

	old := takeLock(t, h, "/pay/_lock/p1", `{"ttl":0.1}`)
	began := time.Now()
	next := takeLock(t, h, "/pay/_lock/p1", `{"wait":"5s"}`)

	if waited := time.Since(began); waited < 50*time.Millisecond || waited > 2*time.Second {
		t.Fatalf("waited %s for a lock of 100 ms", waited)
	}

	expectCode(t, call(h, "DELETE", "/pay/_doc/p1?lock="+old.Token, ``), http.StatusConflict, "lock_expired")
	expectStatus(t, call(h, "DELETE", "/pay/_doc/p1?lock="+next.Token, ``), http.StatusOK)
}

func TestOnlyOneOfManyLockersIsInAtATime(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "POST", "/pay/_bulk", `{"docs":[{"_id":"p1","n":0}]}`), http.StatusOK)

	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		inside int
		worst  int
	)

	for range 8 {
		wg.Go(func() {
			lock := takeLock(t, h, "/pay/_lock/p1", `{"wait":"10s"}`)

			mu.Lock()
			inside++
			worst = max(worst, inside)
			mu.Unlock()

			n := lock.Doc["n"].(float64) + 1
			body, _ := json.Marshal(map[string]any{"update": map[string]any{"$set": map[string]any{"n": n}}})

			call(h, "POST", "/pay/_update/p1?lock="+lock.Token, string(body))

			mu.Lock()
			inside--
			mu.Unlock()

			call(h, "POST", "/pay/_unlock/p1", `{"token":"`+lock.Token+`"}`)
		})
	}

	wg.Wait()

	var doc map[string]any

	_ = json.Unmarshal(call(h, "GET", "/pay/_doc/p1", "").Body.Bytes(), &doc)

	if worst != 1 || doc["n"] != float64(8) {
		t.Fatalf("%d holders at once, n = %v; want 1 and 8", worst, doc["n"])
	}
}

func TestLockErrorsAndOtherCollections(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "POST", "/pay/_bulk", `{"docs":[{"_id":"p1"}]}`), http.StatusOK)
	expectStatus(t, call(h, "PUT", "/events", `{"time":"ts","partition_by":"month"}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/events/_bulk", `{"docs":[{"_id":"e1","ts":"2026-11-01T00:00:00Z"}]}`), http.StatusOK)

	expectCode(t, call(h, "POST", "/pay/_lock/p1", `{"ttl":"1h"}`), http.StatusBadRequest, "invalid_request")
	expectCode(t, call(h, "POST", "/pay/_lock/p1", `{"ttl":0}`), http.StatusBadRequest, "invalid_request")
	expectCode(t, call(h, "POST", "/pay/_lock/p1", `{"wait":"soon"}`), http.StatusBadRequest, "invalid_request")
	expectCode(t, call(h, "POST", "/pay/_unlock/p1", `{}`), http.StatusBadRequest, "invalid_request")
	expectCode(t, call(h, "POST", "/nope/_lock/p1", ``), http.StatusNotFound, "unknown_collection")

	lock := takeLock(t, h, "/events/_lock/e1", ``)

	expectStatus(t, call(h, "POST", "/events/_update/e1?lock="+lock.Token, `{"update":{"$set":{"n":1}}}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/events/_unlock/e1", `{"token":"`+lock.Token+`"}`), http.StatusOK)
}
