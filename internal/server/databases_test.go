package server_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thadeu/ndb/internal/backup"
	"github.com/thadeu/ndb/internal/logging"
	"github.com/thadeu/ndb/internal/server"
)

func in(db string) map[string]string {
	return map[string]string{"Ndb-Database": db}
}

func codeOf(t *testing.T, body string) string {
	t.Helper()

	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}

	_ = json.Unmarshal([]byte(body), &e)

	return e.Error.Code
}

func TestDatabaseHeaderKeepsTheSameNamesApart(t *testing.T) {
	c := unixServer(t)

	for _, db := range []string{"", "billion", "cronota"} {
		hdr := map[string]string{}

		if db != "" {
			hdr = in(db)
		}

		resp, body := do(t, c, "POST", "/users/_doc", `{"_id":"u1","app":"`+db+`"}`, hdr)

		if resp.StatusCode != 201 && resp.StatusCode != 200 {
			t.Fatalf("insert in %q: %d %s", db, resp.StatusCode, body)
		}
	}

	for db, want := range map[string]string{"": `"app":""`, "default": `"app":""`, "billion": `"app":"billion"`, "cronota": `"app":"cronota"`} {
		hdr := map[string]string{}

		if db != "" {
			hdr = in(db)
		}

		resp, body := do(t, c, "GET", "/users/_doc/u1", "", hdr)

		if resp.StatusCode != 200 || !strings.Contains(body, want) {
			t.Fatalf("database %q: %d %s", db, resp.StatusCode, body)
		}
	}

	resp, body := do(t, c, "POST", "/users/_count", `{}`, in("billion"))

	if resp.StatusCode != 200 || !strings.Contains(body, `"count":1`) {
		t.Fatalf("count: %d %s", resp.StatusCode, body)
	}

	resp, body = do(t, c, "GET", "/_collections", "", in("billion"))

	if resp.StatusCode != 200 || !strings.Contains(body, "users") {
		t.Fatalf("collections: %d %s", resp.StatusCode, body)
	}
}

func TestDatabaseNamesAndUnknownDatabasesOverHTTP(t *testing.T) {
	c := unixServer(t)

	resp, body := do(t, c, "GET", "/users/_doc/u1", "", in("Bad Name"))

	if resp.StatusCode != 400 || codeOf(t, body) != "invalid_database" {
		t.Fatalf("bad name: %d %s", resp.StatusCode, body)
	}

	for _, req := range [][3]string{
		{"GET", "/users/_doc/u1", ""},
		{"POST", "/users/_search", `{}`},
		{"POST", "/users/_count", `{}`},
		{"GET", "/_collections", ""},
		{"POST", "/events/_streams/read", `{}`},
	} {
		resp, body := do(t, c, req[0], req[1], req[2], in("nope"))

		if resp.StatusCode != 404 || codeOf(t, body) != "database_not_found" {
			t.Fatalf("%s %s on an unknown database: %d %s", req[0], req[1], resp.StatusCode, body)
		}
	}

	_, body = do(t, c, "GET", "/_databases", "", nil)

	if strings.Contains(body, "nope") {
		t.Fatalf("a read made a database: %s", body)
	}
}

func TestDatabasesRoutes(t *testing.T) {
	c := unixServer(t)

	do(t, c, "PUT", "/users", `{"indexes":["email"]}`, in("billion"))
	do(t, c, "POST", "/users/_doc", `{"_id":"u1"}`, in("billion"))

	resp, body := do(t, c, "GET", "/_databases", "", nil)

	if resp.StatusCode != 200 {
		t.Fatalf("list: %d %s", resp.StatusCode, body)
	}

	var list struct {
		Databases []struct {
			Name        string `json:"name"`
			Collections int    `json:"collections"`
		} `json:"databases"`
	}

	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatal(err)
	}

	if len(list.Databases) != 2 || list.Databases[0].Name != "default" || list.Databases[1].Name != "billion" || list.Databases[1].Collections != 1 {
		t.Fatalf("list %s", body)
	}

	resp, body = do(t, c, "DELETE", "/_databases/billion", "", nil)

	if resp.StatusCode != 400 || codeOf(t, body) != "confirm_required" {
		t.Fatalf("drop without confirm: %d %s", resp.StatusCode, body)
	}

	resp, body = do(t, c, "DELETE", "/_databases/default?confirm=default", "", nil)

	if resp.StatusCode != 400 || codeOf(t, body) != "invalid_request" {
		t.Fatalf("drop default: %d %s", resp.StatusCode, body)
	}

	resp, body = do(t, c, "DELETE", "/_databases/nope?confirm=nope", "", nil)

	if resp.StatusCode != 404 || codeOf(t, body) != "database_not_found" {
		t.Fatalf("drop unknown: %d %s", resp.StatusCode, body)
	}

	resp, body = do(t, c, "DELETE", "/_databases/Bad?confirm=Bad", "", nil)

	if resp.StatusCode != 400 || codeOf(t, body) != "invalid_database" {
		t.Fatalf("drop bad name: %d %s", resp.StatusCode, body)
	}

	resp, body = do(t, c, "DELETE", "/_databases/billion?confirm=billion", "", nil)

	if resp.StatusCode != 200 || !strings.Contains(body, `"dropped":"billion"`) {
		t.Fatalf("drop: %d %s", resp.StatusCode, body)
	}

	resp, body = do(t, c, "GET", "/users/_doc/u1", "", in("billion"))

	if resp.StatusCode != 404 || codeOf(t, body) != "database_not_found" {
		t.Fatalf("read after the drop: %d %s", resp.StatusCode, body)
	}
}

func TestDaemonRoutesIgnoreTheDatabaseHeader(t *testing.T) {
	c := unixServer(t)

	for _, req := range [][3]string{
		{"GET", "/_cluster/settings", ""},
		{"PUT", "/_cluster/settings", `{"log":{"level":"info"}}`},
		{"GET", "/_log", ""},
		{"GET", "/_health", ""},
	} {
		resp, body := do(t, c, req[0], req[1], req[2], in("nope"))

		if resp.StatusCode != 200 {
			t.Fatalf("%s %s with a database header: %d %s", req[0], req[1], resp.StatusCode, body)
		}
	}

	_, body := do(t, c, "GET", "/_databases", "", nil)

	if strings.Contains(body, "nope") {
		t.Fatalf("a daemon route made a database: %s", body)
	}
}

func TestDatabasesNeedACreateWithoutAllowImplicit(t *testing.T) {
	h := strictHandler(t)

	rec := raw(h, "POST", "/users/_doc", `{"_id":"u1"}`)

	if rec.Code != 404 {
		t.Fatalf("control: %d", rec.Code)
	}

	send := func(method, path, body, db string) (int, string) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))

		if db != "" {
			req.Header.Set("Ndb-Database", db)
		}

		rec := httptest.NewRecorder()

		h.ServeHTTP(rec, req)

		return rec.Code, rec.Body.String()
	}

	if code, body := send("PUT", "/users", `{}`, "billion"); code != 404 || codeOf(t, body) != "database_not_found" || !strings.Contains(body, "PUT /_databases/billion") {
		t.Fatalf("write in a new database: %d %s", code, body)
	}

	if code, body := send("PUT", "/_databases/billion", "", ""); code != 201 || !strings.Contains(body, `"created":true`) {
		t.Fatalf("create: %d %s", code, body)
	}

	if code, body := send("PUT", "/_databases/billion", "", ""); code != 200 || !strings.Contains(body, `"created":false`) {
		t.Fatalf("create again: %d %s", code, body)
	}

	if code, body := send("PUT", "/users", `{}`, "billion"); code >= 300 {
		t.Fatalf("ensure in the created database: %d %s", code, body)
	}

	if code, body := send("PUT", "/_databases/Bad", "", ""); code != 400 || codeOf(t, body) != "invalid_database" {
		t.Fatalf("create a bad name: %d %s", code, body)
	}

	if code, body := send("PUT", "/_databases/default", "", ""); code != 200 || !strings.Contains(body, `"created":false`) {
		t.Fatalf("create default: %d %s", code, body)
	}
}

func TestStreamsLiveInTheirDatabase(t *testing.T) {
	c := unixServer(t)

	resp, body := do(t, c, "POST", "/events/_streams/append", `{"doc":{"n":1}}`, in("billion"))

	if resp.StatusCode != 201 {
		t.Fatalf("append: %d %s", resp.StatusCode, body)
	}

	do(t, c, "PUT", "/events/_streams/groups/mailer", `{"start":"0"}`, in("billion"))

	resp, body = do(t, c, "POST", "/events/_streams/groups/mailer/read", `{"consumer":"c1","count":10}`, in("billion"))

	if resp.StatusCode != 200 || !strings.Contains(body, `"n":1`) {
		t.Fatalf("group read: %d %s", resp.StatusCode, body)
	}

	resp, body = do(t, c, "GET", "/events/_streams", "", nil)

	if resp.StatusCode != 404 {
		t.Fatalf("the stream of billion is in the default database: %d %s", resp.StatusCode, body)
	}

	resp, body = do(t, c, "POST", "/_backup", `{}`, nil)

	if resp.StatusCode != 200 || !strings.HasPrefix(body, `{"ndb_backup":2`) || !strings.Contains(body, `{"_section":"database","name":"billion"`) || !strings.Contains(body, `{"_section":"stream","collection":"events"`) {
		t.Fatalf("the backup does not hold the database and its stream: %d %.300s", resp.StatusCode, body)
	}

	if err := backup.Extract(bytes.NewReader([]byte(body)), t.TempDir()+"/r"); err != nil {
		t.Fatal(err)
	}

	resp, body = do(t, c, "POST", "/_backup", `{}`, in("billion"))

	if resp.StatusCode != 200 || !strings.HasPrefix(body, `{"ndb_backup":1`) || strings.Contains(body, `"_section":"database"`) {
		t.Fatalf("the backup of one database: %d %.300s", resp.StatusCode, body)
	}

	resp, body = do(t, c, "DELETE", "/_databases/billion?confirm=billion", "", nil)

	if resp.StatusCode != 200 {
		t.Fatalf("drop with an open stream: %d %s", resp.StatusCode, body)
	}

	resp, body = do(t, c, "POST", "/events/_streams/append", `{"doc":{"n":2}}`, in("billion"))

	if resp.StatusCode != 201 {
		t.Fatalf("append after the drop: %d %s", resp.StatusCode, body)
	}

	_, body = do(t, c, "GET", "/events/_streams", "", in("billion"))

	if !strings.Contains(body, `"length":1`) {
		t.Fatalf("the new database has the old stream: %s", body)
	}
}

func TestLogLinesNameTheDatabase(t *testing.T) {
	_, h, buf := logServer(t, logging.Options{Level: slog.LevelDebug}, true)

	req := httptest.NewRequest("POST", "/calls/_doc", strings.NewReader(`{"_id":"a1"}`))
	req.Header.Set("Ndb-Database", "billion")

	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code >= 300 {
		t.Fatalf("insert: %d %s", rec.Code, rec.Body)
	}

	for _, msg := range []string{"write", "request"} {
		lines := buf.lines(t, msg)

		if len(lines) == 0 || lines[0]["database"] != "billion" {
			t.Fatalf("%s line without the database: %v\n%s", msg, lines, buf)
		}
	}

	req = httptest.NewRequest("DELETE", "/_databases/billion?confirm=billion", nil)
	rec = httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if lines := buf.lines(t, "database dropped"); len(lines) != 1 || lines[0]["database"] != "billion" {
		t.Fatalf("drop line: %v", lines)
	}
}

func TestScopedTokensStayInTheDefaultDatabase(t *testing.T) {
	_, tcp, addr := authServer(t, func(srv *server.Server) {
		srv.SetToken("full-secret")
		srv.SetTokens([]server.Token{{Name: "app", Secret: "app-secret", Write: []string{"*"}}})
	})

	send := func(token, db, method, path, body string) (int, string) {
		req, _ := http.NewRequest(method, "http://"+addr+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)

		if db != "" {
			req.Header.Set("Ndb-Database", db)
		}

		resp, err := tcp.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		defer resp.Body.Close()

		var b bytes.Buffer

		b.ReadFrom(resp.Body)

		return resp.StatusCode, b.String()
	}

	if code, body := send("app-secret", "", "POST", "/users/_doc", `{"_id":"u1"}`); code >= 300 {
		t.Fatalf("scoped token, default database: %d %s", code, body)
	}

	if code, body := send("app-secret", "default", "GET", "/users/_doc/u1", ""); code != 200 {
		t.Fatalf("scoped token, default by name: %d %s", code, body)
	}

	if code, body := send("app-secret", "billion", "POST", "/users/_doc", `{"_id":"u1"}`); code != 403 || codeOf(t, body) != "forbidden" {
		t.Fatalf("scoped token, other database: %d %s", code, body)
	}

	if code, body := send("app-secret", "", "GET", "/_databases", ""); code != 403 {
		t.Fatalf("scoped token lists the databases: %d %s", code, body)
	}

	if code, body := send("full-secret", "billion", "POST", "/users/_doc", `{"_id":"u1"}`); code >= 300 {
		t.Fatalf("full token, other database: %d %s", code, body)
	}

	if code, body := send("full-secret", "", "GET", "/_databases", ""); code != 200 || !strings.Contains(body, "billion") {
		t.Fatalf("full token lists the databases: %d %s", code, body)
	}
}
