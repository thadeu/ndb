package server_test

import (
	"bytes"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thadeu/ndb/internal/logging"
	"github.com/thadeu/ndb/internal/server"
)

const cpf = "39053344705"

// noPlainOnDisk fails when a file of the data dir holds the plain value.
func noPlainOnDisk(t *testing.T, dir, plain string) {
	t.Helper()

	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}

		data, _ := os.ReadFile(path)

		if bytes.Contains(data, []byte(plain)) {
			t.Errorf("%s holds the plain value:\n%s", path, data)
		}

		return nil
	})
}

func TestEncryptedFieldIsSealedOnDiskAndPlainForTheSocket(t *testing.T) {
	srv, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/users", `{"encrypted":["cpf","pem"]}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/users/_doc", `{"_id":"u1","cpf":"`+cpf+`","pem":{"k":"PRIVATE"},"name":"Ana"}`), http.StatusOK)

	if rec := call(h, "GET", "/users/_doc/u1", ""); !strings.Contains(rec.Body.String(), `"cpf":"`+cpf+`"`) || !strings.Contains(rec.Body.String(), `"pem":{"k":"PRIVATE"}`) {
		t.Fatalf("get %s", rec.Body.String())
	}

	if rec := call(h, "POST", "/users/_search", `{}`); !strings.Contains(rec.Body.String(), cpf) {
		t.Fatalf("search %s", rec.Body.String())
	}

	rec := call(h, "POST", "/users/_update/u1", `{"update":{"$set":{"name":"Bia"}}}`)
	if !strings.Contains(rec.Body.String(), cpf) {
		t.Fatalf("update %s", rec.Body.String())
	}

	expectCode(t, call(h, "POST", "/users/_update/u1", `{"update":{"$inc":{"cpf":1}}}`), http.StatusBadRequest, "invalid_query")

	col, err := srv.DB().Get("users")
	if err != nil {
		t.Fatal(err)
	}

	if err := col.Seal(); err != nil {
		t.Fatal(err)
	}

	noPlainOnDisk(t, srv.DB().Dir(), cpf)
	noPlainOnDisk(t, srv.DB().Dir(), "PRIVATE")

	if rec := call(h, "GET", "/users/_doc/u1", ""); !strings.Contains(rec.Body.String(), cpf) || !strings.Contains(rec.Body.String(), `"name":"Bia"`) {
		t.Fatalf("get after seal %s", rec.Body.String())
	}
}

func TestEncryptedFieldNeedsTheDecryptRight(t *testing.T) {
	unix, tcp, addr := authServer(t, func(srv *server.Server) {
		srv.SetTokens([]server.Token{
			{Name: "app", Secret: "app-secret", Write: []string{"users"}},
			{Name: "kyc", Secret: "kyc-secret", Read: []string{"users"}, Decrypt: []string{"users"}},
		})
	})

	for _, c := range []struct{ method, path, body string }{
		{"PUT", "/users", `{"encrypted":["cpf"]}`},
		{"POST", "/users/_doc", `{"_id":"u1","cpf":"` + cpf + `"}`},
	} {
		req, _ := http.NewRequest(c.method, "http://ndb"+c.path, strings.NewReader(c.body))

		resp, err := unix.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		resp.Body.Close()
	}

	get := func(token string) string {
		req, _ := http.NewRequest("GET", "http://"+addr+"/users/_doc/u1", nil)
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := tcp.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)

		return string(body)
	}

	if body := get("app-secret"); strings.Contains(body, cpf) || !strings.Contains(body, `"cpf":"ndb:e1:k1:`) {
		t.Fatalf("app: %s", body)
	}

	if body := get("kyc-secret"); !strings.Contains(body, `"cpf":"`+cpf+`"`) {
		t.Fatalf("kyc: %s", body)
	}
}

func TestEncryptedCollectionLogsNoBody(t *testing.T) {
	_, h, buf := logServer(t, logging.Options{Level: slog.LevelDebug, BodyMax: 4096}, true)

	expectStatus(t, call(h, "PUT", "/users", `{"encrypted":["cpf"]}`), http.StatusOK)
	call(h, "POST", "/users/_doc", `{"_id":"u1","cpf":"`+cpf+`"}`)
	call(h, "POST", "/users/_update/u1", `{"update":{"$set":{"cpf":"`+cpf+`"}}}`)
	call(h, "POST", "/users/_bulk", `{"docs":[{"_id":"u2","cpf":"`+cpf+`"}]}`)
	call(h, "POST", "/users/_search", `{"query":{"cpf":"`+cpf+`"}}`)
	call(h, "POST", "/users/_update/u1", `{"update":{"$inc":{"cpf":1}}}`)

	if strings.Contains(buf.String(), cpf) {
		t.Fatalf("the log has the plain value:\n%s", buf.String())
	}

	if !strings.Contains(buf.String(), `"op":"insert"`) {
		t.Fatalf("no write line:\n%s", buf.String())
	}
}

func TestEveryWriteSealsTheField(t *testing.T) {
	srv, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/users", `{"encrypted":["cpf"]}`), http.StatusOK)
	expectStatus(t, call(h, "PUT", "/events", `{"time":"at","partition_by":"month","encrypted":["cpf"]}`), http.StatusOK)

	writes := []struct{ path, body string }{
		{"/users/_bulk", `{"docs":[{"_id":"b1","cpf":"` + cpf + `1"}]}`},
		{"/users/_tx", `{"ops":[{"op":"insert","doc":{"_id":"t1","cpf":"` + cpf + `2"}}]}`},
		{"/users/_update/up1", `{"update":{"$set":{"cpf":"` + cpf + `3"}},"upsert":true}`},
		{"/users/_bulk", `{"upsert":true,"docs":[{"_id":"b1","cpf":"` + cpf + `4"}]}`},
		{"/events/_doc", `{"_id":"e1","at":"2026-11-01T00:00:00Z","cpf":"` + cpf + `5"}`},
	}

	for _, wr := range writes {
		if rec := call(h, "POST", wr.path, wr.body); rec.Code >= 300 {
			t.Fatalf("%s: %d %s", wr.path, rec.Code, rec.Body.String())
		}
	}

	noPlainOnDisk(t, srv.DB().Dir(), cpf)

	if rec := call(h, "POST", "/users/_search", `{}`); strings.Count(rec.Body.String(), cpf) != 3 || !strings.Contains(rec.Body.String(), cpf+"4") {
		t.Fatalf("search %s", rec.Body.String())
	}

	if rec := call(h, "POST", "/events/_search", `{}`); !strings.Contains(rec.Body.String(), cpf+"5") {
		t.Fatalf("partition search %s", rec.Body.String())
	}
}

func TestEncryptedAddedLaterReachesAnOpenPartition(t *testing.T) {
	srv, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/events", `{"time":"at","partition_by":"month"}`), http.StatusOK)
	call(h, "POST", "/events/_doc", `{"_id":"e1","at":"2026-11-01T00:00:00Z","cpf":"x"}`)
	expectStatus(t, call(h, "PUT", "/events", `{"encrypted":["cpf"]}`), http.StatusOK)
	call(h, "POST", "/events/_doc", `{"_id":"e2","at":"2026-11-02T00:00:00Z","cpf":"`+cpf+`"}`)

	noPlainOnDisk(t, srv.DB().Dir(), cpf)
}
