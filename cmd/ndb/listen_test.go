package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
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

// A socket that answers belongs to a running ndb: a second daemon does not
// remove it (that daemon would become unreachable). A dead one is removed.
func TestRemoveStaleSocketKeepsALiveOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ndb.sock")

	live, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}

	if err := removeStaleSocket(path); err == nil || !strings.Contains(err.Error(), "another ndb") {
		t.Fatalf("a live socket: %v, want an error", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the live socket was removed: %v", err)
	}

	live.(*net.UnixListener).SetUnlinkOnClose(false)
	live.Close()

	if err := removeStaleSocket(path); err != nil {
		t.Fatalf("a dead socket: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("the dead socket is still there")
	}
}
