// Package fs is the only door to the disk. Tests swap it for Fault to
// inject write, sync and rename failures.
package fs

import "os"

type File interface {
	Write(p []byte) (int, error)
	ReadAt(p []byte, off int64) (int, error)
	Sync() error
	Truncate(size int64) error
	Fallocate(size int64) error
	Stat() (os.FileInfo, error)
	Close() error
}

type FS interface {
	Create(path string) (File, error)
	OpenAppend(path string) (File, error)
	Open(path string) (File, error)
	Rename(oldPath, newPath string) error
	Remove(path string) error
	Stat(path string) (os.FileInfo, error)
	MkdirAll(path string) error
	SyncDir(path string) error
}
