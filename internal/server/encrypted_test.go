package server_test

import (
	"bytes"
	"encoding/json"
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

	if rec := call(h, "GET", "/users/_doc/u1?decrypt=1", ""); !strings.Contains(rec.Body.String(), `"cpf":"`+cpf+`"`) || !strings.Contains(rec.Body.String(), `"pem":{"k":"PRIVATE"}`) {
		t.Fatalf("get %s", rec.Body.String())
	}

	if rec := call(h, "POST", "/users/_search?decrypt=1", `{}`); !strings.Contains(rec.Body.String(), cpf) {
		t.Fatalf("search %s", rec.Body.String())
	}

	rec := call(h, "POST", "/users/_update/u1?decrypt=1", `{"update":{"$set":{"name":"Bia"}}}`)
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

	if rec := call(h, "GET", "/users/_doc/u1?decrypt=1", ""); !strings.Contains(rec.Body.String(), cpf) || !strings.Contains(rec.Body.String(), `"name":"Bia"`) {
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
		req, _ := http.NewRequest("GET", "http://"+addr+"/users/_doc/u1?decrypt=1", nil)
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

	if rec := call(h, "POST", "/users/_search?decrypt=1", `{}`); strings.Count(rec.Body.String(), cpf) != 3 || !strings.Contains(rec.Body.String(), cpf+"4") {
		t.Fatalf("search %s", rec.Body.String())
	}

	if rec := call(h, "POST", "/events/_search?decrypt=1", `{}`); !strings.Contains(rec.Body.String(), cpf+"5") {
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

	expectStatus(t, call(h, "PUT", "/users", `{"encrypted":["cpf"],"blind":{"cpf":"numeric"},"unique":["cpf"],"indexes":["cpf"]}`), http.StatusOK)
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

	expectCode(t, call(h, "POST", "/users/_aggregate", `{"pipeline":[{"$group":{"_id":null,"m":{"$max":"$cpf"}}}]}`), http.StatusBadRequest, "invalid_query")

	if rows := aggOf(t, h, "users?decrypt=1", `[{"$match":{"cpf":"`+cpf+`"}},{"$project":{"name":1}}]`); len(rows) != 1 || !strings.Contains(rows[0], "Ana") {
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

	if rec := call(h, "GET", "/users/_doc/u1?decrypt=1", ""); strings.Contains(rec.Body.String(), "_bidx") || !strings.Contains(rec.Body.String(), "222.333.444-55") {
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

func TestAValueIsSealedUnlessTheCallAsks(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/users", `{"encrypted":["cpf"]}`), http.StatusOK)
	call(h, "POST", "/users/_doc", `{"_id":"u1","cpf":"`+cpf+`"}`)

	for _, path := range []string{"/users/_doc/u1", "/users/_doc/u1?decrypt=0"} {
		if rec := call(h, "GET", path, ""); strings.Contains(rec.Body.String(), cpf) || !strings.Contains(rec.Body.String(), "ndb:e1:k1:") {
			t.Fatalf("%s: %s", path, rec.Body.String())
		}
	}

	var doc struct {
		CPF string `json:"cpf"`
	}

	_ = json.Unmarshal(call(h, "GET", "/users/_doc/u1", "").Body.Bytes(), &doc)

	call(h, "POST", "/users/_update/u1", `{"update":{"$set":{"cpf":"`+doc.CPF+`","name":"Ana"}}}`)

	if rec := call(h, "GET", "/users/_doc/u1?decrypt=1", ""); !strings.Contains(rec.Body.String(), `"cpf":"`+cpf+`"`) {
		t.Fatalf("a sealed value sent back changed the field: %s", rec.Body.String())
	}
}

func TestAnyWriteEncryptsAnOldDocument(t *testing.T) {
	srv, h := newHandler(t)

	call(h, "POST", "/users/_doc", `{"_id":"u1","cpf":"`+cpf+`"}`)
	expectStatus(t, call(h, "PUT", "/users", `{"blind":{"cpf":"numeric"}}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/users/_update_by_query", `{"query":{"_id":{"$exists":true}},"update":{"$set":{"encrypted_at":"now"}}}`), http.StatusOK)

	if rec := call(h, "POST", "/users/_count", `{"query":{"cpf":"`+cpf+`"}}`); !strings.Contains(rec.Body.String(), `"count":1`) {
		t.Fatalf("count %s", rec.Body.String())
	}

	col, _ := srv.DB().Get("users")
	if err := col.Seal(); err != nil {
		t.Fatal(err)
	}

	if _, err := col.Compact(); err != nil {
		t.Fatal(err)
	}

	noPlainOnDisk(t, filepath.Join(srv.DB().Dir(), "collections", "users", "segments"), cpf)
}

func TestGroupByABlindFieldGivesTheValue(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/orders", `{"blind":{"cpf":"numeric"}}`), http.StatusOK)
	call(h, "POST", "/orders/_bulk", `{"docs":[
		{"_id":"o1","cpf":"390.533.447-05","total":10,"st":"paid"},
		{"_id":"o2","cpf":"39053344705","total":5,"st":"paid"},
		{"_id":"o3","cpf":"11122233344","total":7,"st":"open"}]}`)

	rows := aggOf(t, h, "orders?decrypt=1", `[{"$group":{"_id":"$cpf","total":{"$sum":"$total"}}},{"$sort":{"total":-1}}]`)
	if len(rows) != 2 || !strings.Contains(rows[0], `"total":15`) || !strings.Contains(rows[0], `"_id":"390.533.447-05"`) && !strings.Contains(rows[0], `"_id":"39053344705"`) {
		t.Fatalf("rows %v", rows)
	}

	rows = aggOf(t, h, "orders?decrypt=1", `[{"$group":{"_id":{"c":"$cpf","st":"$st"},"n":{"$sum":1}}},{"$sort":{"n":-1}}]`)
	if len(rows) != 2 || !strings.Contains(rows[0], `"st":"paid"`) || !strings.Contains(rows[0], `"c":"39053344705"`) && !strings.Contains(rows[0], `"c":"390.533.447-05"`) {
		t.Fatalf("compound rows %v", rows)
	}

	for _, row := range rows {
		if strings.Contains(row, "_ndb") || strings.Contains(row, "_bidx") || strings.Contains(row, "ndb:") {
			t.Fatalf("a helper or a hash in %s", row)
		}
	}

	expectCode(t, call(h, "POST", "/orders/_aggregate", `{"pipeline":[{"$group":{"_id":null,"s":{"$sum":"$cpf"}}}]}`), http.StatusBadRequest, "invalid_query")
}

func TestGroupByABlindFieldStaysSealedWithoutDecrypt(t *testing.T) {
	unix, tcp, addr := authServer(t, func(srv *server.Server) {
		srv.SetTokens([]server.Token{{Name: "bi", Secret: "bi-secret", Read: []string{"orders"}}})
	})

	for _, c := range []struct{ method, path, body string }{
		{"PUT", "/orders", `{"blind":{"cpf":"numeric"}}`},
		{"POST", "/orders/_doc", `{"_id":"o1","cpf":"` + cpf + `"}`},
	} {
		req, _ := http.NewRequest(c.method, "http://ndb"+c.path, strings.NewReader(c.body))

		resp, err := unix.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		resp.Body.Close()
	}

	req, _ := http.NewRequest("POST", "http://"+addr+"/orders/_aggregate?decrypt=1", strings.NewReader(`{"pipeline":[{"$group":{"_id":"$cpf","n":{"$sum":1}}}]}`))
	req.Header.Set("Authorization", "Bearer bi-secret")

	resp, err := tcp.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if strings.Contains(string(body), cpf) || !strings.Contains(string(body), `"_id":"ndb:e1:k1:`) {
		t.Fatalf("aggregate %s", body)
	}
}

func TestGroupByABlindFieldAcrossPartitions(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/sales", `{"time":"at","partition_by":"month","blind":{"cpf":"numeric"}}`), http.StatusOK)
	call(h, "POST", "/sales/_bulk", `{"docs":[
		{"_id":"s1","at":"2026-10-03T00:00:00Z","cpf":"390.533.447-05","v":1},
		{"_id":"s2","at":"2026-11-03T00:00:00Z","cpf":"39053344705","v":2}]}`)

	rows := aggOf(t, h, "sales?decrypt=1", `[{"$group":{"_id":"$cpf","v":{"$sum":"$v"}}}]`)
	if len(rows) != 1 || !strings.Contains(rows[0], `"v":3`) || strings.Contains(rows[0], "ndb:") || strings.Contains(rows[0], "_ndb") {
		t.Fatalf("rows %v", rows)
	}
}

func TestLookupOpensTheDocumentsOfTheSourceCollection(t *testing.T) {
	unix, tcp, addr := authServer(t, func(srv *server.Server) {
		srv.SetTokens([]server.Token{{Name: "shop", Secret: "shop-secret", Read: []string{"orders", "customers", "cards"}, Decrypt: []string{"orders"}}})
	})

	send := func(client *http.Client, method, url, body, token string) string {
		req, _ := http.NewRequest(method, url, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		defer resp.Body.Close()

		out, _ := io.ReadAll(resp.Body)

		return string(out)
	}

	for _, c := range []struct{ method, path, body string }{
		{"PUT", "/customers", `{"blind":{"cpf":"numeric"}}`},
		{"PUT", "/cards", `{"encrypted":["number"],"indexes":["customer_id"]}`},
		{"POST", "/customers/_doc", `{"_id":"c1","cpf":"` + cpf + `"}`},
		{"POST", "/cards/_doc", `{"_id":"k1","customer_id":"c1","number":"4111111111111111"}`},
		{"POST", "/orders/_doc", `{"_id":"o1","customer_id":"c1"}`},
	} {
		send(unix, c.method, "http://ndb"+c.path, c.body, "")
	}

	body := `{"lookup":{"customer":{"belongs_to":"customers","fk":"customer_id","lookup":{"cards":{"has_many":"cards","fk":"customer_id"}}}}}`

	plain := send(unix, "POST", "http://ndb/orders/_search?decrypt=1", body, "")
	if !strings.Contains(plain, `"cpf":"`+cpf+`"`) || !strings.Contains(plain, `"number":"4111111111111111"`) || strings.Contains(plain, "_bidx") {
		t.Fatalf("socket: %s", plain)
	}

	if sealed := send(unix, "POST", "http://ndb/orders/_search", body, ""); strings.Contains(sealed, cpf) || strings.Contains(sealed, "_bidx") {
		t.Fatalf("socket without decrypt=1: %s", sealed)
	}

	if shop := send(tcp, "POST", "http://"+addr+"/orders/_search?decrypt=1", body, "shop-secret"); strings.Contains(shop, cpf) || strings.Contains(shop, "4111") {
		t.Fatalf("a right on orders opened customers: %s", shop)
	}
}

func TestBlindTypeChangeAndUnset(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/users", `{"blind":{"cpf":"numeric"}}`), http.StatusOK)
	expectCode(t, call(h, "PUT", "/users", `{"blind":{"cpf":"exact"}}`), http.StatusConflict, "catalog_conflict")

	call(h, "POST", "/users/_doc", `{"_id":"u1","cpf":"`+cpf+`"}`)
	call(h, "POST", "/users/_update/u1", `{"update":{"$unset":{"cpf":""}}}`)

	if rec := call(h, "POST", "/users/_count", `{"query":{"cpf":"`+cpf+`"}}`); !strings.Contains(rec.Body.String(), `"count":0`) {
		t.Fatalf("an unset field still matches: %s", rec.Body.String())
	}
}

// A field that becomes blind seals the old documents in the background.
// Until the job is ready, a filter on the field finds the old documents by
// the plain value and the new ones by the hash.
func TestAFieldThatBecomesBlindSealsTheOldDocuments(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/events", `{}`), http.StatusOK)
	call(h, "POST", "/events/_doc", `{"_id":"e1","rcpt_to":"Ana@dest.test"}`)
	call(h, "POST", "/events/_doc", `{"_id":"e2","rcpt_to":"bia@dest.test"}`)
	call(h, "POST", "/events/_doc", `{"_id":"e3"}`)

	expectStatus(t, call(h, "PUT", "/events", `{"encrypted":["rcpt_to"],"blind":{"rcpt_to":"lower"}}`), http.StatusOK)
	call(h, "POST", "/events/_doc", `{"_id":"e4","rcpt_to":"ana@dest.test"}`)

	ids := func() string {
		rec := call(h, "POST", "/events/_search", `{"query":{"rcpt_to":"ana@dest.test"},"sort":{"_id":1}}`)
		expectStatus(t, rec, http.StatusOK)

		var out []string

		for line := range strings.SplitSeq(strings.TrimSpace(rec.Body.String()), "\n") {
			if i := strings.Index(line, `"_id":"`); i >= 0 {
				out = append(out, line[i+7:i+9])
			}
		}

		return strings.Join(out, ",")
	}

	if got := ids(); got != "e1,e4" {
		t.Errorf("before ready: %s, want e1,e4", got)
	}

	waitIndexReady(t, h, "events")

	if body := call(h, "GET", "/events", "").Body.String(); !strings.Contains(body, `"encrypting":[{"field":"rcpt_to","state":"ready"`) {
		t.Errorf("GET /events: %s", body)
	}

	if got := ids(); got != "e1,e4" {
		t.Errorf("after ready: %s, want e1,e4", got)
	}

	for _, id := range []string{"e1", "e2"} {
		if body := call(h, "GET", "/events/_doc/"+id, "").Body.String(); !strings.Contains(body, `"rcpt_to":"ndb:e1:`) {
			t.Errorf("%s is not sealed: %s", id, body)
		}
	}

	if body := call(h, "GET", "/events/_doc/e3", "").Body.String(); strings.Contains(body, "rcpt_to") {
		t.Errorf("e3 got the field: %s", body)
	}

}
