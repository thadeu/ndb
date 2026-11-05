package collection

// SetBuildGate makes the background index build call g before each chunk.
// Set it before EnsureIndex.
func SetBuildGate(c *Collection, g func()) {
	c.rd.gate = g
}
