package collection_test

import (
	"errors"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
)

func codeIs(t *testing.T, err error, want string) {
	t.Helper()

	var coded interface{ Code() string }

	if !errors.As(err, &coded) || coded.Code() != want {
		t.Fatalf("error = %v, want code %s", err, want)
	}
}

func TestInsertInStampsTheExpiry(t *testing.T) {
	clk := newClock()
	c := openTTL(t, catalog.Def{ExpiresField: "exp"}, clk, false)

	if _, err := c.InsertIn(ctx, time.Hour, doc(`{"id":"a","exp":"2999-01-01T00:00:00Z"}`), doc(`{"id":"b"}`)); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"a", "b"} {
		info, err := c.TTL(id)
		if err != nil {
			t.Fatal(err)
		}

		if info.Remaining != time.Hour {
			t.Fatalf("%s remaining = %v, want 1h (expire_in wins)", id, info.Remaining)
		}
	}

	clk.advance(time.Hour + time.Second)

	if n := countOf(t, c, `{}`); n != 0 {
		t.Fatalf("%d visible after the expiry", n)
	}
}

func TestExpiryNeedsExpiresField(t *testing.T) {
	clk := newClock()

	plain := openTTL(t, catalog.Def{}, clk, false)

	_, err := plain.InsertIn(ctx, time.Hour, doc(`{"id":"a"}`))
	codeIs(t, err, "ttl_not_enabled")

	derived := openTTL(t, catalog.Def{TTL: "1h", Time: "at"}, clk, false)

	_, _, err = derived.Expire(ctx, "a", collection.Expiry{In: time.Hour}, 0)
	codeIs(t, err, "ttl_derived")

	_, err = derived.Persist(ctx, "a")
	codeIs(t, err, "ttl_derived")

	_, err = derived.Update(ctx, "a", collection.Change{ExpireIn: time.Hour}, 0)
	codeIs(t, err, "ttl_derived")
}

func TestUpdateExpireInWinsOverSetAndUnset(t *testing.T) {
	clk := newClock()
	c := openTTL(t, catalog.Def{ExpiresField: "exp"}, clk, false)

	if _, err := c.Insert(ctx, doc(`{"id":"a","n":1}`)); err != nil {
		t.Fatal(err)
	}

	ch := collection.Change{Set: map[string]any{"n": 2, "exp": "2999-01-01T00:00:00Z"}, Unset: []string{"exp"}, ExpireIn: 30 * time.Minute}

	v, err := c.Update(ctx, "a", ch, 0)
	if err != nil {
		t.Fatal(err)
	}

	if v != 2 {
		t.Fatalf("v = %d", v)
	}

	info, err := c.TTL("a")
	if err != nil {
		t.Fatal(err)
	}

	if info.Remaining != 30*time.Minute {
		t.Fatalf("remaining = %v", info.Remaining)
	}
}

func TestExpireAndPersist(t *testing.T) {
	for _, seal := range []bool{false, true} {
		clk := newClock()
		c := openTTL(t, catalog.Def{ExpiresField: "exp"}, clk, seal)

		if _, err := c.Insert(ctx, doc(`{"id":"s1"}`)); err != nil {
			t.Fatal(err)
		}

		info, err := c.TTL("s1")
		if err != nil || !info.Never {
			t.Fatalf("ttl = %+v, %v; want never", info, err)
		}

		v, at, err := c.Expire(ctx, "s1", collection.Expiry{In: 30 * time.Minute}, 0)
		if err != nil {
			t.Fatal(err)
		}

		if v != 2 || !at.Equal(clk.now().Add(30*time.Minute)) {
			t.Fatalf("v = %d, at = %v", v, at)
		}

		_, _, err = c.Expire(ctx, "s1", collection.Expiry{In: time.Hour}, 1)
		codeIs(t, err, "version_conflict")

		clk.advance(10 * time.Minute)

		info, _ = c.TTL("s1")
		if info.Remaining != 20*time.Minute {
			t.Fatalf("remaining = %v", info.Remaining)
		}

		v, err = c.Persist(ctx, "s1")
		if err != nil || v != 3 {
			t.Fatalf("persist v = %d, %v", v, err)
		}

		clk.advance(24 * time.Hour)

		info, err = c.TTL("s1")
		if err != nil || !info.Never {
			t.Fatalf("after persist: %+v, %v", info, err)
		}

		if _, _, err := c.Expire(ctx, "s1", collection.Expiry{At: clk.now().Add(time.Minute)}, 0); err != nil {
			t.Fatal(err)
		}

		clk.advance(2 * time.Minute)

		_, err = c.TTL("s1")
		codeIs(t, err, "not_found")

		_, _, err = c.Expire(ctx, "s1", collection.Expiry{In: time.Hour}, 0)
		codeIs(t, err, "not_found")

		_, err = c.Persist(ctx, "s1")
		codeIs(t, err, "not_found")

		_, _, err = c.Expire(ctx, "missing", collection.Expiry{In: time.Hour}, 0)
		codeIs(t, err, "not_found")
	}
}

func TestExpireNeedsATime(t *testing.T) {
	c := openTTL(t, catalog.Def{ExpiresField: "exp"}, newClock(), false)

	if _, err := c.Insert(ctx, doc(`{"id":"a"}`)); err != nil {
		t.Fatal(err)
	}

	_, _, err := c.Expire(ctx, "a", collection.Expiry{}, 0)
	codeIs(t, err, "invalid_request")
}

func TestTTLDerivedAndEarliest(t *testing.T) {
	clk := newClock()
	c := openTTL(t, catalog.Def{TTL: "1h", Time: "at", ExpiresField: "exp"}, clk, false)

	if _, err := c.Insert(ctx, doc(`{"id":"a","at":"`+rfc(clk.now())+`"}`), doc(`{"id":"b","at":"`+rfc(clk.now())+`","exp":"`+rfc(clk.now().Add(10*time.Minute))+`"}`)); err != nil {
		t.Fatal(err)
	}

	a, _ := c.TTL("a")
	b, _ := c.TTL("b")

	if a.Remaining != time.Hour || b.Remaining != 10*time.Minute {
		t.Fatalf("a = %v, b = %v", a.Remaining, b.Remaining)
	}
}
