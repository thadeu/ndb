package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/config"
)

func write(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "ndb.yml")

	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestLoadFull(t *testing.T) {
	path := write(t, `
data_dir: /var/lib/ndb
listen:
  unix: /run/ndb.sock
  tcp: 127.0.0.1:7070
memory_limit: 1GiB
allow_implicit: true

collections:
  calls:
    path: /data/fsw-esl/calls/**/*.ndjson
    id: call_id
    time: ts
    indexes: [status, dnis]
  outbox:
    write: engine
    id: event_id
    durability: strict
    max_line_size: 2MiB
`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.DataDir != "/var/lib/ndb" || cfg.Listen.TCP != "127.0.0.1:7070" || !cfg.AllowImplicit {
		t.Fatalf("got %+v", cfg)
	}

	if cfg.MemoryLimit != 1<<30 {
		t.Fatalf("memory_limit = %d", cfg.MemoryLimit)
	}

	calls := cfg.Collections["calls"]

	if calls.Name != "calls" || calls.ID != "call_id" || len(calls.Indexes) != 2 {
		t.Fatalf("calls = %+v", calls)
	}

	if cfg.Collections["outbox"].MaxLineSize != 2<<20 {
		t.Fatalf("outbox = %+v", cfg.Collections["outbox"])
	}
}

func TestDefaults(t *testing.T) {
	cfg, err := config.Load(write(t, "{}"))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.DataDir != "/var/lib/ndb" || cfg.Listen.Unix != "/run/ndb.sock" {
		t.Fatalf("got %+v", cfg)
	}
}

func TestMissingFileIsNotAnError(t *testing.T) {
	cfg, err := config.Load(filepath.Join(t.TempDir(), "nope.yml"))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.DataDir == "" {
		t.Fatal("defaults not applied")
	}
}

func TestUnknownKeyIsAnError(t *testing.T) {
	_, err := config.Load(write(t, "collections:\n  a:\n    indexs: [x]\n"))

	if !errors.Is(err, config.ErrInvalid) {
		t.Fatalf("typo not caught: %v", err)
	}
}

func TestApplyToCatalog(t *testing.T) {
	cfg, _ := config.Load(write(t, "collections:\n  logs:\n    path: /l/*.ndjson\n    retention: 30d\n"))

	cat, err := catalog.Open(filepath.Join(t.TempDir(), "catalog.bolt"), catalog.Options{})
	if err != nil {
		t.Fatal(err)
	}

	defer cat.Close()

	if err := cfg.Apply(cat); err != nil {
		t.Fatal(err)
	}

	def, ok := cat.Get("logs")

	if !ok || def.Write != catalog.Direct || def.Retention != "30d" || !def.OpsFromYAML {
		t.Fatalf("def=%+v ok=%v", def, ok)
	}
}
