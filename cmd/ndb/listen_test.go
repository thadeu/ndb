package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/thadeu/ndb/internal/config"
)

func TestListenAppliesUnixMode(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "ndb.sock")

	ls, err := listen(config.Listen{Unix: sock, UnixMode: 0o660})
	if err != nil {
		t.Fatal(err)
	}

	defer ls[0].Close()

	fi, err := os.Stat(sock)
	if err != nil {
		t.Fatal(err)
	}

	if got := fi.Mode().Perm(); got != 0o660 {
		t.Fatalf("socket mode = %o, want 660", got)
	}
}

func TestParseUnixMode(t *testing.T) {
	for in, want := range map[string]os.FileMode{"0660": 0o660, "660": 0o660, "0777": 0o777} {
		got, err := parseUnixMode(in)
		if err != nil || got != want {
			t.Errorf("parseUnixMode(%q) = %o, %v; want %o", in, got, err, want)
		}
	}

	for _, in := range []string{"abc", "09", "01777", "-1"} {
		if _, err := parseUnixMode(in); err == nil {
			t.Errorf("parseUnixMode(%q) should fail", in)
		}
	}
}
