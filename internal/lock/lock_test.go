package lock_test

import (
	"errors"
	"testing"

	"github.com/thadeu/ndb/internal/lock"
)

func TestSecondLockFailsUntilRelease(t *testing.T) {
	dir := t.TempDir()

	first, err := lock.Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := lock.Acquire(dir); !errors.Is(err, lock.ErrLocked) {
		t.Fatalf("second acquire: want ErrLocked, got %v", err)
	}

	if err := first.Release(); err != nil {
		t.Fatal(err)
	}

	again, err := lock.Acquire(dir)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}

	again.Release()
}
