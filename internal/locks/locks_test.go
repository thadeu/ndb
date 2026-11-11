package locks

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAcquireCheckRelease(t *testing.T) {
	s := New().Scope("pay")

	lease, err := s.TryAcquire("p1", time.Minute)
	if err != nil || lease.Token == "" {
		t.Fatalf("lease %+v, err %v", lease, err)
	}

	var busy *Busy

	if _, err := s.TryAcquire("p1", time.Minute); !errors.As(err, &busy) {
		t.Fatalf("second acquire: %v, want Busy", err)
	}

	if err := s.Check("p1", ""); !errors.As(err, &busy) {
		t.Fatalf("write without the token: %v, want Busy", err)
	}

	if err := s.Check("p1", lease.Token); err != nil {
		t.Fatalf("write with the token: %v", err)
	}

	if err := s.Check("p2", ""); err != nil {
		t.Fatalf("another id: %v", err)
	}

	if err := s.Check("p2", lease.Token); !errors.Is(err, ErrExpired) {
		t.Fatalf("the token on another id: %v, want ErrExpired", err)
	}

	if err := New().Scope("other").Check("p1", ""); err != nil {
		t.Fatalf("another table: %v", err)
	}

	if err := s.Release("p1", "nope"); !errors.Is(err, ErrExpired) {
		t.Fatalf("release with a wrong token: %v", err)
	}

	if err := s.Release("p1", lease.Token); err != nil {
		t.Fatal(err)
	}

	if err := s.Check("p1", lease.Token); !errors.Is(err, ErrExpired) {
		t.Fatalf("the token after the release: %v, want ErrExpired", err)
	}

	if s.t.Any() {
		t.Fatal("the table still holds a lock")
	}
}

func TestALockExpires(t *testing.T) {
	tab := New()
	now := time.Unix(1000, 0)
	tab.now = func() time.Time { return now }

	s := tab.Scope("pay")

	old, err := s.TryAcquire("p1", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	now = now.Add(31 * time.Second)

	if err := s.Check("p1", ""); err != nil {
		t.Fatalf("write after the expiry: %v", err)
	}

	if err := s.Check("p1", old.Token); !errors.Is(err, ErrExpired) {
		t.Fatalf("the old token: %v, want ErrExpired", err)
	}

	fresh, err := s.TryAcquire("p1", 30*time.Second)
	if err != nil || fresh.Token == old.Token {
		t.Fatalf("acquire after the expiry: %+v, %v", fresh, err)
	}

	if err := s.Release("p1", old.Token); !errors.Is(err, ErrExpired) {
		t.Fatalf("release with the old token: %v", err)
	}
}

func TestWaitEndsOnReleaseExpiryAndCancel(t *testing.T) {
	s := New().Scope("pay")

	lease, _ := s.TryAcquire("p1", time.Minute)

	var busy *Busy

	_, err := s.TryAcquire("p1", time.Minute)
	errors.As(err, &busy)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if err := busy.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait on a held lock: %v", err)
	}

	go func() {
		time.Sleep(10 * time.Millisecond)

		_ = s.Release("p1", lease.Token)
	}()

	if err := busy.Wait(context.Background()); err != nil {
		t.Fatalf("wait until the release: %v", err)
	}

	short, _ := s.TryAcquire("p1", 15*time.Millisecond)

	_, err = s.TryAcquire("p1", time.Minute)
	errors.As(err, &busy)

	began := time.Now()

	if err := busy.Wait(context.Background()); err != nil || time.Since(began) > time.Second {
		t.Fatalf("wait until the expiry of %s: %v after %s", short.Token, err, time.Since(began))
	}
}

func TestASetIsTakenWholeOrNotAtAll(t *testing.T) {
	s := New().Scope("acc")

	one, err := s.TryAcquire("a2", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	var busy *Busy

	if _, err := s.TryAcquireSet([]string{"a1", "a2", "a3"}, time.Minute); !errors.As(err, &busy) || busy.ID != "a2" {
		t.Fatalf("set with a held id: %v, want Busy on a2", err)
	}

	if err := s.Check("a1", ""); err != nil {
		t.Fatalf("a1 was locked by a refused set: %v", err)
	}

	_ = s.Release("a2", one.Token)

	set, err := s.TryAcquireSet([]string{"a1", "a2", "a1"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"a1", "a2"} {
		if err := s.Check(id, ""); !errors.As(err, &busy) {
			t.Fatalf("%s is not locked by the set: %v", id, err)
		}

		if err := s.Check(id, set.Token); err != nil {
			t.Fatalf("the token of the set on %s: %v", id, err)
		}
	}

	if n, err := s.ReleaseToken(set.Token); err != nil || n != 2 {
		t.Fatalf("release of the set: %d, %v; want 2", n, err)
	}

	if _, err := s.ReleaseToken(set.Token); !errors.Is(err, ErrExpired) {
		t.Fatalf("a second release: %v, want ErrExpired", err)
	}

	if s.t.Any() {
		t.Fatal("the table still holds a lock")
	}
}

func TestCheckTxNeedsLiveTokensAndTheHolderOfEachID(t *testing.T) {
	tab := New()
	now := time.Unix(1000, 0)
	tab.now = func() time.Time { return now }

	s := tab.Scope("acc")

	mine, _ := s.TryAcquireSet([]string{"a1", "a2"}, 10*time.Second)
	theirs, _ := s.TryAcquire("b1", time.Minute)

	if err := s.CheckTx([]string{"a1", "a2", "new"}, []string{mine.Token}); err != nil {
		t.Fatalf("own set and a free id: %v", err)
	}

	var busy *Busy

	if err := s.CheckTx([]string{"a1", "b1"}, []string{mine.Token}); !errors.As(err, &busy) || busy.ID != "b1" {
		t.Fatalf("an id of another holder: %v, want Busy on b1", err)
	}

	if err := s.CheckTx([]string{"a1"}, []string{mine.Token, theirs.Token}); err != nil {
		t.Fatalf("two live tokens: %v", err)
	}

	if err := New().Scope("other").CheckTx(nil, []string{mine.Token}); !errors.Is(err, ErrExpired) {
		t.Fatalf("a token of another table: %v, want ErrExpired", err)
	}

	if err := tab.Scope("other").CheckTx(nil, []string{mine.Token}); !errors.Is(err, ErrExpired) {
		t.Fatalf("a token of another collection: %v, want ErrExpired", err)
	}

	now = now.Add(11 * time.Second)

	if err := s.CheckTx([]string{"new"}, []string{mine.Token}); !errors.Is(err, ErrExpired) {
		t.Fatalf("an expired token: %v, want ErrExpired", err)
	}

	if err := s.Check("a1", ""); err != nil {
		t.Fatalf("a1 after the expiry of the set: %v", err)
	}
}
