package server_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestCompoundIndexOverHTTP(t *testing.T) {
	_, h := newHandler(t)

	rec := call(h, "PUT", "/orders", `{"indexes":["acc,n"],"unique":["acc,code"]}`)
	expectStatus(t, rec, http.StatusOK)

	if !strings.Contains(rec.Body.String(), `"acc,n"`) || !strings.Contains(rec.Body.String(), `"acc,code"`) {
		t.Fatalf("definition %s", rec.Body.String())
	}

	expectStatus(t, call(h, "POST", "/orders/_bulk", `{"docs":[
		{"_id":"o1","acc":"a","n":1,"code":"x"},
		{"_id":"o2","acc":"a","n":2,"code":"y"},
		{"_id":"o3","acc":"b","n":1,"code":"x"}]}`), http.StatusOK)

	waitIndexReady(t, h, "orders")

	if plan := planOf(t, h, "/orders/_search", `{"query":{"acc":"a","n":{"$gte":2}},"explain":true}`); plan != "index:acc,n" {
		t.Fatalf("plan %q, want index:acc,n", plan)
	}

	expectCode(t, call(h, "POST", "/orders/_doc", `{"_id":"o4","acc":"a","code":"x"}`), http.StatusConflict, "unique_violation")
	expectCode(t, call(h, "POST", "/orders/_update/o2", `{"update":{"$set":{"code":"x"}}}`), http.StatusConflict, "unique_violation")
	expectCode(t, call(h, "POST", "/orders/_tx", `{"ops":[{"op":"insert","doc":{"_id":"o5","acc":"b","code":"z"}},{"op":"update","id":"o5","update":{"$set":{"code":"x"}}}]}`), http.StatusConflict, "unique_violation")
	expectStatus(t, call(h, "POST", "/orders/_doc", `{"_id":"o6","acc":"c","code":"x"}`), http.StatusOK)

	expectStatus(t, call(h, "PUT", "/orders/_indexes/code,acc", ``), http.StatusOK)
	waitIndexReady(t, h, "orders")

	if plan := planOf(t, h, "/orders/_search", `{"query":{"code":"x","acc":{"$in":["a","b"]}},"explain":true}`); plan != "index:code,acc" && plan != "index:acc,n" {
		t.Fatalf("plan %q", plan)
	}

	expectStatus(t, call(h, "DELETE", "/orders/_indexes/code,acc", ``), http.StatusOK)

	if body := call(h, "GET", "/orders/_stats", "").Body.String(); strings.Contains(body, "code,acc") {
		t.Fatalf("the index is still in the stats: %s", body)
	}
}

func TestCompoundIndexErrorsOverHTTP(t *testing.T) {
	_, h := newHandler(t)

	for _, body := range []string{`{"indexes":["a,"]}`, `{"indexes":["a,a"]}`, `{"unique":["a,b,c,d,e"]}`, `{"indexes":["a,_id"]}`} {
		expectCode(t, call(h, "PUT", "/bad", body), http.StatusBadRequest, "catalog_invalid")
	}

	expectStatus(t, call(h, "PUT", "/users", `{}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/users/_bulk", `{"docs":[{"_id":"u1","acc":"a","email":"x"},{"_id":"u2","acc":"a","email":"x"}]}`), http.StatusOK)

	rec := call(h, "PUT", "/users", `{"unique":["acc,email"]}`)

	if rec.Code == http.StatusOK {
		t.Fatalf("a compound unique key was added on data that has the tuple twice: %s", rec.Body.String())
	}

	expectStatus(t, call(h, "PUT", "/users/_indexes/a,a", ``), http.StatusBadRequest)
}

func TestLookupUsesACompoundIndex(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/items", `{"indexes":["order_id,pos"]}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/items/_bulk", `{"docs":[{"_id":"i1","order_id":"o1","pos":1},{"_id":"i2","order_id":"o1","pos":2},{"_id":"i3","order_id":"o2","pos":1}]}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/orders/_bulk", `{"docs":[{"_id":"o1"},{"_id":"o2"}]}`), http.StatusOK)

	waitIndexReady(t, h, "items")

	rec := call(h, "POST", "/orders/_search", `{"query":{"_id":"o1"},"lookup":{"items":{"has_many":"items","fk":"order_id","sort":{"pos":1}}}}`)
	expectStatus(t, rec, http.StatusOK)

	if !strings.Contains(rec.Body.String(), `"i1"`) || !strings.Contains(rec.Body.String(), `"i2"`) || strings.Contains(rec.Body.String(), `"i3"`) {
		t.Fatalf("lookup rows %s", rec.Body.String())
	}
}
