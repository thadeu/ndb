package ndb

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sync"
	"testing"
	"time"
)

// TestPoolBench measures a peak of goroutines that call the daemon at
// once, with no connection limit and with the default one, over unix and
// TCP. It runs only with NDB_POOL_BENCH=1:
//
//	NDB_POOL_BENCH=1 go test -run TestPoolBench -v .
func TestPoolBench(t *testing.T) {
	if os.Getenv("NDB_POOL_BENCH") != "1" {
		t.Skip("set NDB_POOL_BENCH=1")
	}

	for _, addr := range []string{daemon.unix, daemon.tcp} {
		for _, maxConns := range []int{0, DefaultMaxConns} {
			for _, n := range []int{100, 500, 1000} {
				t.Logf("%-5s max %-3d %4d goroutines: %s", addr[:3], maxConns, n, peak(t, addr, maxConns, n))
			}
		}
	}
}

// peak runs n goroutines, 20 counts each, and gives p50, p99, max and the
// total time.
func peak(t *testing.T, addr string, maxConns, n int) string {
	t.Helper()

	db, err := Connect(addr, WithMaxConns(maxConns))

	if err != nil {
		t.Fatal(err)
	}

	defer db.Close()

	calls, err := Collection[Job](db, newColl())

	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	if _, err := calls.Insert(ctx, Job{Status: "new"}); err != nil {
		t.Fatal(err)
	}

	var (
		mu     sync.Mutex
		lats   []time.Duration
		failed int
		wg     sync.WaitGroup
	)

	start := time.Now()

	for range n {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for range 20 {
				began := time.Now()
				_, err := calls.Count(ctx)
				took := time.Since(began)

				mu.Lock()
				lats = append(lats, took)

				if err != nil {
					failed++
				}

				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	total := time.Since(start)

	slices.Sort(lats)

	at := func(q float64) time.Duration { return lats[int(q*float64(len(lats)-1))] }

	return fmt.Sprintf("p50 %s, p99 %s, max %s, total %s, %d failed",
		at(0.5).Round(time.Microsecond*10), at(0.99).Round(time.Microsecond*10), lats[len(lats)-1].Round(time.Millisecond), total.Round(time.Millisecond), failed)
}
