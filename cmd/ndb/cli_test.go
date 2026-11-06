package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/config"
	"github.com/thadeu/ndb/internal/diskusage"
	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/server"
)

var ctx = context.Background()

// seedDataDir writes rows in two collections and closes the engine, so the
// dir looks like the one of a stopped daemon.
func seedDataDir(t *testing.T, dir string) {
	t.Helper()

	db, err := engine.Open(config.Config{DataDir: dir, AllowImplicit: true}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"orders", "events"} {
		def := catalog.Def{Name: name, ID: "id"}

		if name == "orders" {
			def.Indexes = []string{"customer"}
		}

		if _, err := db.Ensure(def); err != nil {
			t.Fatal(err)
		}

		col, err := db.Get(name)
		if err != nil {
			t.Fatal(err)
		}

		for i := 0; i < 3; i++ {
			doc := fmt.Sprintf(`{"id":"%s-%d","customer":"c%d"}`, name, i, i)

			if _, err := col.Insert(ctx, []byte(doc)); err != nil {
				t.Fatal(err)
			}
		}
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

// startDaemon serves dir over a unix socket until the test ends.
func startDaemon(t *testing.T, dir string) (sock string, stop func()) {
	t.Helper()

	short, err := os.MkdirTemp("", "ndbt")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { os.RemoveAll(short) })

	sock = filepath.Join(short, "s.sock")

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}

	db, err := engine.Open(config.Config{DataDir: dir, AllowImplicit: true}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	srv := server.New()

	srv.Attach(db)

	go srv.Serve(ln)

	stopped := false
	stop = func() {
		if stopped {
			return
		}

		stopped = true

		srv.Shutdown(ctx)
		db.Close()
	}

	t.Cleanup(stop)

	return sock, stop
}

func TestVersionAndHelp(t *testing.T) {
	if err := run([]string{"version"}); err != nil {
		t.Fatal(err)
	}

	if err := run([]string{"help"}); err != nil {
		t.Fatal(err)
	}

	for _, cmd := range []string{"restore", "stats", "size", "version", "serve", "backup", "collections", "compact", "update", "delete", "expire", "persist", "ttl", "quarantine", "query"} {
		if !strings.Contains(helpText, "ndb "+cmd) {
			t.Errorf("help does not list %q", cmd)
		}
	}
}

func TestSizeReportsPartsPerCollection(t *testing.T) {
	dir := t.TempDir()

	seedDataDir(t, dir)

	var out strings.Builder

	if err := size([]string{"-config", "none.yml", "-data-dir", dir, "-json"}, &out); err != nil {
		t.Fatal(err)
	}

	var u diskusage.Usage

	if err := json.Unmarshal([]byte(out.String()), &u); err != nil {
		t.Fatal(err)
	}

	if len(u.Collections) != 2 || u.Collections[0].Name != "events" {
		t.Fatalf("collections = %+v", u.Collections)
	}

	for _, c := range u.Collections {
		if c.Bytes.WAL == 0 || c.Bytes.Meta == 0 || c.WALFiles == 0 {
			t.Errorf("%s: parts = %+v wal_files=%d", c.Name, c.Bytes, c.WALFiles)
		}
	}

	if u.Shared.Meta == 0 || u.Total <= u.Shared.Total() {
		t.Errorf("shared = %+v total = %d", u.Shared, u.Total)
	}

	out.Reset()

	if err := size([]string{"-config", "none.yml", "-data-dir", dir}, &out); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"name", "orders", "events", "(shared)", "total", "KiB"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("table lacks %q:\n%s", want, out.String())
		}
	}
}

func TestSizeMissingDataDirFails(t *testing.T) {
	var out strings.Builder

	err := size([]string{"-config", "none.yml", "-data-dir", filepath.Join(t.TempDir(), "nope")}, &out)
	if err == nil {
		t.Fatal("want an error")
	}
}

func TestStatsOfflineReadsSizesWithoutLock(t *testing.T) {
	dir := t.TempDir()

	seedDataDir(t, dir)

	var out strings.Builder

	if err := stats([]string{"-config", "none.yml", "-data-dir", dir, "-json"}, &out); err != nil {
		t.Fatal(err)
	}

	var rep statsReport

	if err := json.Unmarshal([]byte(out.String()), &rep); err != nil {
		t.Fatal(err)
	}

	if rep.Source != "data-dir" || len(rep.Collections) != 2 {
		t.Fatalf("report = %+v", rep)
	}

	if rep.Collections[0].Rows != nil || rep.Collections[0].Total == 0 || rep.Collections[0].WALFiles == 0 {
		t.Errorf("offline collection = %+v", rep.Collections[0])
	}

	out.Reset()

	if err := stats([]string{"-config", "none.yml", "-data-dir", dir}, &out); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out.String(), "collection") || !strings.Contains(out.String(), "need a running daemon") {
		t.Errorf("table:\n%s", out.String())
	}
}

func TestStatsAsksARunningDaemon(t *testing.T) {
	dir := t.TempDir()

	seedDataDir(t, dir)

	sock, _ := startDaemon(t, dir)

	var out strings.Builder

	if err := stats([]string{"-config", "none.yml", "-url", "unix://" + sock, "-json"}, &out); err != nil {
		t.Fatal(err)
	}

	var rep statsReport

	if err := json.Unmarshal([]byte(out.String()), &rep); err != nil {
		t.Fatal(err)
	}

	if len(rep.Collections) != 2 {
		t.Fatalf("report = %+v", rep)
	}

	orders := rep.Collections[1]

	if orders.Name != "orders" || orders.Rows == nil || *orders.Rows != 3 || orders.Write != "engine" {
		t.Errorf("orders = %+v", orders)
	}

	if len(orders.Indexes) != 1 || orders.Indexes[0].Field != "customer" {
		t.Errorf("indexes = %+v", orders.Indexes)
	}

	if orders.Quarantined == nil || *orders.Quarantined != 0 {
		t.Errorf("quarantined = %v", orders.Quarantined)
	}
}

func TestStatsUsesNDBURLAndRefusesBothSources(t *testing.T) {
	dir := t.TempDir()

	seedDataDir(t, dir)

	sock, _ := startDaemon(t, dir)

	t.Setenv("NDB_URL", "unix://"+sock)

	var out strings.Builder

	if err := stats([]string{"-config", "none.yml", "-data-dir", dir, "-json"}, &out); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out.String(), `"source": "data-dir"`) {
		t.Errorf("-data-dir must win over NDB_URL:\n%s", out.String())
	}

	out.Reset()

	if err := stats([]string{"-config", "none.yml", "-json"}, &out); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out.String(), `"rows": 3`) {
		t.Errorf("NDB_URL not used:\n%s", out.String())
	}

	err := stats([]string{"-url", "unix://" + sock, "-data-dir", dir}, &out)
	if err == nil {
		t.Fatal("-url with -data-dir must fail")
	}
}

func TestStatsSendsTheToken(t *testing.T) {
	dir := t.TempDir()

	seedDataDir(t, dir)

	// A TCP daemon with a token; the unix socket stays open by design.
	db, err := engine.Open(config.Config{DataDir: dir, AllowImplicit: true}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	defer db.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	srv := server.New()

	srv.SetToken("s3cret")
	srv.Attach(db)

	go srv.Serve(ln)

	defer srv.Shutdown(ctx)

	url := "tcp://" + ln.Addr().String()

	var out strings.Builder

	t.Setenv("NDB_TOKEN", "")

	if err := stats([]string{"-config", "none.yml", "-url", url}, &out); err == nil {
		t.Fatal("want 401 without a token")
	}

	t.Setenv("NDB_TOKEN", "s3cret")

	if err := stats([]string{"-config", "none.yml", "-url", url}, &out); err != nil {
		t.Fatal(err)
	}
}

func TestStatsFallsBackToDataDirWhenSocketIsDead(t *testing.T) {
	dir := t.TempDir()

	seedDataDir(t, dir)

	cfgFile := filepath.Join(t.TempDir(), "ndb.yml")
	body := fmt.Sprintf("data_dir: %s\nlisten:\n  unix: %s\n", dir, filepath.Join(t.TempDir(), "dead.sock"))

	if err := os.WriteFile(cfgFile, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder

	if err := stats([]string{"-config", cfgFile, "-json"}, &out); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out.String(), `"source": "data-dir"`) {
		t.Errorf("want the offline fallback:\n%s", out.String())
	}
}

func TestServeAliasAndFlagsStillStartTheDaemon(t *testing.T) {
	if err := run([]string{"serve", "-version"}); err != nil {
		t.Fatal(err)
	}

	if err := run([]string{"-version"}); err != nil {
		t.Fatal(err)
	}
}
