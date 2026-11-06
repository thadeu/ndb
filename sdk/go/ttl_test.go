package ndb

import (
	"context"
	"errors"
	"testing"
	"time"
)

type Session struct {
	ID        string    `ndb:"_id"`
	User      string    `ndb:"user"`
	ExpiresAt time.Time `ndb:"expires_at,expires"`
}

func TestExpireInInsertAndUpdate(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, daemon.unix)

	sessions, err := Collection[Session](ctx, db, newColl())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := sessions.InsertWith(ctx, []Session{{ID: "a", User: "u1"}, {ID: "b"}}, ExpireIn(time.Hour)); err != nil {
		t.Fatal(err)
	}

	left, expires, err := sessions.TTL(ctx, "a")
	if err != nil || !expires || left < 59*time.Minute || left > time.Hour {
		t.Fatalf("ttl a = %v %v %v", left, expires, err)
	}

	if _, err := sessions.Update(ctx, Eq("_id", "a"), Ops{"$set": {"user": "u2"}}, ExpireIn(30*time.Minute)); err != nil {
		t.Fatal(err)
	}

	left, _, _ = sessions.TTL(ctx, "a")
	if left < 29*time.Minute || left > 30*time.Minute {
		t.Fatalf("ttl after update = %v", left)
	}
}

func TestExpirePersistTTL(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, daemon.unix)

	sessions, err := Collection[Session](ctx, db, newColl())
	if err != nil {
		t.Fatal(err)
	}

	far := time.Date(2999, 1, 1, 0, 0, 0, 0, time.UTC)

	if _, err := sessions.Insert(ctx, Session{ID: "s1", ExpiresAt: far}); err != nil {
		t.Fatal(err)
	}

	v, err := sessions.Expire(ctx, "s1", 10*time.Minute)
	if err != nil || v != 2 {
		t.Fatalf("expire = %d, %v", v, err)
	}

	if _, err := sessions.ExpireAt(ctx, "s1", time.Now().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}

	left, expires, _ := sessions.TTL(ctx, "s1")
	if !expires || left < 119*time.Minute {
		t.Fatalf("ttl = %v %v", left, expires)
	}

	if v, err := sessions.Persist(ctx, "s1"); err != nil || v != 4 {
		t.Fatalf("persist = %d, %v", v, err)
	}

	if _, expires, err := sessions.TTL(ctx, "s1"); err != nil || expires {
		t.Fatalf("ttl after persist: expires %v err %v", expires, err)
	}

	if _, err := sessions.ExpireAt(ctx, "s1", time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}

	if _, _, err := sessions.TTL(ctx, "s1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ttl of expired = %v", err)
	}

	if _, err := sessions.Expire(ctx, "s1", time.Hour); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expire of expired = %v", err)
	}

	if _, err := sessions.Persist(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("persist of missing = %v", err)
	}
}

func TestExpireWithoutExpiresField(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, daemon.unix)

	calls, err := Collection[Call](ctx, db, newColl())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := calls.InsertWith(ctx, []Call{{CallID: "c1"}}, ExpireIn(time.Hour)); !errors.Is(err, ErrTTLNotEnabled) {
		t.Fatalf("insert expire_in = %v", err)
	}

	if _, err := calls.Expire(ctx, "c1", time.Hour); !errors.Is(err, ErrTTLNotEnabled) {
		t.Fatalf("expire = %v", err)
	}
}
