package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type setAnswer struct {
	Token     string           `json:"token"`
	ExpiresAt time.Time        `json:"expires_at"`
	Docs      []map[string]any `json:"docs"`
}

func lockSet(t *testing.T, h http.Handler, coll, body string) setAnswer {
	t.Helper()

	rec := call(h, "POST", "/"+coll+"/_lock", body)
	expectStatus(t, rec, http.StatusOK)

	var out setAnswer

	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Token == "" {
		t.Fatalf("lock answer %q: %v", rec.Body.String(), err)
	}

	return out
}

func seedAccounts(t *testing.T, h http.Handler) {
	t.Helper()

	expectStatus(t, call(h, "PUT", "/acc", `{"unique":["email"]}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/acc/_bulk", `{"docs":[{"_id":"a1","balance":100,"email":"a1@x"},{"_id":"a2","balance":0,"email":"a2@x"},{"_id":"a9","balance":5}]}`), http.StatusOK)
}

func TestLockOfASetGivesTheDocumentsAndOneToken(t *testing.T) {
	_, h := newHandler(t)

	seedAccounts(t, h)

	set := lockSet(t, h, "acc", `{"ids":["a2","a1","a2"],"ttl":"30s"}`)

	if len(set.Docs) != 2 || set.Docs[0]["_id"] != "a2" || set.Docs[1]["balance"] != float64(100) || set.Docs[1]["_v"] != float64(1) {
		t.Fatalf("docs %v, want a2 then a1 with _v", set.Docs)
	}

	expectCode(t, call(h, "POST", "/acc/_lock/a1", `{"wait":0}`), http.StatusLocked, "locked")
	expectCode(t, call(h, "POST", "/acc/_lock", `{"ids":["a9","a2"],"wait":0}`), http.StatusLocked, "locked")
	takeLock(t, h, "/acc/_lock/a9", `{"wait":0}`)

	expectCode(t, call(h, "POST", "/acc/_lock", `{"ids":["a1","nope"],"wait":0}`), http.StatusNotFound, "not_found")
	expectCode(t, call(h, "POST", "/acc/_lock", `{"ids":[]}`), http.StatusBadRequest, "invalid_request")
	expectCode(t, call(h, "POST", "/acc/_lock", `{"ids":["a1"],"ttl":"1h"}`), http.StatusBadRequest, "invalid_request")

	many := make([]string, 101)

	for i := range many {
		many[i] = `"x"`
	}

	expectCode(t, call(h, "POST", "/acc/_lock", `{"ids":[`+strings.Join(many, ",")+`]}`), http.StatusBadRequest, "invalid_request")

	rec := call(h, "POST", "/acc/_unlock", `{"token":"`+set.Token+`"}`)
	expectStatus(t, rec, http.StatusOK)

	if !strings.Contains(rec.Body.String(), `"unlocked":2`) {
		t.Fatalf("unlock answer %s, want unlocked 2", rec.Body.String())
	}

	expectCode(t, call(h, "POST", "/acc/_unlock", `{"token":"`+set.Token+`"}`), http.StatusConflict, "lock_expired")
	expectCode(t, call(h, "POST", "/acc/_unlock", `{}`), http.StatusBadRequest, "invalid_request")

	lockSet(t, h, "acc", `{"ids":["a1","a2"],"wait":0}`)
}

func TestCrossedSetsNeverDeadlock(t *testing.T) {
	_, h := newHandler(t)

	seedAccounts(t, h)

	var wg sync.WaitGroup

	for i := range 10 {
		wg.Go(func() {
			ids := `["a1","a2"]`
			if i%2 == 1 {
				ids = `["a2","a1"]`
			}

			set := lockSet(t, h, "acc", `{"ids":`+ids+`,"wait":"10s"}`)

			expectStatus(t, call(h, "POST", "/acc/_tx", `{"ops":[{"op":"update","id":"a1","update":{"$inc":{"balance":-1}}},{"op":"update","id":"a2","update":{"$inc":{"balance":1}}}],"locks":["`+set.Token+`"],"unlock":true}`), http.StatusOK)
		})
	}

	done := make(chan struct{})

	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("crossed sets did not finish")
	}

	if a1, a2 := docOf(t, h, "/acc/_doc/a1"), docOf(t, h, "/acc/_doc/a2"); a1["balance"] != float64(90) || a2["balance"] != float64(10) {
		t.Fatalf("balances %v and %v, want 90 and 10", a1["balance"], a2["balance"])
	}
}

func TestTxWritesEveryOpInOneStep(t *testing.T) {
	_, h := newHandler(t)

	seedAccounts(t, h)

	set := lockSet(t, h, "acc", `{"ids":["a1","a2"]}`)

	rec := call(h, "POST", "/acc/_tx", `{
		"ops":[
			{"op":"update","id":"a1","query":{"_v":1},"update":{"$set":{"balance":60}}},
			{"op":"update","id":"a2","update":{"$inc":{"balance":40}}},
			{"op":"insert","doc":{"_id":"a3","balance":0,"email":"a3@x"}},
			{"op":"update","id":"a3","update":{"$set":{"note":"new"}}},
			{"op":"delete","id":"a9"}
		],
		"locks":["`+set.Token+`"],"unlock":true}`)
	expectStatus(t, rec, http.StatusOK)

	var out struct {
		Results []map[string]any `json:"results"`
	}

	_ = json.Unmarshal(rec.Body.Bytes(), &out)

	want := []string{`a1 2`, `a2 2`, `a3 1`, `a3 2`, `a9 deleted`}

	for i, r := range out.Results {
		got := r["_id"].(string)

		if r["deleted"] == float64(1) {
			got += " deleted"
		} else {
			got += " " + jsonNum(r["_v"])
		}

		if i >= len(want) || got != want[i] {
			t.Fatalf("results %v, want %v", out.Results, want)
		}
	}

	if a1 := docOf(t, h, "/acc/_doc/a1"); a1["balance"] != float64(60) {
		t.Fatalf("a1 %v", a1)
	}

	if a3 := docOf(t, h, "/acc/_doc/a3"); a3["note"] != "new" || a3["_v"] != float64(2) {
		t.Fatalf("a3 %v", a3)
	}

	if a9 := docOf(t, h, "/acc/_doc/a9"); a9 != nil {
		t.Fatalf("a9 was not deleted: %v", a9)
	}

	expectCode(t, call(h, "POST", "/acc/_unlock", `{"token":"`+set.Token+`"}`), http.StatusConflict, "lock_expired")
}

func jsonNum(v any) string {
	b, _ := json.Marshal(v)

	return string(b)
}

func TestAFailingOpWritesNothing(t *testing.T) {
	_, h := newHandler(t)

	seedAccounts(t, h)

	cases := []struct {
		name, ops, code string
		status, index   int
	}{
		{"stale guard", `{"op":"update","id":"a2","query":{"_v":9},"update":{"$inc":{"balance":1}}}`, "tx_conflict", http.StatusConflict, 1},
		{"missing update", `{"op":"update","id":"nope","update":{"$inc":{"balance":1}}}`, "tx_conflict", http.StatusConflict, 1},
		{"missing delete", `{"op":"delete","id":"nope"}`, "tx_conflict", http.StatusConflict, 1},
		{"duplicate id", `{"op":"insert","doc":{"_id":"a2"}}`, "duplicate_id", http.StatusConflict, 1},
		{"unique", `{"op":"insert","doc":{"_id":"b1","email":"a2@x"}}`, "unique_violation", http.StatusConflict, 1},
		{"bad operator", `{"op":"update","id":"a2","update":{"$nope":{"x":1}}}`, "unknown_operator", http.StatusBadRequest, 1},
		{"unknown op", `{"op":"replace","id":"a2"}`, "invalid_request", http.StatusBadRequest, 1},
		{"update without id", `{"op":"update","update":{"$set":{"x":1}}}`, "invalid_request", http.StatusBadRequest, 1},
	}

	for _, c := range cases {
		rec := call(h, "POST", "/acc/_tx", `{"ops":[{"op":"update","id":"a1","update":{"$inc":{"balance":-50}}},`+c.ops+`]}`)

		expectCode(t, rec, c.status, c.code)

		if !strings.Contains(rec.Body.String(), "op 1") {
			t.Fatalf("%s: the error does not name the op: %s", c.name, rec.Body.String())
		}

		if a1 := docOf(t, h, "/acc/_doc/a1"); a1["balance"] != float64(100) || a1["_v"] != float64(1) {
			t.Fatalf("%s: a1 changed: %v", c.name, a1)
		}
	}

	expectCode(t, call(h, "POST", "/acc/_tx", `{"ops":[]}`), http.StatusBadRequest, "invalid_request")
	expectCode(t, call(h, "POST", "/acc/_tx", `{"ops":[{"op":"delete","id":"a1"}],"locks":["stale"]}`), http.StatusConflict, "lock_expired")
	expectCode(t, call(h, "POST", "/nope/_tx", `{"ops":[{"op":"delete","id":"a1"}]}`), http.StatusNotFound, "unknown_collection")

	if a1 := docOf(t, h, "/acc/_doc/a1"); a1 == nil {
		t.Fatal("a1 was deleted with a stale token")
	}
}

func TestTxWaitsForAForeignLockAndExpiredLocksRefuseAll(t *testing.T) {
	_, h := newHandler(t)

	seedAccounts(t, h)

	foreign := takeLock(t, h, "/acc/_lock/a2", ``)
	done := make(chan *httptest.ResponseRecorder, 1)

	go func() {
		done <- call(h, "POST", "/acc/_tx", `{"ops":[{"op":"update","id":"a1","update":{"$inc":{"balance":-1}}},{"op":"update","id":"a2","update":{"$inc":{"balance":1}}}]}`)
	}()

	select {
	case rec := <-done:
		t.Fatalf("the tx did not wait: %d %s", rec.Code, rec.Body.String())
	case <-time.After(50 * time.Millisecond):
	}

	expectStatus(t, call(h, "POST", "/acc/_unlock/a2", `{"token":"`+foreign.Token+`"}`), http.StatusOK)

	select {
	case rec := <-done:
		expectStatus(t, rec, http.StatusOK)
	case <-time.After(5 * time.Second):
		t.Fatal("the tx did not go on after the unlock")
	}

	set := lockSet(t, h, "acc", `{"ids":["a1","a2"],"ttl":0.1}`)

	time.Sleep(150 * time.Millisecond)

	expectCode(t, call(h, "POST", "/acc/_tx", `{"ops":[{"op":"update","id":"a1","update":{"$set":{"balance":0}}}],"locks":["`+set.Token+`"],"unlock":true}`), http.StatusConflict, "lock_expired")

	if a1 := docOf(t, h, "/acc/_doc/a1"); a1["balance"] != float64(99) {
		t.Fatalf("a1 %v, want 99", a1)
	}

	single := takeLock(t, h, "/acc/_lock/a1", ``)
	both := lockSet(t, h, "acc", `{"ids":["a2"]}`)

	expectStatus(t, call(h, "POST", "/acc/_tx", `{"ops":[{"op":"update","id":"a1","update":{"$set":{"balance":1}}},{"op":"update","id":"a2","update":{"$set":{"balance":2}}}],"locks":["`+single.Token+`","`+both.Token+`"],"unlock":true}`), http.StatusOK)

	lockSet(t, h, "acc", `{"ids":["a1","a2"],"wait":0}`)
}

func TestTxRoutesOnOtherCollections(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/events", `{"time":"ts","partition_by":"month"}`), http.StatusOK)
	expectCode(t, call(h, "POST", "/events/_tx", `{"ops":[{"op":"delete","id":"e1"}]}`), http.StatusBadRequest, "not_supported")
	expectCode(t, call(h, "POST", "/events/_lock", `{"ids":["e1"]}`), http.StatusBadRequest, "not_supported")
	expectCode(t, call(h, "POST", "/logs-*/_tx", `{"ops":[{"op":"delete","id":"e1"}]}`), http.StatusBadRequest, "pattern_read_only")
}
