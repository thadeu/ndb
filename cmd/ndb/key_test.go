package main

import (
	"strings"
	"testing"
)

func TestKeyCommandRotatesAndShowsTheState(t *testing.T) {
	dir := seedMixed(t, t.TempDir())
	sock, _ := startDaemon(t, dir)

	base := []string{"-config", "none.yml", "-url", "unix://" + sock}
	run := func(args ...string) (string, error) {
		var out strings.Builder

		err := key(append(args, base...), &out)

		return out.String(), err
	}

	if out, err := run("status"); err != nil || !strings.Contains(out, "k1") {
		t.Fatalf("status: %q %v", out, err)
	}

	out, err := run("rotate", "-env")
	if err != nil || !strings.Contains(out, "NDB_ENCRYPTION_KEY=") || !strings.Contains(out, "NDB_ENCRYPTION_KEY_OLD") {
		t.Fatalf("rotate -env: %q %v", out, err)
	}

	if out, err := run("rotate"); err != nil || !strings.Contains(out, "k2") {
		t.Fatalf("rotate: %q %v", out, err)
	}

	if _, err := run("nope"); err == nil {
		t.Fatal("an unknown verb must fail")
	}
}
