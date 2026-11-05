package engine

// SetWALSegmentSize makes the WAL files of collections opened later this
// small, so a few writes rotate them. It returns the reset func.
func SetWALSegmentSize(n int64) func() {
	old := walSegmentSize
	walSegmentSize = n

	return func() { walSegmentSize = old }
}
