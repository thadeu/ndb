package fs

import "syscall"

// fallocKeepSize reserves blocks without changing the file size, so
// O_APPEND writes still go to the real end of the data.
const fallocKeepSize = 0x01

func (f osFile) Fallocate(size int64) error {
	return syscall.Fallocate(int(f.Fd()), fallocKeepSize, 0, size)
}
