package tailer

import (
	"encoding/binary"
	"encoding/json"
	"hash/fnv"
	"io"
	"path/filepath"

	bolt "go.etcd.io/bbolt"
)

const (
	fingerprintSize = 1024
	cursorsFile     = "meta.bolt"
)

var cursorBucket = []byte("cursors")

// Cursor is where the tailer stopped in one file. The inode is the key, so
// a renamed file keeps its place. The fingerprint (hash of the first
// FPLen <= 1 KiB bytes) tells a reused inode or a truncated file apart.
type Cursor struct {
	Path   string `json:"path"`
	Inode  uint64 `json:"inode"`
	Offset int64  `json:"offset"`
	FPLen  int    `json:"fp_len"`
	FP     uint64 `json:"fp"`
}

type cursorStore struct {
	db *bolt.DB
}

func openCursors(dir string, reset bool) (*cursorStore, map[uint64]Cursor, error) {
	db, err := bolt.Open(filepath.Join(dir, cursorsFile), 0o644, nil)
	if err != nil {
		return nil, nil, err
	}

	s := &cursorStore{db: db}

	if reset {
		err = db.Update(func(tx *bolt.Tx) error {
			if tx.Bucket(cursorBucket) != nil {
				if err := tx.DeleteBucket(cursorBucket); err != nil {
					return err
				}
			}

			_, err := tx.CreateBucket(cursorBucket)

			return err
		})
	}

	if err != nil {
		db.Close()

		return nil, nil, err
	}

	cur, err := s.load()
	if err != nil {
		db.Close()

		return nil, nil, err
	}

	return s, cur, nil
}

func (s *cursorStore) load() (map[uint64]Cursor, error) {
	out := map[uint64]Cursor{}

	err := s.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(cursorBucket)
		if err != nil {
			return err
		}

		return b.ForEach(func(_, v []byte) error {
			var c Cursor

			if err := json.Unmarshal(v, &c); err != nil {
				return err
			}

			out[c.Inode] = c

			return nil
		})
	})

	return out, err
}

func (s *cursorStore) save(c Cursor) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(cursorBucket).Put(inodeKey(c.Inode), data)
	})
}

func (s *cursorStore) remove(inode uint64) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(cursorBucket).Delete(inodeKey(inode))
	})
}

func (s *cursorStore) close() error {
	return s.db.Close()
}

// LoadCursors reads the cursors saved in dir/meta.bolt, by inode.
func LoadCursors(dir string) (map[uint64]Cursor, error) {
	s, cur, err := openCursors(dir, false)
	if err != nil {
		return nil, err
	}

	return cur, s.close()
}

func inodeKey(inode uint64) []byte {
	return binary.BigEndian.AppendUint64(nil, inode)
}

// fingerprint hashes the first min(size, 1 KiB) bytes of f.
func fingerprint(f io.ReaderAt, size int64) (int, uint64, error) {
	n := int(min(size, fingerprintSize))
	buf := make([]byte, n)

	if _, err := f.ReadAt(buf, 0); err != nil && err != io.EOF {
		return 0, 0, err
	}

	h := fnv.New64a()
	h.Write(buf)

	return n, h.Sum64(), nil
}
