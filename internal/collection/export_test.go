package collection

// SetBuildGate makes the background index build call g before each chunk.
// Set it before EnsureIndex.
func SetBuildGate(c *Collection, g func()) {
	c.rd.gate = g
}

// MemDocs counts the memtable documents that still hold their bytes.
func MemDocs(c *Collection) int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	n := 0

	for _, e := range c.durable.docs {
		if e.doc != nil {
			n++
		}
	}

	return n
}

// SegmentRows is the number of rows in the live segments.
func SegmentRows(c *Collection) int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	n := 0

	for _, r := range c.seal.readers {
		n += r.Len()
	}

	return n
}

// HoldSegments takes a reference on every live segment, as a running query
// does, and returns the release.
func HoldSegments(c *Collection) func() {
	c.mu.RLock()
	held := c.holdSegments()
	c.mu.RUnlock()

	return func() { releaseAll(held) }
}

// SetCompactHook calls fn after each step of a compaction.
func SetCompactHook(c *Collection, fn func(step string)) {
	c.seal.hook = fn
}

// CompactRange merges the segments lo..hi-1.
func (c *Collection) CompactRange(lo, hi int) error {
	return c.compactRange(lo, hi)
}
