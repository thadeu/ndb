package server_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/config"
	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/server"
)

func send(t *testing.T, c *http.Client, method, url, auth, body string) (int, string) {
	t.Helper()

	req, _ := http.NewRequest(method, url, strings.NewReader(body))

	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}

	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer resp.Body.Close()

	out, _ := io.ReadAll(resp.Body)

	return resp.StatusCode, string(out)
}

// POST /_cluster/key rotates the key, on the unix socket only. The old
// values are sealed again with k2, and the rotation ends by itself.
func TestTheKeyRotatesOnTheUnixSocketOnly(t *testing.T) {
	tick := engine.RotationTick
	engine.RotationTick = 10 * time.Millisecond

	t.Cleanup(func() { engine.RotationTick = tick })

	unix, tcp, addr := authServer(t, func(srv *server.Server) {
		srv.SetTokens([]server.Token{{Name: "root", Secret: "root-secret", Full: true}})
	})

	u := "http://ndb"

	send(t, unix, "PUT", u+"/users", "", `{"blind":{"cpf":"numeric"}}`)
	send(t, unix, "POST", u+"/users/_doc", "", `{"_id":"u1","cpf":"390.533.447-05"}`)

	if code, body := send(t, tcp, "POST", "http://"+addr+"/_cluster/key", "root-secret", `{}`); code != http.StatusForbidden {
		t.Fatalf("TCP with the full token: %d %s", code, body)
	}

	code, body := send(t, unix, "POST", u+"/_cluster/key", "", `{}`)
	if code != http.StatusOK || !strings.Contains(body, `"key_id":"k2"`) || strings.Contains(body, `"key":`) {
		t.Fatalf("rotate: %d %s", code, body)
	}

	if code, body := send(t, unix, "POST", u+"/_cluster/key", "", `{}`); code != http.StatusConflict {
		t.Fatalf("a second rotation: %d %s", code, body)
	}

	deadline := time.Now().Add(10 * time.Second)

	for {
		_, settings := send(t, unix, "GET", u+"/_cluster/settings", "", "")

		if !strings.Contains(settings, `"rotation"`) && strings.Contains(settings, `"key_id":"k2"`) {
			break
		}

		if time.Now().After(deadline) {
			t.Fatalf("the rotation never ended: %s", settings)
		}

		time.Sleep(10 * time.Millisecond)
	}

	if _, doc := send(t, unix, "GET", u+"/users/_doc/u1", "", ""); !strings.Contains(doc, `"cpf":"ndb:e1:k2:`) {
		t.Fatalf("u1 has the old key: %s", doc)
	}

	if _, rows := send(t, unix, "POST", u+"/users/_search?decrypt=1", "", `{"query":{"cpf":"39053344705"}}`); !strings.Contains(rows, "390.533.447-05") {
		t.Fatalf("search after: %s", rows)
	}
}

// With env, the answer has the new key once, for NDB_ENCRYPTION_KEY.
func TestAnEnvRotationGivesTheKeyOnce(t *testing.T) {
	unix, _, _ := authServer(t, func(*server.Server) {})

	code, body := send(t, unix, "POST", "http://ndb/_cluster/key", "", `{"env":true}`)

	var out struct {
		Key   string `json:"key"`
		KeyID string `json:"key_id"`
	}

	if err := json.Unmarshal([]byte(body), &out); code != http.StatusOK || err != nil || len(out.Key) != 64 || out.KeyID != "k2" {
		t.Fatalf("env rotate: %d %s", code, body)
	}

	if _, settings := send(t, unix, "GET", "http://ndb/_cluster/settings", "", ""); strings.Contains(settings, out.Key) {
		t.Fatalf("the settings show the new key: %s", settings)
	}
}

func openEnv(t *testing.T, dir, key, old string) (*engine.DB, http.Handler, error) {
	t.Helper()

	db, err := engine.Open(config.Config{DataDir: dir, AllowImplicit: true, EncryptionKey: key, EncryptionKeyOld: old}, slog.Default())
	if err != nil {
		return nil, nil, err
	}

	srv := server.New()
	srv.Attach(db)

	return db, srv.Handler(), nil
}

// An env rotation: the next start has the new key and the old one, the
// rotation ends by itself, and the old key is no longer needed.
func TestAnEnvKeyRotatesAcrossRestarts(t *testing.T) {
	tick := engine.RotationTick
	engine.RotationTick = 10 * time.Millisecond

	t.Cleanup(func() { engine.RotationTick = tick })

	dir := t.TempDir()
	k1 := strings.Repeat("ab", 32)

	db, h, err := openEnv(t, dir, k1, "")
	if err != nil {
		t.Fatal(err)
	}

	call(h, "PUT", "/users", `{"blind":{"cpf":"numeric"}}`)
	call(h, "POST", "/users/_doc", `{"_id":"u1","cpf":"390.533.447-05"}`)

	k2, err := db.RotateKey(true)
	if err != nil {
		t.Fatal(err)
	}

	db.Close()

	if _, _, err := openEnv(t, dir, k2, ""); err == nil || !strings.Contains(err.Error(), "NDB_ENCRYPTION_KEY_OLD") {
		t.Fatalf("a start with no old key: %v", err)
	}

	db, h, err = openEnv(t, dir, k2, k1)
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(10 * time.Second)

	for db.Key().Old != nil {
		if time.Now().After(deadline) {
			t.Fatalf("the rotation never ended: %+v", db.RotationState())
		}

		time.Sleep(10 * time.Millisecond)
	}

	if body := call(h, "GET", "/users/_doc/u1", "").Body.String(); !strings.Contains(body, `"cpf":"ndb:e1:k2:`) {
		t.Fatalf("u1: %s", body)
	}

	db.Close()

	db, h, err = openEnv(t, dir, k2, "")
	if err != nil {
		t.Fatalf("a start with the new key alone: %v", err)
	}

	defer db.Close()

	if body := call(h, "POST", "/users/_search?decrypt=1", `{"query":{"cpf":"39053344705"}}`).Body.String(); !strings.Contains(body, "390.533.447-05") {
		t.Fatalf("search: %s", body)
	}
}
