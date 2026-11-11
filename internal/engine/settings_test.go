package engine_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thadeu/ndb/internal/backup"
	"github.com/thadeu/ndb/internal/engine"
)

func TestTheKeyIsKeptAndGoesWithTheBackup(t *testing.T) {
	src := t.TempDir()

	var logs bytes.Buffer

	db, err := engine.Open(cfg(src, nil), slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}

	key := db.Key()
	master := hex.EncodeToString(key.Master())

	if key.Source != "made" || !strings.Contains(logs.String(), key.Masked()) {
		t.Fatalf("key %+v, log %q", key, logs.String())
	}

	if strings.Contains(logs.String(), master) {
		t.Fatal("the log has the whole key")
	}

	if err := db.Settings().Set(map[string]json.RawMessage{"log.level": json.RawMessage(`"debug"`)}); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer

	if err := db.Backup(ctx, &buf, backup.Options{Version: "test"}); err != nil {
		t.Fatal(err)
	}

	db.Close()

	if !strings.Contains(buf.String(), `{"_section":"settings","key":"encryption.key"`) || !strings.Contains(buf.String(), `"key":"log.level","value":"debug"`) {
		t.Fatalf("the backup has no settings: %.400s", buf.String())
	}

	db, err = engine.Open(cfg(src, nil), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	if hex.EncodeToString(db.Key().Master()) != master || db.Key().Source != "stored" {
		t.Fatalf("a restart has another key: %+v", db.Key())
	}

	db.Close()

	stage := filepath.Join(t.TempDir(), "restored")

	if err := backup.Extract(&buf, stage); err != nil {
		t.Fatal(err)
	}

	db, err = engine.Open(cfg(stage, nil), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	defer db.Close()

	if hex.EncodeToString(db.Key().Master()) != master {
		t.Fatal("the restored data dir has another key")
	}

	if v, _ := db.Settings().Get("log.level"); string(v) != `"debug"` {
		t.Fatalf("log.level = %s after the restore", v)
	}
}

func TestAnEnvKeyIsCheckedAtStart(t *testing.T) {
	dir := t.TempDir()
	env := strings.Repeat("7e", 32)

	c := cfg(dir, nil)
	c.EncryptionKey = env

	db, err := engine.Open(c, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	db.Close()

	raw, _ := os.ReadFile(filepath.Join(dir, "_system", "settings.wal"))

	if strings.Contains(string(raw), env) {
		t.Fatal("the env key was written to the data dir")
	}

	if _, err := engine.Open(cfg(dir, nil), slog.New(slog.DiscardHandler)); err == nil || !strings.Contains(err.Error(), "NDB_ENCRYPTION_KEY") {
		t.Fatalf("a start with no env: %v", err)
	}

	c.EncryptionKey = strings.Repeat("7f", 32)

	if _, err := engine.Open(c, slog.New(slog.DiscardHandler)); err == nil {
		t.Fatal("a start with another env key was accepted")
	}
}
