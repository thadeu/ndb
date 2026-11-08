package server_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/thadeu/ndb/internal/server"
)

// A scoped token may read, write or administer the collections of its lists
// (names or globs); write includes read and admin includes both. Anything
// else is 403 forbidden; an unknown token is 401.
func TestScopedTokens(t *testing.T) {
	_, tcp, addr := authServer(t, func(srv *server.Server) {
		srv.SetTokens([]server.Token{
			{Name: "esl", Secret: "esl-secret", Write: []string{"outbox", "calls*"}},
			{Name: "dash", Secret: "dash-secret", Read: []string{"calls*"}},
			{Name: "ops", Secret: "ops-secret", Admin: []string{"*"}},
			{Name: "calls-admin", Secret: "calls-admin-secret", Admin: []string{"calls*"}},
		})
	})

	cases := []struct {
		token, method, path, body string
		allowed                   bool
	}{
		{"esl", "POST", "/outbox/_doc", `{"a":1}`, true},
		{"esl", "POST", "/outbox/_search", `{}`, true},
		{"esl", "POST", "/calls-2026-11/_bulk", `{"docs":[{"a":1}]}`, true},
		{"esl", "POST", "/outbox/_ack", `{"consumer":"c","pos":"e1.1"}`, true},
		{"esl", "PUT", "/outbox", `{}`, false},
		{"esl", "POST", "/outbox/_truncate", ``, false},
		{"esl", "GET", "/other/_doc/x", ``, false},
		{"dash", "POST", "/calls/_search", `{}`, true},
		{"dash", "POST", "/calls-*/_count", `{}`, true},
		{"dash", "POST", "/logs-*/_count", `{}`, false},
		{"dash", "POST", "/calls/_doc", `{"a":1}`, false},
		{"dash", "GET", "/_collections", ``, true},
		{"dash", "POST", "/_blobs", `x`, false},
		{"ops", "PUT", "/fresh", `{}`, true},
		{"ops", "POST", "/fresh/_compact", ``, true},
		{"esl", "POST", "/outbox/_rename", `{"to":"outbox2"}`, false},
		{"calls-admin", "POST", "/calls_restored/_rename", `{"to":"other"}`, false},
		{"calls-admin", "POST", "/calls/_restore", `{"at":"2026-01-01T00:00:00Z","to":"other"}`, false},
		{"calls-admin", "POST", "/_restore", `{"collection":"other","at":"2026-01-01T00:00:00Z","to":"calls_x"}`, false},
		{"esl", "POST", "/_restore", `{"collection":"outbox","at":"2026-01-01T00:00:00Z","to":"outbox_x"}`, false},
		{"dash", "GET", "/calls/_pitr", ``, true},
	}

	for _, c := range cases {
		req, _ := http.NewRequest(c.method, "http://"+addr+c.path, strings.NewReader(c.body))
		req.Header.Set("Authorization", "Bearer "+c.token+"-secret")

		resp, err := tcp.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		resp.Body.Close()

		denied := resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized

		if denied == c.allowed {
			t.Errorf("%s %s %s: status %d, allowed %v", c.token, c.method, c.path, resp.StatusCode, c.allowed)
		}

		if !c.allowed && resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s %s: status %d, want 403", c.token, c.method, c.path, resp.StatusCode)
		}
	}

	req, _ := http.NewRequest("GET", "http://"+addr+"/_collections", nil)
	req.Header.Set("Authorization", "Bearer nope")

	resp, err := tcp.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unknown token: %d, want 401", resp.StatusCode)
	}
}
