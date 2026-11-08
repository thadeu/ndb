package server_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/thadeu/ndb/internal/server"
)

func TestLookupRoute(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "POST", "/customers/_bulk", `{"docs":[{"_id":"c1","name":"Ana"}]}`), http.StatusOK)
	expectStatus(t, call(h, "PUT", "/order_items", `{"indexes":["order_id"]}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/order_items/_bulk", `{"docs":[{"_id":"i1","order_id":"o1","sku":"A"}]}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/orders/_bulk", `{"docs":[{"_id":"o1","customer_id":"c1"}]}`), http.StatusOK)

	rec := call(h, "POST", "/orders/_search", `{"lookup":{"customer":{"belongs_to":"customers","fk":"customer_id"},"items":{"has_many":"order_items","fk":"order_id"}}}`)

	expectStatus(t, rec, http.StatusOK)

	body := rec.Body.String()

	if !strings.Contains(body, `"customer":{"_id":"c1","name":"Ana"}`) || !strings.Contains(body, `"items":[{"_id":"i1","order_id":"o1","sku":"A"}]`) {
		t.Fatalf("body = %s", body)
	}

	cases := []struct {
		body, code string
	}{
		{`{"lookup":{"x":{"belongs_to":"customers"}}}`, "lookup_invalid"},
		{`{"lookup":{"x":{"has_many":"customers","fk":"name"}}}`, "lookup_unindexed"},
		{`{"lookup":{"x":{"belongs_to":"nope","fk":"customer_id"}}}`, "unknown_collection"},
	}

	for _, c := range cases {
		if got := errorCode(t, call(h, "POST", "/orders/_search", c.body)); got != c.code {
			t.Errorf("%s: code %q, want %q", c.body, got, c.code)
		}
	}

	if got := errorCode(t, call(h, "POST", "/orders/_count", `{"lookup":{}}`)); got != "invalid_request" {
		t.Errorf("_count with lookup: %q", got)
	}
}

// A token that reads orders but not customers may not look customers up.
func TestLookupNeedsReadOnEveryCollection(t *testing.T) {
	unix, tcp, addr := authServer(t, func(srv *server.Server) {
		srv.SetTokens([]server.Token{{Name: "shop", Secret: "shop-secret", Read: []string{"orders", "order_items"}}})
	})

	for _, path := range []string{"/orders/_doc", "/customers/_doc"} {
		resp, err := unix.Post("http://ndb"+path, "application/json", strings.NewReader(`{"_id":"x","customer_id":"c1"}`))
		if err != nil {
			t.Fatal(err)
		}

		resp.Body.Close()
	}

	req, _ := http.NewRequest("POST", "http://"+addr+"/orders/_search", strings.NewReader(`{"lookup":{"customer":{"belongs_to":"customers","fk":"customer_id"}}}`))
	req.Header.Set("Authorization", "Bearer shop-secret")

	resp, err := tcp.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}
