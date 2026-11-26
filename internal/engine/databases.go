package engine

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// A database is a named set of collections in one daemon: two databases
// can each have a "users" collection. The default database is the data dir
// itself, as it was before databases existed, so a data dir with no
// database has the same files as before. Each other database is a data dir
// of its own under <data_dir>/_databases/<name>, with its catalog, watch
// positions, blobs and collections. All of them share the settings, the
// encryption key, the write buffer budget and the lock of the daemon.

// DefaultDatabase is the name of the data dir itself.
const DefaultDatabase = "default"

// databasesDir holds the other databases, one dir each. A name that starts
// with "." is the left over of a drop, removed in the background.
const databasesDir = "_databases"

var databaseName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

var (
	// ErrInvalidDatabase is a name that is not [a-z0-9][a-z0-9_-]{0,62}.
	ErrInvalidDatabase = errors.New("invalid database name")

	// ErrUnknownDatabase is a database that does not exist.
	ErrUnknownDatabase = errors.New("unknown database")

	// ErrDefaultDatabase is a drop of the default database.
	ErrDefaultDatabase = errors.New("the default database cannot be dropped")

	// ErrNestedDatabase is a database asked of a database other than the
	// default one.
	ErrNestedDatabase = errors.New("a database has no databases of its own")

	// ErrNotDefaultDatabase is a daemon-wide call (a key rotation) on a
	// database other than the default one.
	ErrNotDefaultDatabase = errors.New("only the default database can do this: it is for the whole daemon")
)

// ValidDatabase tells if name can be a database: [a-z0-9][a-z0-9_-]{0,62}.
// "default" is valid: it names the data dir itself.
func ValidDatabase(name string) bool {
	return databaseName.MatchString(name)
}

// DatabaseName is the name of this database: "default" for the data dir.
func (db *DB) DatabaseName() string {
	if db.parent == nil {
		return DefaultDatabase
	}

	return db.name
}

// Database gives the database of that name. "" and "default" give db. An
// unknown name is ErrUnknownDatabase, or a new empty database with create.
func (db *DB) Database(name string, create bool) (*DB, error) {
	if db.parent != nil {
		return nil, ErrNestedDatabase
	}

	if name == "" || name == DefaultDatabase {
		return db, nil
	}

	if !ValidDatabase(name) {
		return nil, fmt.Errorf("%w: %q (use a-z, 0-9, _ and -, up to 63 characters)", ErrInvalidDatabase, name)
	}

	db.dbMu.Lock()
	defer db.dbMu.Unlock()

	if db.dbClosed {
		return nil, ErrClosed
	}

	if child, ok := db.dbs[name]; ok {
		return child, nil
	}

	if !create {
		return nil, fmt.Errorf("%w: %s", ErrUnknownDatabase, name)
	}

	child, err := db.openDatabase(name)
	if err != nil {
		return nil, err
	}

	db.log.Info("database created", "database", name)

	return child, nil
}

// Implicit tells if a write may make a database that does not exist
// (allow_implicit, the same rule as for collections).
func (db *DB) Implicit() bool {
	return db.cfg.AllowImplicit
}

// Databases lists the names of the databases other than the default one,
// sorted.
func (db *DB) Databases() []string {
	db.dbMu.Lock()
	defer db.dbMu.Unlock()

	return slices.Sorted(maps.Keys(db.dbs))
}

// DropDatabase closes the database and removes its dir in the background.
// The caller ends the users of the database first (the streams of the
// server).
func (db *DB) DropDatabase(name string) error {
	if db.parent != nil {
		return ErrNestedDatabase
	}

	if name == "" || name == DefaultDatabase {
		return ErrDefaultDatabase
	}

	db.dbMu.Lock()
	defer db.dbMu.Unlock()

	child, ok := db.dbs[name]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownDatabase, name)
	}

	delete(db.dbs, name)

	if err := child.Close(); err != nil {
		return err
	}

	root := filepath.Join(db.dir, databasesDir)
	gone := filepath.Join(root, "."+name+"-"+strconv.FormatInt(time.Now().UnixNano(), 10))

	if err := os.Rename(filepath.Join(root, name), gone); err != nil {
		return err
	}

	db.log.Info("database dropped", "database", name)
	db.removeLater(gone)

	return nil
}

// openDatabase opens the database of that name. The caller holds dbMu.
func (db *DB) openDatabase(name string) (*DB, error) {
	// The settings of the daemon, without the collections of ndb.yml:
	// they are of the default database.
	cfg := db.cfg
	cfg.DataDir = filepath.Join(db.dir, databasesDir, name)
	cfg.Collections = nil

	child, err := open(cfg, db.log.With("database", name), db, name)
	if err != nil {
		return nil, fmt.Errorf("database %s: %w", name, err)
	}

	db.dbs[name] = child

	return child, nil
}

// openDatabases opens every database of the data dir at start, so their
// background work (TTL, retention, PITR) runs, and removes what a drop left.
func (db *DB) openDatabases() error {
	entries, err := os.ReadDir(filepath.Join(db.dir, databasesDir))

	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	if err != nil {
		return err
	}

	db.dbMu.Lock()
	defer db.dbMu.Unlock()

	for _, e := range entries {
		name := e.Name()
		path := filepath.Join(db.dir, databasesDir, name)

		if strings.HasPrefix(name, ".") {
			db.removeLater(path)

			continue
		}

		if !e.IsDir() || !ValidDatabase(name) {
			db.log.Warn("not a database, left as it is", "path", path)

			continue
		}

		if _, err := db.openDatabase(name); err != nil {
			return err
		}
	}

	return nil
}

// removeLater deletes path in the background; Close waits for it.
func (db *DB) removeLater(path string) {
	db.trashWait.Add(1)

	go func() {
		defer db.trashWait.Done()

		if err := os.RemoveAll(path); err != nil {
			db.log.Error("remove a dropped database", "path", path, "err", err)
		}
	}()
}

// children takes the databases out of db, for Close: no database opens
// after it.
func (db *DB) children() []*DB {
	db.dbMu.Lock()
	defer db.dbMu.Unlock()

	db.dbClosed = true

	out := make([]*DB, 0, len(db.dbs))

	for _, name := range slices.Sorted(maps.Keys(db.dbs)) {
		out = append(out, db.dbs[name])
	}

	db.dbs = map[string]*DB{}

	return out
}

// eachDatabase calls fn for every database other than the default one.
func (db *DB) eachDatabase(fn func(*DB)) {
	db.dbMu.Lock()
	list := make([]*DB, 0, len(db.dbs))

	for _, name := range slices.Sorted(maps.Keys(db.dbs)) {
		list = append(list, db.dbs[name])
	}

	db.dbMu.Unlock()

	for _, child := range list {
		fn(child)
	}
}
