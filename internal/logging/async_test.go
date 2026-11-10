package logging_test

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/logging"
)

// out is a goroutine-safe buffer that counts the Write calls.
type out struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	calls int
}

func (o *out) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.calls++

	return o.buf.Write(p)
}

func (o *out) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()

	return o.buf.String()
}

// Lines wait in memory and come out in order, in few writes.
func TestAsyncKeepsTheOrderInFewWrites(t *testing.T) {
	o := &out{}
	a := logging.NewAsync(o, 0)

	for i := range 1000 {
		_, _ = a.Write([]byte("level=DEBUG msg=n" + strings.Repeat("x", i%7) + "\n"))
	}

	_ = a.Close()

	if got := strings.Count(o.String(), "\n"); got != 1000 {
		t.Fatalf("lines = %d, want 1000", got)
	}

	if o.calls > 10 {
		t.Fatalf("%d writes for 1000 lines: the lines are not written in blocks", o.calls)
	}
}

// An ERROR line is written at once, after the lines before it.
func TestAsyncWritesAnErrorAtOnce(t *testing.T) {
	o := &out{}
	a := logging.NewAsync(o, 0)

	defer a.Close()

	_, _ = a.Write([]byte("time=x level=INFO msg=before\n"))
	_, _ = a.Write([]byte("time=x level=ERROR msg=boom\n"))

	got := o.String()

	if got != "time=x level=INFO msg=before\ntime=x level=ERROR msg=boom\n" {
		t.Fatalf("out = %q", got)
	}

	_, _ = a.Write([]byte(`{"time":"x","level":"ERROR","msg":"json"}` + "\n"))

	if !strings.Contains(o.String(), `"msg":"json"`) {
		t.Fatal("a JSON ERROR line was not written at once")
	}
}

// When the buffer is full, lines are dropped and counted, and Write does
// not block.
func TestAsyncDropsWhenFull(t *testing.T) {
	o := &out{}
	a := logging.NewAsync(o, 100)

	line := []byte("level=DEBUG " + strings.Repeat("y", 40) + "\n")

	for range 10 {
		_, _ = a.Write(line)
	}

	_ = a.Close()

	got := o.String()

	// One line of 53 bytes fits in 100; the other 9 are dropped.
	if !strings.HasPrefix(got, string(line)) || !strings.Contains(got, "log lines dropped") || !strings.Contains(got, "lines=9") {
		t.Fatalf("out = %q", got)
	}
}

// A line waits at most about one tick.
func TestAsyncWritesWithinATick(t *testing.T) {
	o := &out{}
	a := logging.NewAsync(o, 0)

	defer a.Close()

	_, _ = a.Write([]byte("level=INFO msg=soon\n"))

	deadline := time.Now().Add(2 * time.Second)

	for !strings.Contains(o.String(), "soon") {
		if time.Now().After(deadline) {
			t.Fatal("the line was not written within the tick")
		}

		time.Sleep(10 * time.Millisecond)
	}
}

// Many goroutines can write at once.
func TestAsyncConcurrentWrites(t *testing.T) {
	o := &out{}
	a := logging.NewAsync(o, 0)

	var wg sync.WaitGroup

	for range 8 {
		wg.Go(func() {
			for range 500 {
				_, _ = a.Write([]byte("level=DEBUG msg=c\n"))
			}
		})
	}

	wg.Wait()

	_ = a.Close()

	if got := strings.Count(o.String(), "\n"); got != 4000 {
		t.Fatalf("lines = %d, want 4000", got)
	}
}
