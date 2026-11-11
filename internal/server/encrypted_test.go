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

func TestBlindFieldIsFoundByItsValue(t *testing.T) {
	srv, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/users", `{"encrypted":["cpf"],"blind":{"cpf":"digits"},"unique":["cpf"],"indexes":["cpf"]}`), http.StatusOK)
	call(h, "POST", "/users/_doc", `{"_id":"u1","cpf":"390.533.447-05","name":"Ana"}`)
	call(h, "POST", "/users/_doc", `{"_id":"u2","cpf":"11122233344","name":"Bia"}`)

	expectCode(t, call(h, "POST", "/users/_doc", `{"_id":"u3","cpf":"`+cpf+`"}`), http.StatusConflict, "unique_violation")

	ids := func(body string) string {
		rec := call(h, "POST", "/users/_search", body)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body.String())
		}

		if strings.Contains(rec.Body.String(), "_bidx") {
			t.Fatalf("an answer has the hash: %s", rec.Body.String())
		}

		var out []string

		for line := range strings.SplitSeq(strings.TrimSpace(rec.Body.String()), "\n") {
			if i := strings.Index(line, `"_id":"`); i >= 0 {
				out = append(out, line[i+7:i+9])
			}
		}

		return strings.Join(out, ",")
	}

	cases := map[string]string{
		`{"query":{"cpf":"` + cpf + `"}}`:                          "u1",
		`{"query":{"cpf":{"$eq":"390.533.447-05"}}}`:               "u1",
		`{"query":{"cpf":{"$in":["11122233344","x"]}}}`:            "u2",
		`{"query":{"cpf":{"$ne":"11122233344"}},"sort":{"_id":1}}`: "u1",
		`{"query":{"$or":[{"cpf":"x"},{"name":"Bia"}]}}`:           "u2",
		`{"query":{"cpf":{"$exists":true}},"sort":{"_id":1}}`:      "u1,u2",
	}

	for body, want := range cases {
		if got := ids(body); got != want {
			t.Errorf("%s: %s, want %s", body, got, want)
		}
	}

	waitIndexReady(t, h, "users")

	if plan := planOf(t, h, "/users/_search", `{"query":{"cpf":"`+cpf+`"}}`); plan != "index:cpf_bidx" {
		t.Errorf("plan %s, want index:cpf_bidx", plan)
	}

	for _, body := range []string{`{"query":{"cpf":{"$gt":"1"}}}`, `{"query":{"cpf":{"$regex":"^3"}}}`} {
		expectCode(t, call(h, "POST", "/users/_search", body), http.StatusBadRequest, "invalid_query")
	}

	expectCode(t, call(h, "POST", "/users/_aggregate", `{"pipeline":[{"$group":{"_id":"$cpf"}}]}`), http.StatusBadRequest, "invalid_query")

	if rows := aggOf(t, h, "users", `[{"$match":{"cpf":"`+cpf+`"}},{"$project":{"name":1}}]`); len(rows) != 1 || !strings.Contains(rows[0], "Ana") {
		t.Errorf("aggregate %v", rows)
	}

	if rec := call(h, "POST", "/users/_count", `{"query":{"cpf":"`+cpf+`"}}`); !strings.Contains(rec.Body.String(), `"count":1`) {
		t.Errorf("count %s", rec.Body.String())
	}

	call(h, "POST", "/users/_update/u1", `{"update":{"$set":{"cpf":"222.333.444-55"}}}`)

	if got := ids(`{"query":{"cpf":"22233344455"}}`); got != "u1" {
		t.Errorf("after a $set: %s", got)
	}

	if got := ids(`{"query":{"cpf":"` + cpf + `"}}`); got != "" {
		t.Errorf("the old value still matches: %s", got)
	}

	if rec := call(h, "GET", "/users/_doc/u1", ""); strings.Contains(rec.Body.String(), "_bidx") || !strings.Contains(rec.Body.String(), "222.333.444-55") {
		t.Errorf("get %s", rec.Body.String())
	}

	noPlainOnDisk(t, srv.DB().Dir(), "22233344455")
}

func TestEncryptedFieldWithoutBlindRefusesAValueFilter(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/keys", `{"encrypted":["pem"]}`), http.StatusOK)
	call(h, "POST", "/keys/_doc", `{"_id":"k1","pem":"x"}`)

	expectCode(t, call(h, "POST", "/keys/_search", `{"query":{"pem":"x"}}`), http.StatusBadRequest, "invalid_query")
	expectCode(t, call(h, "POST", "/keys/_delete_by_query", `{"query":{"pem":"x"}}`), http.StatusBadRequest, "invalid_query")

	if rec := call(h, "POST", "/keys/_count", `{"query":{"pem":{"$exists":true}}}`); !strings.Contains(rec.Body.String(), `"count":1`) {
		t.Fatalf("exists %s", rec.Body.String())
	}
}
