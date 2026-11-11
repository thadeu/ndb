// Package settings is the settings of a daemon that live in its data dir,
// not in ndb.yml: GET and PUT /_cluster/settings, and the encryption key.
//
// They are a log of their own, <data-dir>/_system/settings.wal, in the
// format of the WAL of a collection (one line per change, with a CRC, and
// fsync before the answer); each key is one op, its value in
// {"value": ...}. The last value of a key wins. A cut last line,
// left by a crash in the middle of a write, is dropped at open. A backup
// holds the settings, the key with them.
package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/thadeu/ndb/internal/wal"
)

// Dir is the dir of the settings, in the data dir; File is the log in it.
const (
	Dir  = "_system"
	File = "settings.wal"

	maxLine = 1 << 20
)

// Store is the settings of one data dir.
type Store struct {
	mu   sync.Mutex
	path string
	vals map[string]json.RawMessage
	tx   uint64
}

// Open reads the settings of dataDir (none when the file is missing).
func Open(dataDir string) (*Store, error) {
	s := &Store{path: filepath.Join(dataDir, Dir, File), vals: map[string]json.RawMessage{}}

	f, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}

	if err != nil {
		return nil, err
	}

	defer f.Close()

	good, err := s.replay(f)
	if err != nil {
		return nil, err
	}

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}

	if info.Size() > good {
		if err := os.Truncate(s.path, good); err != nil {
			return nil, fmt.Errorf("settings: drop a cut last line: %w", err)
		}
	}

	return s, nil
}

// replay applies the lines of r and gives the size of the good ones. A bad
// line that is the last one is a cut write; one before the end is an error.
func (s *Store) replay(r io.Reader) (int64, error) {
	lr := wal.NewLineReader(r, 0, maxLine)

	for {
		line, start, err := lr.Next()

		if errors.Is(err, io.EOF) {
			return lr.Offset(), nil
		}

		if errors.Is(err, wal.ErrPartialLine) {
			return start, nil
		}

		if err != nil {
			return 0, fmt.Errorf("settings: %w", err)
		}

		rec, err := wal.Decode(line)
		if err != nil {
			if _, _, next := lr.Next(); errors.Is(next, io.EOF) {
				return start, nil
			}

			return 0, fmt.Errorf("settings: line at offset %d: %w", start, err)
		}

		s.tx = rec.Tx

		for _, op := range rec.Ops {
			var doc struct {
				Value json.RawMessage `json:"value"`
			}

			if err := json.Unmarshal(op.Doc, &doc); err != nil {
				return 0, fmt.Errorf("settings: %s: %w", op.ID, err)
			}

			s.vals[op.ID] = doc.Value
		}
	}
}

// Get is the value of key.
func (s *Store) Get(key string) (json.RawMessage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	v, ok := s.vals[key]

	return v, ok
}

// All is every key and its value.
func (s *Store) All() map[string]json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()

	return maps.Clone(s.vals)
}

// Set writes vals in one line, synced, then keeps them.
func (s *Store) Set(vals map[string]json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec := wal.Record{Tx: s.tx + 1, TS: time.Now().UTC()}

	for _, key := range sortedKeys(vals) {
		doc, err := json.Marshal(map[string]json.RawMessage{"value": vals[key]})
		if err != nil {
			return err
		}

		rec.Ops = append(rec.Ops, wal.Op{Kind: wal.Put, ID: key, V: rec.Tx, Doc: doc})
	}

	if err := appendRecord(s.path, rec); err != nil {
		return err
	}

	s.tx = rec.Tx

	for k, v := range vals {
		s.vals[k] = bytes.Clone(v)
	}

	return nil
}

// WriteAll writes vals as the settings of dataDir, for a restore. The
// file must not exist.
func WriteAll(dataDir string, vals map[string]json.RawMessage) error {
	if len(vals) == 0 {
		return nil
	}

	s := &Store{path: filepath.Join(dataDir, Dir, File), vals: map[string]json.RawMessage{}}

	return s.Set(vals)
}

func appendRecord(path string, rec wal.Record) error {
	line, err := wal.Encode(nil, rec)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}

	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()

		return err
	}

	if err := f.Sync(); err != nil {
		f.Close()

		return err
	}

	return f.Close()
}

func sortedKeys(m map[string]json.RawMessage) []string {
	return slices.Sorted(maps.Keys(m))
}
