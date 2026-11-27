package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"

	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/streams"
)

// DatabaseHeader names the database of a request. No header, or
// "default", is the data dir itself, as before databases existed.
const DatabaseHeader = "Ndb-Database"

type databaseKey struct{}

// requestDB is the database a request resolved: its name and its engine.
type requestDB struct {
	name string
	db   *engine.DB
}

// databaseOf is the database of the request (zero for the default).
func databaseOf(r *http.Request) requestDB {
	got, _ := r.Context().Value(databaseKey{}).(requestDB)

	return got
}

func withDatabase(ctx context.Context, name string, db *engine.DB) context.Context {
	return context.WithValue(ctx, databaseKey{}, requestDB{name: name, db: db})
}

// headerDatabase is the name in the header; "default" is "".
func headerDatabase(r *http.Request) string {
	name := r.Header.Get(DatabaseHeader)

	if name == engine.DefaultDatabase {
		return ""
	}

	return name
}

// database gives the engine of the database the request names. A write
// makes a database that does not exist; a read answers
// database_not_found and makes nothing.
func (s *Server) database(r *http.Request, root *engine.DB) (*engine.DB, *http.Request, error) {
	name := headerDatabase(r)

	if name == "" {
		return root, r, nil
	}

	segs, _ := segments(r.URL.EscapedPath())
	_, need := required(r.Method, segs)

	// A copy reads the stream of a database; it never makes one.
	if len(segs) > 0 && segs[0] == "_replication" {
		need = accessRead
	}

	db, err := root.Database(name, need >= accessWrite && root.Implicit())

	if errors.Is(err, engine.ErrUnknownDatabase) && need >= accessWrite {
		err = &apiError{status: http.StatusNotFound, code: "database_not_found", msg: "no database " + name + " (allow_implicit is off): create it with PUT /_databases/" + name}
	}

	if err != nil {
		return nil, r, err
	}

	return db, r.WithContext(withDatabase(r.Context(), name, db)), nil
}

// dbStreams are the streams services of the databases other than the
// default one (the default one is Server.streams). Each holds the engine it
// was made for: a database dropped and made again gets a new service.
type dbStreams struct {
	mu   sync.Mutex
	byDB map[string]dbStream
	shut bool
}

type dbStream struct {
	db  *engine.DB
	svc *streams.Service
}

// streamsFor is the streams service of the database of the request. A
// request whose database was dropped after it started gets a service of its
// own engine, which answers closed, and that service is not kept.
func (s *Server) streamsFor(r *http.Request) *streams.Service {
	got := databaseOf(r)

	if got.name == "" {
		return s.streams
	}

	if cur, err := s.db.Load().Database(got.name, false); err != nil || cur != got.db {
		return streams.New(func() *engine.DB { return got.db })
	}

	return s.streamsOf(got.name, got.db)
}

// streamsOf is the service of the database name, for its engine db.
func (s *Server) streamsOf(name string, db *engine.DB) *streams.Service {
	s.dbStreams.mu.Lock()
	defer s.dbStreams.mu.Unlock()

	if cur, ok := s.dbStreams.byDB[name]; ok && cur.db == db {
		return cur.svc
	}

	if cur, ok := s.dbStreams.byDB[name]; ok {
		cur.svc.Close()
	}

	svc := streams.New(func() *engine.DB { return db })

	if s.dbStreams.shut {
		svc.Close()

		return svc
	}

	if s.dbStreams.byDB == nil {
		s.dbStreams.byDB = map[string]dbStream{}
	}

	s.dbStreams.byDB[name] = dbStream{db: db, svc: svc}

	return svc
}

// closeStreams closes the streams service of one database ("" = all).
func (s *Server) closeStreams(name string, all bool) {
	s.dbStreams.mu.Lock()
	defer s.dbStreams.mu.Unlock()

	if all {
		s.dbStreams.shut = true
	}

	for n, cur := range s.dbStreams.byDB {
		if all || n == name {
			cur.svc.Close()
			delete(s.dbStreams.byDB, n)
		}
	}
}

// snapshotsOf gives the stream snapshots of another database, for a backup
// of the whole daemon.
func (s *Server) snapshotsOf(root *engine.DB) func(string) func(string) ([]byte, error) {
	return func(name string) func(string) ([]byte, error) {
		db, err := root.Database(name, false)
		if err != nil {
			return nil
		}

		return s.streamsOf(name, db).Snapshot
	}
}

// listDatabases is GET /_databases: the default database first, then the
// others by name, each with its count of collections.
func (s *Server) listDatabases(w http.ResponseWriter, _ *http.Request, root *engine.DB) {
	type entry struct {
		Name        string `json:"name"`
		Collections int    `json:"collections"`
	}

	out := []entry{{Name: engine.DefaultDatabase, Collections: len(root.Definitions())}}

	for _, name := range root.Databases() {
		db, err := root.Database(name, false)
		if err != nil {
			continue
		}

		out = append(out, entry{Name: name, Collections: len(db.Definitions())})
	}

	writeJSON(w, http.StatusOK, map[string]any{"databases": out})
}

// createDatabase is PUT /_databases/{name}: 201 when it makes the database,
// 200 when it exists ("default" always does).
func (s *Server) createDatabase(w http.ResponseWriter, r *http.Request, root *engine.DB) {
	name := r.PathValue("database")

	if name != engine.DefaultDatabase && !engine.ValidDatabase(name) {
		writeError(w, engine.ErrInvalidDatabase)

		return
	}

	if name == engine.DefaultDatabase {
		writeJSON(w, http.StatusOK, map[string]any{"database": name, "created": false})

		return
	}

	_, err := root.Database(name, false)
	existed := err == nil

	if _, err := root.Database(name, true); err != nil {
		writeError(w, err)

		return
	}

	status := http.StatusOK

	if !existed {
		status = http.StatusCreated

		s.logInfo(w, r, "database created", slog.String("database", name))
	}

	writeJSON(w, status, map[string]any{"database": name, "created": !existed})
}

// dropDatabase is DELETE /_databases/{name}?confirm=<name>: the streams of
// the database close, then its engine, then its dir goes.
func (s *Server) dropDatabase(w http.ResponseWriter, r *http.Request, root *engine.DB) {
	name := r.PathValue("database")

	if name != engine.DefaultDatabase && !engine.ValidDatabase(name) {
		writeError(w, engine.ErrInvalidDatabase)

		return
	}

	if r.URL.Query().Get("confirm") != name {
		writeError(w, &apiError{status: http.StatusBadRequest, code: "confirm_required", msg: "confirm must be the database name: ?confirm=" + name})

		return
	}

	if name != engine.DefaultDatabase {
		if _, err := root.Database(name, false); err != nil {
			writeError(w, err)

			return
		}
	}

	s.closeStreams(name, false)

	if err := root.DropDatabase(name); err != nil {
		writeError(w, err)

		return
	}

	s.logInfo(w, r, "database dropped", slog.String("database", name))

	writeJSON(w, http.StatusOK, map[string]string{"dropped": name})
}

// databaseCode is the API code of a database error, or "".
func databaseCode(err error) string {
	switch {
	case errors.Is(err, engine.ErrInvalidDatabase):
		return "invalid_database"
	case errors.Is(err, engine.ErrUnknownDatabase):
		return "database_not_found"
	case errors.Is(err, engine.ErrDefaultDatabase), errors.Is(err, engine.ErrNestedDatabase), errors.Is(err, engine.ErrNotDefaultDatabase):
		return "invalid_request"
	}

	return ""
}
