//go:build !linux

package fs

// Fallocate is a no-op outside Linux. It is only an optimization.
func (f osFile) Fallocate(int64) error {
	return nil
}
