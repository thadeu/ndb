package engine_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
)

func TestPartitionSweepAndRestart(t *testing.T) {
	dir := t.TempDir()
	db := open(t, cfg(dir, nil))

	def, err := db.EnsurePartitioned(catalog.Def{Name: "logs", Time: "ts", PartitionBy: "day", Retention: "2d"})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

	for _, at := range []time.Time{now.AddDate(0, 0, -10), now.AddDate(0, 0, -1), now} {
		col, err := db.PartitionAt(def, at)
		if err != nil {
			t.Fatal(err)
		}

		if _, err := col.Insert(ctx, []byte(`{"ts":"`+at.Format(time.RFC3339)+`"}`)); err != nil {
			t.Fatal(err)
		}
	}

	if got := db.Partitions(def, true, now); !slices.Equal(got, []string{"logs-2026-11-06", "logs-2026-11-07"}) {
		t.Fatalf("live = %v", got)
	}

	dropped, err := db.SweepPartitions(now)
	if err != nil || !slices.Equal(dropped, []string{"logs-2026-10-28"}) {
		t.Fatalf("sweep = %v, %v", dropped, err)
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// The logical collection and its partitions come back after a restart.
	db = open(t, cfg(dir, nil))

	if got := db.Partitions(def, false, now); !slices.Equal(got, []string{"logs-2026-11-06", "logs-2026-11-07"}) {
		t.Fatalf("after restart = %v", got)
	}

	if _, ok := db.Partitioned("logs"); !ok {
		t.Fatal("the logical definition is gone")
	}
}

// In a list of collections, the name of a partitioned collection reads its
// live partitions; the caller picks the list, ndb does not choose from the
// query.
func TestMatchAListWithAPartitionedName(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))

	def, err := db.EnsurePartitioned(catalog.Def{Name: "logs", Time: "ts", PartitionBy: "day"})
	if err != nil {
		t.Fatal(err)
	}

	today := time.Now().UTC()

	for _, at := range []time.Time{today.AddDate(0, 0, -1), today} {
		if _, err := db.PartitionAt(def, at); err != nil {
			t.Fatal(err)
		}
	}

	ensure(t, db, catalog.Def{Name: "other"})

	names, err := db.Match("other,logs,logs-*")
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"logs-" + today.AddDate(0, 0, -1).Format(time.DateOnly), "logs-" + today.Format(time.DateOnly), "other"}

	if !slices.Equal(names, want) {
		t.Fatalf("Match = %v, want %v", names, want)
	}

	if _, err := db.Match("other,nope"); !errors.Is(err, catalog.ErrUnknown) {
		t.Fatalf("an unknown name: %v", err)
	}
}
