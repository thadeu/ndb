// Package lock makes sure only one ndb process uses a data dir.
package lock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

var ErrLocked = errors.New("lock: data dir is used by another ndb process")

type Lock struct {
	f *os.File
}

// Acquire takes an exclusive flock on <dir>/LOCK. The kernel drops the
// lock when the process dies, so a crash never leaves a stale lock.
func Acquire(dir string) (*Lock, error) {
	f, err := os.OpenFile(filepath.Join(dir, "LOCK"), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}

	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)

	if errors.Is(err, syscall.EWOULDBLOCK) {
		f.Close()

		return nil, ErrLocked
	}

	if err != nil {
		f.Close()

		return nil, fmt.Errorf("lock: %w", err)
	}

	return &Lock{f: f}, nil
}

func (l *Lock) Release() error {
	return l.f.Close()
}
