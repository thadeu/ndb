// Package cursors keeps the position of each watch consumer in
// <data_dir>/cursors.bolt: collection + consumer name -> position token.
// Every update is one bbolt transaction, fsync'd before Update returns.
package cursors

import (
	"bytes"
	"io"

	bolt "go.etcd.io/bbolt"
)

var bucket = []byte("cursors")

type Store struct {
	db *bolt.DB
}

func Open(path string) (*Store, error) {
	db, err := bolt.Open(path, 0o644, nil)
	if err != nil {
		return nil, err
	}

	err = db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(bucket)

		return err
	})
	if err != nil {
		db.Close()

		return nil, err
	}

	return &Store{db: db}, nil
}

// Snapshot writes a consistent copy of the cursor file (one read
// transaction), while the store is in use.
func (s *Store) Snapshot(w io.Writer) error {
	return s.db.View(func(tx *bolt.Tx) error {
		_, err := tx.WriteTo(w)

		return err
	})
}

func (s *Store) Close() error {
	return s.db.Close()
}

func key(coll, consumer string) []byte {
	return []byte(coll + "\x00" + consumer)
}

// Get returns the stored position of the consumer.
func (s *Store) Get(coll, consumer string) (string, bool, error) {
	var (
		val string
		ok  bool
	)

	err := s.db.View(func(tx *bolt.Tx) error {
		if v := tx.Bucket(bucket).Get(key(coll, consumer)); v != nil {
			val, ok = string(v), true
		}

		return nil
	})

	return val, ok, err
}

// Update replaces the position with the result of fn, in one transaction.
// fn gets the current position (ok is false when there is none). If fn
// returns an error, nothing is stored.
func (s *Store) Update(coll, consumer string, fn func(old string, ok bool) (string, error)) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucket)
		k := key(coll, consumer)

		old := b.Get(k)

		next, err := fn(string(old), old != nil)
		if err != nil {
			return err
		}

		return b.Put(k, []byte(next))
	})
}

// List returns the stored position of every consumer of the collection.
func (s *Store) List(coll string) ([]string, error) {
	var out []string

	prefix := []byte(coll + "\x00")

	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucket).Cursor()

		for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
			out = append(out, string(v))
		}

		return nil
	})

	return out, err
}

// Entry is one stored position.
type Entry struct {
	Collection string
	Consumer   string
	Pos        string
}

// All returns every stored position, ordered by collection and consumer.
func (s *Store) All() ([]Entry, error) {
	var out []Entry

	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucket).ForEach(func(k, v []byte) error {
			coll, consumer, _ := bytes.Cut(k, []byte{0})

			out = append(out, Entry{Collection: string(coll), Consumer: string(consumer), Pos: string(v)})

			return nil
		})
	})

	return out, err
}
