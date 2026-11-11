package ndb

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type Account struct {
	ID      string `ndb:"_id"`
	Balance int    `ndb:"balance"`
	Note    string `ndb:"note"`
	V       uint64 `ndb:"_v,version"`
}

func accounts(t *testing.T, seed ...Account) *Coll[Account] {
	t.Helper()

	col, err := Collection[Account](openTest(t, daemon.unix), newColl())
	if err != nil {
		t.Fatal(err)
	}

	if len(seed) > 0 {
		if _, err := col.InsertMany(context.Background(), seed); err != nil {
			t.Fatal(err)
		}
	}

	return col
}

func balance(t *testing.T, col *Coll[Account], id string) int {
	t.Helper()

	a, err := col.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}

	return a.Balance
}

func transfer(ctx context.Context, col *Coll[Account], from, to string, amount int) error {
	return col.Tx(ctx, []string{from, to}, func(tx *Tx[Account], docs map[string]*Account) error {
		if docs[from].Balance < amount {
			return ErrRollback
		}

		docs[from].Balance -= amount
		docs[to].Balance += amount

		tx.Update(docs[from])
		tx.Update(docs[to])

		return nil
	})
}

func TestTxTransfersWithLocks(t *testing.T) {
	ctx := context.Background()
	col := accounts(t, Account{ID: "a1", Balance: 100}, Account{ID: "a2"})

	var wg sync.WaitGroup

	for i := range 10 {
		wg.Go(func() {
			from, to := "a1", "a2"
			if i%2 == 1 {
				from, to = "a2", "a1"
			}

			if err := transfer(ctx, col, from, to, 1); err != nil {
				t.Error(err)
			}
		})
	}

	wg.Wait()

	if a1, a2 := balance(t, col, "a1"), balance(t, col, "a2"); a1+a2 != 100 {
		t.Fatalf("a1=%d a2=%d, want a sum of 100", a1, a2)
	}

	if err := transfer(ctx, col, "a2", "a1", 1000); err != nil {
		t.Fatalf("a rollback is not an error: %v", err)
	}

	if _, err := col.Lock(ctx, "a1", func(*Account) error { return nil }, LockWait(0)); err != nil {
		t.Fatalf("the locks were not released: %v", err)
	}
}

func TestTxErrorWritesNothingAndReleases(t *testing.T) {
	ctx := context.Background()
	col := accounts(t, Account{ID: "a1", Balance: 100}, Account{ID: "a2"})
	errGateway := errors.New("gateway down")

	err := col.Tx(ctx, []string{"a1"}, func(tx *Tx[Account], docs map[string]*Account) error {
		docs["a1"].Balance = 0
		tx.Update(docs["a1"])

		return errGateway
	})
	if !errors.Is(err, errGateway) {
		t.Fatalf("err %v, want the error of fn", err)
	}

	if balance(t, col, "a1") != 100 {
		t.Fatal("a failed block wrote")
	}

	err = col.Tx(ctx, []string{"a1"}, func(tx *Tx[Account], docs map[string]*Account) error {
		docs["a1"].Balance = 1
		tx.Update(docs["a1"])
		tx.Delete("nope")

		return nil
	})
	if !errors.Is(err, ErrTxConflict) {
		t.Fatalf("err %v, want ErrTxConflict", err)
	}

	if balance(t, col, "a1") != 100 {
		t.Fatal("a refused transaction wrote")
	}

	if err := col.Tx(ctx, []string{"a1", "nope"}, func(*Tx[Account], map[string]*Account) error { return nil }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("lock of a missing id: %v, want ErrNotFound", err)
	}

	if _, err := col.Lock(ctx, "a1", func(*Account) error { return nil }, LockWait(0)); err != nil {
		t.Fatalf("the locks were not released: %v", err)
	}
}

func TestTxWithoutLocksIsAnAtomicBatch(t *testing.T) {
	ctx := context.Background()
	col := accounts(t, Account{ID: "a1", Balance: 5, Note: "x"}, Account{ID: "a9"})

	stale, _ := col.Get(ctx, "a1")

	err := col.Tx(ctx, nil, func(tx *Tx[Account], _ map[string]*Account) error {
		tx.Insert(Account{ID: "a3", Balance: 7})
		tx.Delete("a9")

		a1 := stale
		a1.Balance = 6
		tx.Update(&a1)

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if balance(t, col, "a1") != 6 || balance(t, col, "a3") != 7 {
		t.Fatal("the batch was not written")
	}

	if _, err := col.Get(ctx, "a9"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a9: %v, want deleted", err)
	}

	err = col.Tx(ctx, nil, func(tx *Tx[Account], _ map[string]*Account) error {
		a1 := stale
		a1.Balance = 0
		tx.Update(&a1)

		return nil
	})
	if !errors.Is(err, ErrTxConflict) {
		t.Fatalf("an update with a stale version: %v, want ErrTxConflict", err)
	}

	if err := col.Tx(ctx, nil, func(*Tx[Account], map[string]*Account) error { return nil }); err != nil {
		t.Fatalf("an empty block: %v", err)
	}
}
