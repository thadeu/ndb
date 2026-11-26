package server

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/thadeu/ndb/internal/config"
	"github.com/thadeu/ndb/internal/engine"
)

func TestDatabaseRoutesWaitForRecovery(t *testing.T) {
	srv := New()

	rec := httptest.NewRecorder()

	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/_databases", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

func TestStreamsOfAGoneDatabase(t *testing.T) {
	db, err := engine.Open(config.Config{DataDir: t.TempDir(), AllowImplicit: true}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { db.Close() })

	srv := New()
	srv.Attach(db)

	if got := srv.snapshotsOf(db)("nope"); got != nil {
		t.Fatal("snapshots of an unknown database")
	}

	app, err := db.Database("app", true)
	if err != nil {
		t.Fatal(err)
	}

	first := srv.streamsOf("app", app)

	if srv.streamsOf("app", app) != first {
		t.Fatal("a second call made another service")
	}

	if err := db.DropDatabase("app"); err != nil {
		t.Fatal(err)
	}

	again, err := db.Database("app", true)
	if err != nil {
		t.Fatal(err)
	}

	if srv.streamsOf("app", again) == first {
		t.Fatal("the new database got the service of the old one")
	}

	stale := httptest.NewRequest(http.MethodGet, "/", nil)
	stale = stale.WithContext(withDatabase(stale.Context(), "app", app))

	svc := srv.streamsFor(stale)

	if _, err := svc.Info(t.Context(), "events"); err == nil {
		t.Fatal("the streams of a dropped database answer")
	}

	if srv.dbStreams.byDB["app"].db != again {
		t.Fatal("a stale request took the service of the new database")
	}

	if _, err := db.Database("other", true); err != nil {
		t.Fatal(err)
	}

	gone := httptest.NewRequest(http.MethodGet, "/", nil)
	gone = gone.WithContext(withDatabase(gone.Context(), "nope", app))

	if _, err := srv.streamsFor(gone).Info(t.Context(), "events"); err == nil {
		t.Fatal("the streams of an unknown database answer")
	}

	srv.closeStreams("", true)

	if svc := srv.streamsOf("late", again); svc == nil {
		t.Fatal("no service after the shutdown")
	}

	if len(srv.dbStreams.byDB) != 0 {
		t.Fatal("a service was kept after the shutdown")
	}
}
