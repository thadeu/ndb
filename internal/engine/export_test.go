package engine

// SetWALSegmentSize makes the WAL files of collections opened later this
// small, so a few writes rotate them. It returns the reset func.
func SetWALSegmentSize(n int64) func() {
	old := walSegmentSize
	walSegmentSize = n

	return func() { walSegmentSize = old }
}

func SetLookupBudget(n int) func() {
	old := lookupBudget
	lookupBudget = n

	return func() { lookupBudget = old }
}

// Sweep runs the TTL sweep of an engine collection now.
func (c *Collection) Sweep() (int, error) {
	if err := c.enter(); err != nil {
		return 0, err
	}

	defer c.leave()

	return c.eng.Sweep()
}
