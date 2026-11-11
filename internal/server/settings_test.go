package server_test

import (
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/thadeu/ndb/internal/logging"
)

type settingsAnswer struct {
	Encryption struct {
		Key         string `json:"key"`
		KeyID       string `json:"key_id"`
		Fingerprint string `json:"fingerprint"`
		Source      string `json:"source"`
		CreatedAt   string `json:"created_at"`
	} `json:"encryption"`
	Log map[string]struct {
		Value  any    `json:"value"`
		Source string `json:"source"`
	} `json:"log"`
	Warnings []string `json:"warnings"`
}

func settingsOf(t *testing.T, body string) settingsAnswer {
	t.Helper()

	var out settingsAnswer

	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("settings %q: %v", body, err)
	}

	return out
}

func TestClusterSettingsShowTheKeyMaskedAndTheLogSettings(t *testing.T) {
	srv, h, buf := logServer(t, logging.Options{Level: slog.LevelInfo, BodyMax: 2048, SlowMs: 1000}, true)

	srv.SetLogSources(map[string]string{"level": "env"})

	rec := call(h, "GET", "/_cluster/settings", "")
	expectStatus(t, rec, http.StatusOK)

	got := settingsOf(t, rec.Body.String())
	master := hex.EncodeToString(srv.DB().Key().Master())

	if strings.Contains(rec.Body.String(), master) || strings.Contains(rec.Body.String(), master[4:60]) {
		t.Fatal("the answer has the key")
	}

	if got.Encryption.Key != master[:4]+"****…****"+master[60:] || got.Encryption.KeyID != "k1" || got.Encryption.Source != "made" || got.Encryption.Fingerprint == "" || got.Encryption.CreatedAt == "" {
		t.Fatalf("encryption %+v", got.Encryption)
	}

	if got.Log["level"].Value != "info" || got.Log["level"].Source != "env" || got.Log["slow_ms"].Value != float64(1000) || got.Log["slow_ms"].Source != "default" {
		t.Fatalf("log %+v", got.Log)
	}

	rec = call(h, "PUT", "/_cluster/settings", `{"log":{"level":"debug","slow_ms":250}}`)
	expectStatus(t, rec, http.StatusOK)

	got = settingsOf(t, rec.Body.String())

	if srv.Logger().Level() != slog.LevelDebug || srv.Logger().SlowMs() != 250 {
		t.Fatal("the PUT did not change the logger")
	}

	if got.Log["level"].Source != "stored" || got.Log["slow_ms"].Source != "stored" || len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0], "log.level") {
		t.Fatalf("after the PUT: %+v", got)
	}

	if v, _ := srv.DB().Settings().Get("log.level"); string(v) != `"debug"` {
		t.Fatalf("log.level was not stored: %s", v)
	}

	if strings.Contains(buf.String(), master) {
		t.Fatal("a log line has the key")
	}
}

func TestClusterSettingsRefuseWhatCannotChange(t *testing.T) {
	_, h, _ := logServer(t, logging.Options{Level: slog.LevelInfo}, true)

	for _, body := range []string{
		`{"encryption":{"key":"00"}}`,
		`{"log":{"level":"loud"}}`,
		`{"log":{"slow_ms":-1}}`,
		`{"log":{"format":"json"}}`,
		`{"nope":1}`,
		`{}`,
		`[]`,
	} {
		rec := call(h, "PUT", "/_cluster/settings", body)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("PUT %s = %d %s, want 400", body, rec.Code, rec.Body.String())
		}
	}

	expectStatus(t, call(h, "GET", "/_cluster/nope", ""), http.StatusNotFound)
	expectStatus(t, call(h, "DELETE", "/_cluster/settings", ""), http.StatusMethodNotAllowed)
}
