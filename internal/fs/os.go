package fs

import (
	"os"
	"time"
)

// OS is the real file system.
type OS struct{}

type osFile struct {
	*os.File
}

func (OS) Create(path string) (File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}

	return osFile{f}, nil
}

func (OS) OpenAppend(path string) (File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}

	return osFile{f}, nil
}

func (OS) Open(path string) (File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	return osFile{f}, nil
}

func (OS) Rename(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}

func (OS) Remove(path string) error {
	return os.Remove(path)
}

func (OS) Stat(path string) (os.FileInfo, error) {
	return os.Stat(path)
}

func (OS) MkdirAll(path string) error {
	return os.MkdirAll(path, 0o755)
}

// SyncDir makes a create or rename in dir durable.
func (OS) SyncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}

	defer d.Close()

	return d.Sync()
}

func (OS) ReadDir(path string) ([]os.DirEntry, error) {
	return os.ReadDir(path)
}

func (OS) Chtimes(path string, t time.Time) error {
	return os.Chtimes(path, t, t)
}
