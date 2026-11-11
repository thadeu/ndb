package ndb

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type Wallet struct {
	ID      string `ndb:"_id"`
	Balance int    `ndb:"balance_cents"`
	Note    string `ndb:"note"`
}

func TestWithRetryRetriesAConflict(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, daemon.unix)

	col, err := Collection[Wallet](db, newColl())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := col.Insert(ctx, Wallet{ID: "w1", Balance: 1000, Note: "x"}); err != nil {
		t.Fatal(err)
	}

	calls := 0

	got, err := col.WithRetry(ctx, "w1", func(w *Wallet) error {
		calls++

		if calls == 1 {
			if _, err := col.Update(ctx, "w1", Ops{"$inc": {"balance_cents": -300}}); err != nil {
				return err
			}
		}

		w.Balance -= 500
		w.Note = ""

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if calls != 2 || got.Balance != 200 || got.Note != "" {
		t.Fatalf("calls %d, wallet %+v; want 2 calls and 200 without note", calls, got)
	}

	stored, _ := col.Get(ctx, "w1")

	if stored.Balance != 200 || stored.Note != "" {
		t.Fatalf("stored %+v, want 200 without note", stored)
	}
}

func TestWithRetryConcurrentDebitsAllLand(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, daemon.unix)

	col, err := Collection[Wallet](db, newColl())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := col.Insert(ctx, Wallet{ID: "w1", Balance: 1000}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup

	for range 5 {
		wg.Go(func() {
			if _, err := col.WithRetry(ctx, "w1", func(w *Wallet) error {
				w.Balance -= 100

				return nil
			}, Retries(20)); err != nil {
				t.Error(err)
			}
		})
	}

	wg.Wait()

	stored, _ := col.Get(ctx, "w1")

	if stored.Balance != 500 {
		t.Fatalf("balance %d, want 500", stored.Balance)
	}
}

func TestWithRetryStopsOnTheCallbackError(t *testing.T) {
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

	_, err = col.WithRetry(ctx, "w1", func(w *Wallet) error {
		if w.Balance < 500 {
			return errFunds
		}

		w.Balance -= 500

		return nil
	})
	if !errors.Is(err, errFunds) {
		t.Fatalf("err %v, want the callback error", err)
	}

	if _, err := col.WithRetry(ctx, "nope", func(*Wallet) error { return nil }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing doc: err %v, want ErrNotFound", err)
	}
}

func TestWithRetryGivesUpAfterItsRetries(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, daemon.unix)

	col, err := Collection[Wallet](db, newColl())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := col.Insert(ctx, Wallet{ID: "w1", Balance: 100}); err != nil {
		t.Fatal(err)
	}

	calls := 0

	_, err = col.WithRetry(ctx, "w1", func(w *Wallet) error {
		calls++

		if _, err := col.Update(ctx, "w1", Ops{"$inc": {"balance_cents": 1}}); err != nil {
			return err
		}

		w.Balance = 0

		return nil
	}, Retries(3))
	if !errors.Is(err, ErrStaleObject) {
		t.Fatalf("err %v, want ErrStaleObject", err)
	}

	if calls != 3 {
		t.Fatalf("calls %d, want 3", calls)
	}
}

func TestWithRetryWithoutChangesDoesNotWrite(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, daemon.unix)

	col, err := Collection[Wallet](db, newColl())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := col.Insert(ctx, Wallet{ID: "w1", Balance: 100}); err != nil {
		t.Fatal(err)
	}

	if _, err := col.WithRetry(ctx, "w1", func(*Wallet) error { return nil }); err != nil {
		t.Fatal(err)
	}

	var raw map[string]any

	if err := col.call(ctx, "GET", "_doc/w1", nil, &raw); err != nil {
		t.Fatal(err)
	}

	if raw["_v"] != float64(1) {
		t.Fatalf("_v %v, want 1 (no write)", raw["_v"])
	}
}
