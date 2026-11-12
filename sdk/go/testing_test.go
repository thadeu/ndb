package ndb

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestCleanTruncatesTheCollectionsTheDBUsed(t *testing.T) {
	ctx := context.Background()
	db, err := Connect(daemon.unix)

	if err != nil {
		t.Fatal(err)
	}

	defer db.Close()

	a, _ := Collection[Job](db, newColl())
	b, _ := Collection[Job](db, newColl())

	for _, c := range []*Coll[Job]{a, b} {
		if _, err := c.Insert(ctx, Job{Status: "new"}); err != nil {
			t.Fatal(err)
		}
	}

	cleaned, err := Testing.Clean(ctx, db, CleanExcept(b.Name()))

	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(cleaned, []string{a.Name()}) {
		t.Fatalf("cleaned %v, want [%s]", cleaned, a.Name())
	}

	if n, _ := a.Count(ctx); n != 0 {
		t.Fatalf("%s has %d docs after the clean", a.Name(), n)
	}

	if n, _ := b.Count(ctx); n != 1 {
		t.Fatalf("%s (except) has %d docs, want 1", b.Name(), n)
	}

	again, err := Testing.Clean(ctx, db)

	if err != nil || len(again) != 0 {
		t.Fatalf("a second clean gave %v, %v: the used list must be empty", again, err)
	}
}

func TestCleanOnlyAndUnknownCollections(t *testing.T) {
	ctx := context.Background()
	db, _ := Connect(daemon.unix)

	defer db.Close()

	cleaned, err := Testing.Clean(ctx, db, CleanOnly("never_made_"+newColl()))

	if err != nil || len(cleaned) != 1 {
		t.Fatalf("clean of an unknown collection: %v, %v", cleaned, err)
	}
}

// NDB_PREFIX gives the prefix to a DB that has no WithPrefix, so a test
// that starts the app from env vars can have its own collections.
func TestPrefixFromTheEnv(t *testing.T) {
	t.Setenv("NDB_PREFIX", "env_")

	db, _ := Connect(daemon.unix)

	defer db.Close()

	c, _ := Collection[Job](db, "calls")

	if c.Name() != "env_calls" {
		t.Fatalf("name %q, want env_calls", c.Name())
	}

	mine, _ := Connect(daemon.unix, WithPrefix("opt_"))

	defer mine.Close()

	d, _ := Collection[Job](mine, "calls")

	if d.Name() != "opt_calls" {
		t.Fatalf("WithPrefix must win: %q", d.Name())
	}
}

func TestWithPrefixAndCleanAll(t *testing.T) {
	ctx := context.Background()
	prefix := "w" + newColl() + "_"
	db, _ := Connect(daemon.unix, WithPrefix(prefix))

	defer db.Close()

	c, _ := Collection[Job](db, "calls")

	if c.Name() != prefix+"calls" {
		t.Fatalf("name %q, want %q", c.Name(), prefix+"calls")
	}

	if _, err := c.Insert(ctx, Job{Status: "new"}); err != nil {
		t.Fatal(err)
	}

	cleaned, err := Testing.CleanAll(ctx, db)

	if err != nil || !slices.Equal(cleaned, []string{prefix + "calls"}) {
		t.Fatalf("CleanAll gave %v, %v; want only the prefixed collection", cleaned, err)
	}

	if n, _ := c.Count(ctx); n != 0 {
		t.Fatalf("%d docs after CleanAll", n)
	}
}

func TestCleanIsAllowedUnderGoTest(t *testing.T) {
	if err := guardClean(); errors.Is(err, ErrUnsafeClean) {
		t.Fatal("go test must allow a clean")
	}
}
