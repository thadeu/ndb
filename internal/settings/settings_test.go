package settings_test

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thadeu/ndb/internal/settings"
)

func open(t *testing.T, dir string) *settings.Store {
	t.Helper()

	s, err := settings.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	return s
}

func TestSetGetAndRestart(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)

	if _, ok := s.Get("log.level"); ok {
		t.Fatal("a new store has a value")
	}

	if err := s.Set(map[string]json.RawMessage{"log.level": json.RawMessage(`"debug"`), "log.slow_ms": json.RawMessage(`250`)}); err != nil {
		t.Fatal(err)
	}

	if err := s.Set(map[string]json.RawMessage{"log.level": json.RawMessage(`"warn"`)}); err != nil {
		t.Fatal(err)
	}

	s = open(t, dir)

	if v, _ := s.Get("log.level"); string(v) != `"warn"` {
		t.Fatalf("log.level = %s, want the last value", v)
	}

	if v, _ := s.Get("log.slow_ms"); string(v) != `250` {
		t.Fatalf("log.slow_ms = %s", v)
	}

	if all := s.All(); len(all) != 2 {
		t.Fatalf("All = %v", all)
	}
}

func TestACutLastLineIsDropped(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)

	if err := s.Set(map[string]json.RawMessage{"a": json.RawMessage(`1`)}); err != nil {
		t.Fatal(err)
	}

	f, err := os.OpenFile(filepath.Join(dir, settings.Dir, settings.File), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}

	f.WriteString(`{"_tx":2,"_ts":"2026-11-11T00:00:00Z","_op":"put","_id":"a","_v":2,"doc":{"value":`)
	f.Close()

	s = open(t, dir)

	if v, _ := s.Get("a"); string(v) != `1` {
		t.Fatalf("a = %s, want 1 (the cut line dropped)", v)
	}

	if err := s.Set(map[string]json.RawMessage{"a": json.RawMessage(`3`)}); err != nil {
		t.Fatal(err)
	}

	if v, _ := open(t, dir).Get("a"); string(v) != `3` {
		t.Fatalf("a = %s after a write past the cut line", v)
	}
}

func TestKeyIsMadeOnceThenKept(t *testing.T) {
	dir := t.TempDir()

	k1, err := settings.LoadKey(open(t, dir), "")
	if err != nil {
		t.Fatal(err)
	}

	if k1.Source != "made" || k1.ID != "k1" {
		t.Fatalf("key %+v", k1)
	}

	k2, err := settings.LoadKey(open(t, dir), "")
	if err != nil {
		t.Fatal(err)
	}

	if k2.Source != "stored" || hex.EncodeToString(k2.Master()) != hex.EncodeToString(k1.Master()) || !k2.Created.Equal(k1.Created) {
		t.Fatalf("second start: %+v, want the stored key", k2)
	}
}

func TestKeyFromTheEnv(t *testing.T) {
	env := strings.Repeat("ab", 32)
	dir := t.TempDir()

	k, err := settings.LoadKey(open(t, dir), env)
	if err != nil || k.Source != "env" || hex.EncodeToString(k.Master()) != env {
		t.Fatalf("key %+v, err %v", k, err)
	}

	raw, _ := os.ReadFile(filepath.Join(dir, settings.Dir, settings.File))

	if strings.Contains(string(raw), env) {
		t.Fatal("an env key was written to the settings")
	}

	if _, err := settings.LoadKey(open(t, dir), ""); err == nil || !strings.Contains(err.Error(), "NDB_ENCRYPTION_KEY") {
		t.Fatalf("a data dir of an env key started with no env: %v", err)
	}

	if _, err := settings.LoadKey(open(t, dir), strings.Repeat("cd", 32)); err == nil {
		t.Fatal("another env key was accepted")
	}

	made := t.TempDir()

	if _, err := settings.LoadKey(open(t, made), ""); err != nil {
		t.Fatal(err)
	}

	if _, err := settings.LoadKey(open(t, made), env); err == nil {
		t.Fatal("an env key that differs from the stored one was accepted")
	}

	for _, bad := range []string{"abc", strings.Repeat("zz", 32), strings.Repeat("ab", 31)} {
		if _, err := settings.LoadKey(open(t, t.TempDir()), bad); err == nil {
			t.Errorf("env %q was accepted", bad)
		}
	}
}

func TestMaskAndDerive(t *testing.T) {
	env := "0123456789abcdef" + strings.Repeat("00", 20) + "fedcba98"

	k, err := settings.LoadKey(open(t, t.TempDir()), env)
	if err != nil {
		t.Fatal(err)
	}

	if m := k.Masked(); m != "0123****…****ba98" {
		t.Fatalf("masked %q", m)
	}

	if strings.Contains(k.Masked(), env[4:60]) {
		t.Fatal("the mask shows the middle of the key")
	}

	a, b := k.Derive("ndb:e1"), k.Derive("ndb:h1")

	if len(a) != 32 || len(b) != 32 || string(a) == string(b) || string(a) == string(k.Master()) {
		t.Fatal("the derived keys are not 32 bytes, or not different")
	}
}
