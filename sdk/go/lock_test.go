package ndb

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLockRunsOneHolderAtATime(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, daemon.unix)

	col, err := Collection[Wallet](db, newColl())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := col.Insert(ctx, Wallet{ID: "w1", Balance: 1000}); err != nil {
		t.Fatal(err)
	}

	var (
		wg     sync.WaitGroup
		inside atomic.Int32
		calls  atomic.Int32
	)

	for range 5 {
		wg.Go(func() {
			_, err := col.Lock(ctx, "w1", func(w *Wallet) error {
				if inside.Add(1) != 1 {
					t.Error("two holders at once")
				}

				calls.Add(1)
				time.Sleep(5 * time.Millisecond)

				w.Balance -= 100

				inside.Add(-1)

				return nil
			})
			if err != nil {
				t.Error(err)
			}
		})
	}

	wg.Wait()

	stored, _ := col.Get(ctx, "w1")

	if stored.Balance != 500 || calls.Load() != 5 {
		t.Fatalf("balance %d after %d calls; want 500 after 5 (each runs once)", stored.Balance, calls.Load())
	}
}

func TestLockErrors(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, daemon.unix)

	col, err := Collection[Wallet](db, newColl())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := col.Insert(ctx, Wallet{ID: "w1", Balance: 100}); err != nil {
		t.Fatal(err)
	}

	errFunds := errors.New("insufficient funds")

	_, err = col.Lock(ctx, "w1", func(w *Wallet) error {
		w.Balance = 0

		if _, err := col.Lock(ctx, "w1", func(*Wallet) error { return nil }, LockWait(0)); !errors.Is(err, ErrLocked) {
			t.Errorf("a second lock with no wait: %v, want ErrLocked", err)
		}

		return errFunds
	})
	if !errors.Is(err, errFunds) {
		t.Fatalf("err %v, want the callback error", err)
	}

	if stored, _ := col.Get(ctx, "w1"); stored.Balance != 100 {
		t.Fatalf("balance %d, want 100 (nothing written)", stored.Balance)
	}

	if _, err := col.Lock(ctx, "w1", func(*Wallet) error { return nil }, LockWait(0)); err != nil {
		t.Fatalf("the lock was not released after the error: %v", err)
	}

	if _, err := col.Lock(ctx, "nope", func(*Wallet) error { return nil }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing doc: %v, want ErrNotFound", err)
	}

	_, err = col.Lock(ctx, "w1", func(w *Wallet) error {
		time.Sleep(150 * time.Millisecond)

		w.Balance = 1

		return nil
	}, LockTTL(100*time.Millisecond))
	if !errors.Is(err, ErrLockExpired) {
		t.Fatalf("a write after the ttl: %v, want ErrLockExpired", err)
	}
}
