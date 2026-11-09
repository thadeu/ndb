package ndb

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
)

// Testing is the test support, like Ndb::Testing in Rails. ndb has no
// transactions, so a test cannot roll back: clean the collections a test
// used after it, or after the suite. A truncate takes a few ms at any size
// and keeps the definition and the indexes.
//
//	func TestMain(m *testing.M) {
//		code := m.Run()
//
//		ndb.Testing.Clean(context.Background(), db)
//		os.Exit(code)
//	}
//
//	t.Cleanup(func() { ndb.Testing.Clean(ctx, db) })
//
// Every clean refuses to run outside go test (testing.Testing), a
// safeguard against a clean of production data: set NDB_ALLOW_CLEAN=1 to
// force it.
var Testing testingSupport

// ErrUnsafeClean is the answer of a clean outside go test.
var ErrUnsafeClean = errors.New("ndb: test clean refused: this is not go test (set NDB_ALLOW_CLEAN=1 to force it)")

type testingSupport struct{}

// CleanOption changes which collections a clean truncates.
type CleanOption func(*cleanOptions)

type cleanOptions struct {
	only   []string
	except []string
}

// CleanOnly truncates these collections, not the ones the DB used.
func CleanOnly(names ...string) CleanOption {
	return func(o *cleanOptions) { o.only = names }
}

// CleanExcept leaves these collections out of a clean.
func CleanExcept(names ...string) CleanOption {
	return func(o *cleanOptions) { o.except = append(o.except, names...) }
}

// Clean truncates the collections that db used since the last clean (or
// CleanOnly), less CleanExcept, and gives their names. A collection that
// does not exist, or is in direct mode, is left out without an error.
func (testingSupport) Clean(ctx context.Context, db *DB, opts ...CleanOption) ([]string, error) {
	if err := guardClean(); err != nil {
		return nil, err
	}

	var o cleanOptions

	for _, opt := range opts {
		opt(&o)
	}

	names := o.only

	if names == nil {
		names = db.takeUsed()
	}

	var cleaned []string

	for _, name := range names {
		if slices.Contains(o.except, name) {
			continue
		}

		err := truncateForTest(ctx, db, name)

		if err != nil {
			return cleaned, err
		}

		cleaned = append(cleaned, name)
	}

	return cleaned, nil
}

// CleanAll truncates every engine collection of the daemon whose name has
// the prefix of db (WithPrefix), less CleanExcept: for the start of a run.
// Direct collections and partitions are left out.
func (t testingSupport) CleanAll(ctx context.Context, db *DB, opts ...CleanOption) ([]string, error) {
	if err := guardClean(); err != nil {
		return nil, err
	}

	defs, err := db.Collections(ctx)

	if err != nil {
		return nil, err
	}

	var names []string

	for _, def := range defs {
		name, _ := def["name"].(string)

		if def["write"] == "direct" || def["partition_of"] != nil || !strings.HasPrefix(name, db.prefix) {
			continue
		}

		names = append(names, name)
	}

	return t.Clean(ctx, db, append(opts, CleanOnly(names...))...)
}

func guardClean() error {
	if testing.Testing() || os.Getenv("NDB_ALLOW_CLEAN") == "1" {
		return nil
	}

	return ErrUnsafeClean
}

func truncateForTest(ctx context.Context, db *DB, name string) error {
	resp, err := db.do(ctx, http.MethodPost, collPath(name)+"/_truncate?confirm="+url.QueryEscape(name), nil)

	if errors.Is(err, ErrUnknownCollection) || errors.Is(err, ErrDirectCollection) || errors.Is(err, ErrNotSupported) {
		return nil
	}

	if err != nil {
		return err
	}

	return resp.Body.Close()
}

// WithPrefix puts prefix before the name of every Collection of the DB:
// "test1_" gives each parallel package or worker collections of its own.
func WithPrefix(prefix string) ConnectOption {
	return func(db *DB) { db.prefix = prefix }
}

// use records that the DB used a collection, for Testing.Clean. A pattern
// (logs-*) is not a collection to clean.
func (db *DB) use(name string) {
	if strings.HasSuffix(name, "*") {
		return
	}

	db.mu.Lock()
	defer db.mu.Unlock()

	if db.used == nil {
		db.used = map[string]bool{}
	}

	db.used[name] = true
}

// takeUsed gives the collections the DB used and forgets them.
func (db *DB) takeUsed() []string {
	db.mu.Lock()
	defer db.mu.Unlock()

	names := make([]string, 0, len(db.used))

	for name := range db.used {
		names = append(names, name)
	}

	db.used = nil

	slices.Sort(names)

	return names
}
