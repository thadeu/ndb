package segment

import (
	lru "container/list"
	"sync"
)

// GroupCacheBytes is the most the decoded _raw row groups of every segment
// may hold together. A read decodes the whole row group of a row (1024
// rows), so queries that read rows of different groups at once each keep
// theirs instead of decoding them again and again: with one group per
// segment, 4 sorted searches at once took 5 times their time in a row.
var GroupCacheBytes int64 = 32 << 20

// groups is the cache of the daemon, least recently used out first.
var groups = &groupCache{items: map[groupKey]*lru.Element{}, order: lru.New()}

type groupKey struct {
	r *parquetReader
	g int
}

type groupEntry struct {
	key   groupKey
	raws  [][]byte
	bytes int64
}

type groupCache struct {
	mu    sync.Mutex
	items map[groupKey]*lru.Element
	order *lru.List
	bytes int64
}

func (c *groupCache) get(k groupKey) ([][]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.items[k]
	if !ok {
		return nil, false
	}

	c.order.MoveToFront(el)

	return el.Value.(*groupEntry).raws, true
}

func (c *groupCache) put(k groupKey, raws [][]byte) {
	var size int64

	for _, b := range raws {
		size += int64(len(b)) + 24
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.items[k]; ok || size > GroupCacheBytes {
		return
	}

	c.items[k] = c.order.PushFront(&groupEntry{key: k, raws: raws, bytes: size})
	c.bytes += size

	for c.bytes > GroupCacheBytes {
		c.remove(c.order.Back())
	}
}

// drop removes the groups of a closed reader.
func (c *groupCache) drop(r *parquetReader) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for g := range r.starts {
		if el, ok := c.items[groupKey{r, g}]; ok {
			c.remove(el)
		}
	}
}

func (c *groupCache) remove(el *lru.Element) {
	e := c.order.Remove(el).(*groupEntry)

	delete(c.items, e.key)
	c.bytes -= e.bytes
}

// cachedBytes is what the cache holds (tests).
func (c *groupCache) cachedBytes() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.bytes
}
