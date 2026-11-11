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
