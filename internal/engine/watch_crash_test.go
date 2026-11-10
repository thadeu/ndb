package engine_test

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/config"
	"github.com/thadeu/ndb/internal/crashtest"
	"github.com/thadeu/ndb/internal/engine"
)

var crashIterations = flag.Int("crash.iterations", 25, "kills per crash test")

const crashDocs = 120

func TestMain(m *testing.M) {
	crashtest.Children(map[string]func(){"consumer": consumerChild})
	os.Exit(m.Run())
}

// consumerChild reads the feed from its stored position. For each event it
// appends the id to a log (the "work"), then acks. A kill can fall between
// the two, so the next run sees that event again: a duplicate, never a gap.
func consumerChild() {
	dir := os.Getenv("NDB_DIR")

	db, err := engine.Open(config.Config{DataDir: dir, AllowImplicit: true}, slog.New(slog.DiscardHandler))
	if err != nil {
		panic(err)
	}

	logf, err := os.OpenFile(filepath.Join(dir, "work.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		panic(err)
	}

	s, err := db.Watch("posts", "relay", "")
	if err != nil {
		panic(err)
	}

	for n := int64(1); ; n++ {
		ev, err := s.Next(context.Background())
		if err != nil {
			panic(err)
		}

		fmt.Fprintln(logf, ev.ID)

		if err := db.Ack("posts", "relay", ev.Pos); err != nil {
			panic(err)
		}

		crashtest.Ack(n)
	}
}

func workLog(t *testing.T, dir string) []string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(dir, "work.log"))
	if os.IsNotExist(err) {
		return nil
	}

	if err != nil {
		t.Fatal(err)
	}

	var ids []string

	sc := bufio.NewScanner(strings.NewReader(string(raw)))

	for sc.Scan() {
		if id := sc.Text(); id != "" {
			ids = append(ids, id)
		}
	}

	return ids
}

// unacked returns the ids of the events after the stored position.
func unacked(t *testing.T, db *engine.DB) []string {
	t.Helper()

	s, err := db.Watch("posts", "relay", "")
	if err != nil {
		t.Fatal(err)
	}

	defer s.Close()

	var ids []string

	for {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		ev, err := s.Next(ctx)

		cancel()

		if err != nil {
			return ids
		}

		ids = append(ids, ev.ID)
	}
}

// TestWatchConsumerKilledAtRandomPointsLosesNothing kills a consumer process
// again and again. After each kill, every event before the stored position
// must be in the work log (no gap). Duplicates in the log are allowed.
func TestWatchConsumerKilledAtRandomPointsLosesNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("heavy: it runs without -short (make test), not in CI")
	}

	dir := t.TempDir()

	db := open(t, cfg(dir, nil))

	insertIDs(t, db, 1, crashDocs)
	db.Close()

	check := func(t *testing.T, _ int64) {
		db, err := engine.Open(cfg(dir, nil), slog.New(slog.DiscardHandler))
		if err != nil {
			t.Errorf("reopen: %v", err)

			return
		}

		defer db.Close()

		done := map[string]bool{}

		for _, id := range workLog(t, dir) {
			done[id] = true
		}

		rest := unacked(t, db)
		acked := crashDocs - len(rest)

		for i := 1; i <= acked; i++ {
			if id := fmt.Sprintf("p%d", i); !done[id] {
				t.Errorf("gap: %s is acked but missing from the work log", id)

				return
			}
		}
	}

	crashtest.Run(t, crashtest.Config{
		Child:      "consumer",
		Env:        []string{"NDB_DIR=" + dir},
		Iterations: *crashIterations,
		MaxDelay:   150 * time.Millisecond,
		Check:      check,
	})

	db, err := engine.Open(cfg(dir, nil), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	defer db.Close()

	seen := map[string]int{}

	for _, id := range append(workLog(t, dir), unacked(t, db)...) {
		seen[id]++
	}

	dups := 0

	for i := 1; i <= crashDocs; i++ {
		n := seen[fmt.Sprintf("p%d", i)]

		if n == 0 {
			t.Fatalf("p%d was never delivered", i)
		}

		dups += n - 1
	}

	t.Logf("%d docs, %d kills, %d duplicate deliveries", crashDocs, *crashIterations, dups)
}
