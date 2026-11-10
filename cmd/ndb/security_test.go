package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/config"
)

// A bad value of a security flag stops the start, before any listener.
func TestSecurityFlagsAreChecked(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "absent.yml")

	cases := map[string][]string{
		"-cors-origin":  {"-cors-origin", "app.example.com"},
		"-allowed-host": {"-allowed-host", "ndb.internal:7070"},
		"-max-conns":    {"-max-conns", "-1"},
	}

	for name, flags := range cases {
		err := run(append([]string{"serve", "-config", absent}, flags...))

		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%v: err = %v, want one that names %s", flags, err, name)
		}
	}
}

func TestSecurityFromConfig(t *testing.T) {
	cfg := config.Config{
		CORSOrigins:  []string{"https://app.example.com"},
		AllowedHosts: []string{"ndb.internal"},
		TrustProxy:   true,
		AuthFailures: 3,
		AuthWindow:   time.Second,
		AuthLockout:  time.Hour,
		MaxWatches:   7,
	}

	sec := security(cfg)

	if sec.CORSOrigins[0] != "https://app.example.com" || sec.AllowedHosts[0] != "ndb.internal" || !sec.TrustProxy {
		t.Fatalf("got %+v", sec)
	}

	if sec.AuthFailures != 3 || sec.AuthWindow != time.Second || sec.AuthLockout != time.Hour || sec.MaxWatches != 7 {
		t.Fatalf("got %+v", sec)
	}

	var f listFlag

	_ = f.Set("a")
	_ = f.Set("b")

	if f.String() != "a,b" {
		t.Fatalf("listFlag = %q", f.String())
	}
}
