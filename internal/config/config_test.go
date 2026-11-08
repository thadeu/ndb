package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

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
write_buffer: 16MiB
allow_implicit: true

collections:
  calls:
    path: /data/fsw-esl/calls/**/*.ndjson
    time: ts
    indexes: [status, dnis]
  outbox:
    write: engine
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

	if cfg.MemoryLimit != 1<<30 || cfg.WriteBuffer != 16<<20 {
		t.Fatalf("memory_limit = %d, write_buffer = %d", cfg.MemoryLimit, cfg.WriteBuffer)
	}

	calls := cfg.Collections["calls"]

	if calls.Name != "calls" || len(calls.Indexes) != 2 {
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

	if cfg.DataDir != "/var/lib/ndb" || cfg.Listen.Unix != "/run/ndb/ndb.sock" {
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

func TestGroupLimitIsASize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ndb.yml")

	if err := os.WriteFile(path, []byte("group_limit: 64MiB\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.GroupLimit != 64<<20 {
		t.Fatalf("GroupLimit = %d", cfg.GroupLimit)
	}
}

func TestLoadTTLAndExpiresField(t *testing.T) {
	cfg, err := config.Load(write(t, "collections:\n  a:\n    time: at\n    ttl: 24h\n  b:\n    expires_field: expires_at\n"))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Collections["a"].TTL != "24h" || cfg.Collections["b"].ExpiresField != "expires_at" {
		t.Fatalf("collections: %+v", cfg.Collections)
	}
}

func TestBlobMaxSizeIsASize(t *testing.T) {
	cfg, err := config.Load(write(t, "blob_max_size: 256MiB\n"))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.BlobMaxSize != 256<<20 {
		t.Fatalf("BlobMaxSize = %d", cfg.BlobMaxSize)
	}
}

func TestRelativePathsResolveAgainstTheConfigDir(t *testing.T) {
	path := write(t, `
data_dir: ./data
listen:
  unix: ./ndb.sock
collections:
  events:
    write: direct
    path: ./logs/*.ndjson
  other:
    write: direct
    path: /abs/*.ndjson
`)

	dir := filepath.Dir(path)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.DataDir != filepath.Join(dir, "data") || cfg.Listen.Unix != filepath.Join(dir, "ndb.sock") {
		t.Fatalf("got data_dir %q unix %q", cfg.DataDir, cfg.Listen.Unix)
	}

	if got := cfg.Collections["events"].Path; got != filepath.Join(dir, "logs", "*.ndjson") {
		t.Fatalf("events path = %q", got)
	}

	if got := cfg.Collections["other"].Path; got != "/abs/*.ndjson" {
		t.Fatalf("absolute path changed: %q", got)
	}
}

func TestDefaultsMatchTheImageAndTheUnit(t *testing.T) {
	cfg, err := config.Load(filepath.Join(t.TempDir(), "missing.yml"))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Listen.Unix != "/run/ndb/ndb.sock" || cfg.Listen.UnixMode != 0o660 {
		t.Fatalf("got unix %q mode %o", cfg.Listen.Unix, cfg.Listen.UnixMode)
	}
}

func TestUnixModeFromTheFile(t *testing.T) {
	cfg, err := config.Load(write(t, "listen:\n  unix_mode: \"0600\"\n"))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Listen.UnixMode != 0o600 {
		t.Fatalf("mode %o", cfg.Listen.UnixMode)
	}

	if _, err := config.Load(write(t, "listen:\n  unix_mode: \"rw\"\n")); err == nil {
		t.Fatal("a bad unix_mode must fail")
	}
}

func TestFindPath(t *testing.T) {
	dir := t.TempDir()
	etc := filepath.Join(dir, "etc.yml")

	t.Chdir(dir)

	if got := config.FindPath("", etc); got != etc {
		t.Fatalf("no file anywhere: got %q, want the etc path %q", got, etc)
	}

	if err := os.WriteFile(filepath.Join(dir, "ndb.yml"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := config.FindPath("", etc); got != "ndb.yml" {
		t.Fatalf("local file: got %q", got)
	}

	t.Setenv("NDB_CONFIG", "/x/from-env.yml")

	if got := config.FindPath("", etc); got != "/x/from-env.yml" {
		t.Fatalf("env: got %q", got)
	}

	if got := config.FindPath("flag.yml", etc); got != "flag.yml" {
		t.Fatalf("flag: got %q", got)
	}
}

func TestLoadTokens(t *testing.T) {
	path := write(t, `
tokens:
  - name: esl
    file: tokens/esl.token
    write: [outbox, "calls*"]
  - name: dash
    file: /etc/ndb/dash.token
    read: ["calls*"]
`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(cfg.Tokens) != 2 || cfg.Tokens[0].Name != "esl" || len(cfg.Tokens[0].Write) != 2 || cfg.Tokens[1].Read[0] != "calls*" {
		t.Fatalf("tokens = %+v", cfg.Tokens)
	}

	if want := filepath.Join(filepath.Dir(path), "tokens", "esl.token"); cfg.Tokens[0].File != want {
		t.Fatalf("file = %s, want %s (relative to ndb.yml)", cfg.Tokens[0].File, want)
	}

	for _, bad := range []string{
		"tokens:\n  - file: a\n    read: [x]\n",
		"tokens:\n  - name: a\n    read: [x]\n",
		"tokens:\n  - name: a\n    file: a\n",
		"tokens:\n  - name: a\n    file: a\n    read: [x]\n  - name: a\n    file: b\n    read: [y]\n",
		"tokens:\n  - name: a\n    file: a\n    read: [\"[\"]\n",
	} {
		if _, err := config.Load(write(t, bad)); !errors.Is(err, config.ErrInvalid) {
			t.Fatalf("%q: err = %v, want ErrInvalid", bad, err)
		}
	}
}

func TestParseIdleClose(t *testing.T) {
	for in, want := range map[string]time.Duration{"": 10 * time.Minute, "off": 0, "2s": 2 * time.Second, "1d": 24 * time.Hour} {
		got, err := config.ParseIdleClose(in)
		if err != nil || got != want {
			t.Fatalf("ParseIdleClose(%q) = %s, %v; want %s", in, got, err, want)
		}
	}

	for _, in := range []string{"500ms", "soon"} {
		if _, err := config.ParseIdleClose(in); err == nil {
			t.Fatalf("ParseIdleClose(%q): want an error", in)
		}
	}
}
