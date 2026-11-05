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
