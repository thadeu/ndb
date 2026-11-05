// Package blob is the content-addressed store of plan §7.5. A blob lives at
// <dir>/sha256/ab/cd/<hash> and is written atomically (tmp + fsync + rename
// + fsync dir), so a reader sees a whole blob or none. The same content is
// stored once.
//
// Order rule for writers: Put returns only after the blob is durable. A
// document that refers to it is written to the WAL after that. A crash
// between the two leaves an unused blob, never a reference to a missing one.
package blob

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thadeu/ndb/internal/fs"
)

const prefix = "sha256:"

var (
	ErrTooLarge = errors.New("blob_too_large: the blob is over the size limit")
	ErrNotFound = errors.New("not_found: no such blob")
	ErrBadRef   = errors.New("invalid_blob_ref: want sha256:<64 hex digits>")
)

// Ref is the reference that a document keeps in place of the content.
type Ref struct {
	Blob string `json:"$blob"`
	Size int64  `json:"size"`
	Type string `json:"type,omitempty"`
}

// Hash is the hex digest of the reference.
func (r Ref) Hash() string {
	return strings.TrimPrefix(r.Blob, prefix)
}

// ParseHash accepts "sha256:<hex>" or "<hex>" and returns the hex digest.
func ParseHash(ref string) (string, error) {
	h := strings.TrimPrefix(ref, prefix)

	if len(h) != sha256.Size*2 {
		return "", ErrBadRef
	}

	for _, c := range h {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", ErrBadRef
		}
	}

	return h, nil
}

type Store struct {
	fs  fs.FS
	dir string

	// mu orders the last step of Put (exists? rename) so two writers of the
	// same content count it once.
	mu    sync.Mutex
	tmpID atomic.Int64

	count atomic.Int64
	bytes atomic.Int64
}

// Open creates the store dir if needed, removes the tmp files that a crash
// left, and counts the blobs.
func Open(fsys fs.FS, dir string) (*Store, error) {
	s := &Store{fs: fsys, dir: dir}

	if err := fsys.MkdirAll(s.tmpDir()); err != nil {
		return nil, err
	}

	if err := s.clearTmp(); err != nil {
		return nil, err
	}

	err := s.walk(func(_ string, size int64, _ time.Time) error {
		s.count.Add(1)
		s.bytes.Add(size)

		return nil
	})
	if err != nil {
		return nil, err
	}

	return s, nil
}

func (s *Store) tmpDir() string {
	return filepath.Join(s.dir, "tmp")
}

func (s *Store) path(hash string) string {
	return filepath.Join(s.dir, "sha256", hash[:2], hash[2:4], hash)
}

func (s *Store) clearTmp() error {
	entries, err := s.fs.ReadDir(s.tmpDir())
	if err != nil {
		return err
	}

	for _, e := range entries {
		if err := s.fs.Remove(filepath.Join(s.tmpDir(), e.Name())); err != nil {
			return err
		}
	}

	return nil
}

// Stats is the number of blobs and their total size.
func (s *Store) Stats() (count, bytes int64) {
	return s.count.Load(), s.bytes.Load()
}

// Has tells whether the blob is in the store.
func (s *Store) Has(hash string) bool {
	if _, err := ParseHash(hash); err != nil {
		return false
	}

	_, err := s.fs.Stat(s.path(hash))

	return err == nil
}

// Put streams r into the store. max > 0 limits the size: a longer stream
// gives ErrTooLarge and stores nothing. typ is kept in the returned Ref only.
func (s *Store) Put(r io.Reader, typ string, max int64) (Ref, error) {
	tmp := filepath.Join(s.tmpDir(), strconv.FormatInt(time.Now().UnixNano(), 36)+"-"+strconv.FormatInt(s.tmpID.Add(1), 10))

	hash, size, err := s.writeTmp(tmp, r, max)
	if err != nil {
		s.fs.Remove(tmp)

		return Ref{}, err
	}

	if err := s.publish(tmp, hash, size); err != nil {
		s.fs.Remove(tmp)

		return Ref{}, err
	}

	return Ref{Blob: prefix + hash, Size: size, Type: typ}, nil
}

// PutBytes stores a value that is already in memory.
func (s *Store) PutBytes(b []byte, typ string) (Ref, error) {
	return s.Put(bytes.NewReader(b), typ, 0)
}

// writeTmp copies r to a durable tmp file and returns its digest and size.
func (s *Store) writeTmp(tmp string, r io.Reader, max int64) (string, int64, error) {
	f, err := s.fs.Create(tmp)
	if err != nil {
		return "", 0, err
	}

	defer f.Close()

	h := sha256.New()
	src := r

	if max > 0 {
		src = io.LimitReader(r, max+1)
	}

	size, err := io.Copy(io.MultiWriter(f, h), src)
	if err != nil {
		return "", 0, err
	}

	if max > 0 && size > max {
		return "", 0, ErrTooLarge
	}

	if err := f.Sync(); err != nil {
		return "", 0, err
	}

	return hex.EncodeToString(h.Sum(nil)), size, nil
}

// publish moves the tmp file to its address. When the content is already
// there, the old file stays and its time is renewed, so a GC that is already
// running keeps it.
func (s *Store) publish(tmp, hash string, size int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	final := s.path(hash)

	if _, err := s.fs.Stat(final); err == nil {
		if err := s.fs.Chtimes(final, time.Now()); err != nil {
			return err
		}

		return s.fs.Remove(tmp)
	}

	leaf := filepath.Dir(final)
	_, statErr := s.fs.Stat(leaf)

	if err := s.fs.MkdirAll(leaf); err != nil {
		return err
	}

	if err := s.fs.Rename(tmp, final); err != nil {
		return err
	}

	if err := s.syncDirs(leaf, statErr != nil); err != nil {
		return err
	}

	s.count.Add(1)
	s.bytes.Add(size)

	return nil
}

// syncDirs makes the rename durable. New dirs also need their parents synced.
func (s *Store) syncDirs(leaf string, created bool) error {
	if err := s.fs.SyncDir(leaf); err != nil {
		return err
	}

	if !created {
		return nil
	}

	for d := filepath.Dir(leaf); strings.HasPrefix(d, s.dir) && d != filepath.Dir(s.dir); d = filepath.Dir(d) {
		if err := s.fs.SyncDir(d); err != nil {
			return err
		}
	}

	return nil
}

// Reader is an open blob. It reads, seeks and reports its size; http.ServeContent
// takes it as is, so Range requests work.
type Reader struct {
	*io.SectionReader

	f       fs.File
	modTime time.Time
}

func (r *Reader) Close() error {
	return r.f.Close()
}

func (r *Reader) ModTime() time.Time {
	return r.modTime
}

// Get opens a blob by reference ("sha256:<hex>" or "<hex>").
func (s *Store) Get(ref string) (*Reader, error) {
	hash, err := ParseHash(ref)
	if err != nil {
		return nil, err
	}

	f, err := s.fs.Open(s.path(hash))

	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}

	if err != nil {
		return nil, err
	}

	info, err := f.Stat()
	if err != nil {
		f.Close()

		return nil, err
	}

	return &Reader{SectionReader: io.NewSectionReader(f, 0, info.Size()), f: f, modTime: info.ModTime()}, nil
}

// walk calls fn for every blob file.
func (s *Store) walk(fn func(hash string, size int64, mtime time.Time) error) error {
	return s.walkDir(filepath.Join(s.dir, "sha256"), 0, fn)
}

func (s *Store) walkDir(dir string, depth int, fn func(string, int64, time.Time) error) error {
	entries, err := s.fs.ReadDir(dir)

	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	if err != nil {
		return err
	}

	for _, e := range entries {
		if e.IsDir() {
			if err := s.walkDir(filepath.Join(dir, e.Name()), depth+1, fn); err != nil {
				return err
			}

			continue
		}

		info, err := e.Info()
		if err != nil {
			return fmt.Errorf("blob: %w", err)
		}

		if err := fn(e.Name(), info.Size(), info.ModTime()); err != nil {
			return err
		}
	}

	return nil
}
