package settings_test

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thadeu/ndb/internal/settings"
)

// A stored key rotates in place. A restart during the rotation keeps both
// keys; Finish drops the old one.
func TestAStoredKeyRotates(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)

	k1, err := settings.LoadKey(s, "")
	if err != nil {
		t.Fatal(err)
	}

	k2, err := settings.Rotate(s, k1)
	if err != nil {
		t.Fatal(err)
	}

	if k2.ID != "k2" || k2.Old == nil || k2.Old.ID != "k1" || hex.EncodeToString(k2.Old.Master()) != hex.EncodeToString(k1.Master()) {
		t.Fatalf("rotated %+v", k2)
	}

	if _, err := settings.Rotate(s, k2); err == nil {
		t.Fatal("a second rotation started before the first one ended")
	}

	again, err := settings.LoadKey(open(t, dir), "")
	if err != nil || again.ID != "k2" || again.Old == nil || again.Old.ID != "k1" {
		t.Fatalf("restart during the rotation: %+v %v", again, err)
	}

	if err := settings.Finish(open(t, dir)); err != nil {
		t.Fatal(err)
	}

	done, err := settings.LoadKey(open(t, dir), "")
	if err != nil || done.ID != "k2" || done.Old != nil {
		t.Fatalf("after Finish: %+v %v", done, err)
	}

	raw, _ := os.ReadFile(filepath.Join(dir, settings.Dir, settings.File))

	if strings.Contains(string(raw), hex.EncodeToString(k1.Master())) || !strings.Contains(string(raw), hex.EncodeToString(k2.Master())) {
		t.Fatal("after Finish the file must hold the new key and not the old one")
	}
}

// An env rotation: PlanEnv gives the new key and keeps its fingerprint. The
// next start with that key in NDB_ENCRYPTION_KEY and the old one in
// NDB_ENCRYPTION_KEY_OLD rotates. Another key is still refused.
func TestAnEnvKeyRotatesAtTheNextStart(t *testing.T) {
	dir := t.TempDir()
	oldEnv := strings.Repeat("ab", 32)

	k1, err := settings.LoadKey(open(t, dir), oldEnv)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := settings.Rotate(open(t, dir), k1); err == nil {
		t.Fatal("an env key rotated in the settings")
	}

	newEnv, err := settings.PlanEnv(open(t, dir), k1)
	if err != nil || len(newEnv) != 64 {
		t.Fatalf("plan %q %v", newEnv, err)
	}

	if _, err := settings.LoadKeys(open(t, dir), strings.Repeat("cd", 32), oldEnv); err == nil {
		t.Fatal("a key that was not planned was accepted")
	}

	if _, err := settings.LoadKeys(open(t, dir), newEnv, ""); err == nil || !strings.Contains(err.Error(), "NDB_ENCRYPTION_KEY_OLD") {
		t.Fatalf("a rotation with no old key: %v", err)
	}

	k2, err := settings.LoadKeys(open(t, dir), newEnv, oldEnv)
	if err != nil || k2.ID != "k2" || k2.Source != "env" || k2.Old == nil || hex.EncodeToString(k2.Old.Master()) != oldEnv {
		t.Fatalf("rotated %+v %v", k2, err)
	}

	if _, err := settings.LoadKeys(open(t, dir), newEnv, ""); err == nil {
		t.Fatal("a start during the rotation with no old key was accepted")
	}

	if err := settings.Finish(open(t, dir)); err != nil {
		t.Fatal(err)
	}

	if k, err := settings.LoadKeys(open(t, dir), newEnv, ""); err != nil || k.Old != nil || k.ID != "k2" {
		t.Fatalf("after Finish: %+v %v", k, err)
	}

	raw, _ := os.ReadFile(filepath.Join(dir, settings.Dir, settings.File))

	if strings.Contains(string(raw), newEnv) || strings.Contains(string(raw), oldEnv) {
		t.Fatal("an env key was written to the settings")
	}
}

// A stored key moves to the env: the old key comes from the settings, and
// the settings no longer hold a key once the rotation ends.
func TestAStoredKeyMovesToTheEnv(t *testing.T) {
	dir := t.TempDir()

	k1, err := settings.LoadKey(open(t, dir), "")
	if err != nil {
		t.Fatal(err)
	}

	newEnv, err := settings.PlanEnv(open(t, dir), k1)
	if err != nil {
		t.Fatal(err)
	}

	k2, err := settings.LoadKeys(open(t, dir), newEnv, "")
	if err != nil || k2.ID != "k2" || k2.Old == nil || hex.EncodeToString(k2.Old.Master()) != hex.EncodeToString(k1.Master()) {
		t.Fatalf("rotated %+v %v", k2, err)
	}

	if err := settings.Finish(open(t, dir)); err != nil {
		t.Fatal(err)
	}

	if _, err := settings.LoadKey(open(t, dir), ""); err == nil {
		t.Fatal("the data dir started with no env after the move")
	}

	raw, _ := os.ReadFile(filepath.Join(dir, settings.Dir, settings.File))

	if strings.Contains(string(raw), hex.EncodeToString(k1.Master())) {
		t.Fatal("the settings file still holds the old key")
	}
}
