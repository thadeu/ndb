package collection

import (
	"container/list"
	"encoding/json"
	"sync"
)

const defaultDocCache = 1024

// docCache is an LRU of decoded documents for find by id. An entry is
// immutable, so the pointer to it names one version of a document: after an
// update the id maps to a new entry, and the old one is never hit again.
type docCache struct {
	mu  sync.Mutex
	max int
	ll  *list.List
	idx map[*entry]*list.Element
}

type cached struct {
	e   *entry
	raw json.RawMessage
	m   map[string]any
}

// newDocCache returns a cache of n documents: 0 is the default, and a
// negative n gives no cache (nil).
func newDocCache(n int) *docCache {
	if n < 0 {
		return nil
	}

	if n == 0 {
		n = defaultDocCache
	}

	return &docCache{max: n, ll: list.New(), idx: map[*entry]*list.Element{}}
}

// get returns the cached document of e and marks it as recently used.
func (d *docCache) get(e *entry) (cached, bool) {
	if d == nil {
		return cached{}, false
	}

	d.mu.Lock()

	defer d.mu.Unlock()

	el, ok := d.idx[e]

	if !ok {
		return cached{}, false
	}

	d.ll.MoveToFront(el)

	return el.Value.(cached), true
}

// put keeps c, and drops the least recently used document when full.
func (d *docCache) put(c cached) {
	if d == nil {
		return
	}

	d.mu.Lock()

	defer d.mu.Unlock()

	if el, ok := d.idx[c.e]; ok {
		d.ll.MoveToFront(el)

		return
	}

	d.idx[c.e] = d.ll.PushFront(c)

	if d.ll.Len() > d.max {
		last := d.ll.Back()

		d.ll.Remove(last)
		delete(d.idx, last.Value.(cached).e)
	}
}
