package collection

import (
	"path/filepath"
	"strings"
	"sync"

	"github.com/thadeu/ndb/internal/fs"
	"github.com/thadeu/ndb/internal/segment"
)

// segRef is a live segment with a reference count. A query takes a reference
// on every live segment when it starts (under c.mu.RLock) and drops it when
// it ends, so a compaction can retire a segment without pulling the file from
// under a query that still reads it. The file closes and goes when the
// segment is retired and the last reference is gone.
type segRef struct {
	segment.Reader

	path string
	fs   fs.FS

	// ids, keys (secondary index fields) and uniq (unique fields) are the
	// key files of the segment (sealed.go). keys gains a field when an index
	// built later reaches this segment; the map is changed under
	// Collection.mu.
	ids  *segment.Keys
	keys map[string]*segment.Keys
	uniq map[string]*segment.Keys

	// live has the rows that are the current version of their document, and
	// nlive counts them. gen counts the changes to live, and saved is the gen
	// that meta.bolt holds. All four are guarded by Collection.mu.
	live  bitset
	nlive int
	gen   uint64
	saved uint64

	mu      sync.Mutex
	refs    int
	retired bool
	done    bool
}

func newSegRef(r segment.Reader, fsys fs.FS, path string) *segRef {
	return &segRef{Reader: r, fs: fsys, path: path}
}

func (r *segRef) acquire() {
	r.mu.Lock()
	r.refs++
	r.mu.Unlock()
}

func (r *segRef) release() {
	r.mu.Lock()
	r.refs--

	last := r.finished()

	r.mu.Unlock()

	if last {
		r.remove()
	}
}

// retire marks the segment as replaced. It is removed now if no query holds
// it, or by the release of the last query.
func (r *segRef) retire() {
	r.mu.Lock()
	r.retired = true

	last := r.finished()

	r.mu.Unlock()

	if last {
		r.remove()
	}
}

// finished is true once, when the segment is retired and unused. The caller
// holds r.mu.
func (r *segRef) finished() bool {
	if !r.retired || r.refs > 0 || r.done {
		return false
	}

	r.done = true

	return true
}

func (r *segRef) remove() {
	r.closeFiles()
	r.fs.Remove(r.path)

	removeSidecars(r.fs, r.path)
}

// closeFiles closes the segment and its key files.
func (r *segRef) closeFiles() {
	r.Reader.Close()

	if r.ids != nil {
		r.ids.Close()
	}

	for _, k := range r.keys {
		k.Close()
	}

	for _, k := range r.uniq {
		k.Close()
	}
}

// attachIndex opens the index file of field, when it exists.
func (r *segRef) attachIndex(fsys fs.FS, field string) error {
	k, err := segment.OpenKeys(fsys, indexPath(r.path, field))

	if isNotExist(err) {
		return nil
	}

	if err != nil {
		return err
	}

	r.keys[field] = k

	return nil
}

// removeSidecars deletes the key files of the segment at path.
func removeSidecars(fsys fs.FS, path string) {
	dir := filepath.Dir(path)
	prefix := filepath.Base(sidecarBase(path)) + "."

	entries, err := fsys.ReadDir(dir)
	if err != nil {
		return
	}

	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) && e.Name() != filepath.Base(path) {
			fsys.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// holdSegments takes a reference on every live segment. The caller holds
// c.mu, so a compaction swap cannot run in between.
func (c *Collection) holdSegments() []*segRef {
	if c.seal == nil {
		return nil
	}

	held := make([]*segRef, len(c.seal.readers))

	for i, r := range c.seal.readers {
		r.acquire()

		held[i] = r
	}

	return held
}

func releaseAll(held []*segRef) {
	for _, r := range held {
		r.release()
	}
}
