package collection

import (
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
	r.Reader.Close()
	r.fs.Remove(r.path)
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
