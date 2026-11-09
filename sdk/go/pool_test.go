package ndb

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestConnectLimitsConnectionsByDefault(t *testing.T) {
	db, err := Connect(daemon.unix)

	if err != nil {
		t.Fatal(err)
	}

	defer db.Close()

	if got := transportOf(db.client).MaxConnsPerHost; got != DefaultMaxConns {
		t.Fatalf("MaxConnsPerHost = %d, want %d", got, DefaultMaxConns)
	}

	if got := transportOf(db.watches).MaxConnsPerHost; got != 0 {
		t.Fatalf("the watch client has a limit of %d connections, want none", got)
	}
}

func TestWithMaxConnsAndIdleTimeout(t *testing.T) {
	db, err := Connect(daemon.unix, WithMaxConns(8), WithIdleTimeout(5*time.Second))

	if err != nil {
		t.Fatal(err)
	}

	defer db.Close()

	tr := transportOf(db.client)

	if tr.MaxConnsPerHost != 8 || tr.MaxIdleConnsPerHost != 16 || tr.IdleConnTimeout != 5*time.Second {
		t.Fatalf("transport = max %d, idle %d, idle timeout %s", tr.MaxConnsPerHost, tr.MaxIdleConnsPerHost, tr.IdleConnTimeout)
	}
}

// Many goroutines share a pool of 2 connections: every call waits for a
// free one and none fails.
func TestCallsWaitForAFreeConnection(t *testing.T) {
	db, err := Connect(daemon.unix, WithMaxConns(2))

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

	var wg sync.WaitGroup

	errs := make(chan error, 100)

	for range 100 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			if _, err := calls.Count(ctx); err != nil {
				errs <- err
			}
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatal(err)
	}
}

// A watch holds its connection for as long as it runs: with a pool of 1,
// the other calls still go through.
func TestAWatchDoesNotTakeThePool(t *testing.T) {
	db, err := Connect(daemon.unix, WithMaxConns(1))

	if err != nil {
		t.Fatal(err)
	}

	defer db.Close()

	calls, err := Collection[Job](db, newColl())

	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	defer cancel()

	started := make(chan struct{})

	go func() {
		for range calls.Watch(ctx, "pool", FromNow()) {
			return
		}
	}()

	go func() {
		time.Sleep(200 * time.Millisecond)
		close(started)
	}()

	<-started

	count, cancelCount := context.WithTimeout(context.Background(), 3*time.Second)

	defer cancelCount()

	if _, err := calls.Count(count); err != nil {
		t.Fatalf("a call with a watch open and a pool of 1: %v", err)
	}
}

func transportOf(c *http.Client) *http.Transport {
	return c.Transport.(*http.Transport)
}
