package logging

import (
	"bytes"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// Defaults of Async.
const (
	// AsyncMax is the most bytes that wait in memory. A line that does not
	// fit is dropped and counted: a log never blocks a request.
	AsyncMax = 4 << 20

	// asyncFlushAt wakes the flusher before its tick when this many bytes
	// wait.
	asyncFlushAt = 64 << 10

	// asyncEvery is the longest time a line waits in memory.
	asyncEvery = 100 * time.Millisecond
)

// Async is an io.Writer that keeps the lines in memory and writes them to
// out from one goroutine, in large blocks. A Write is a copy into a buffer:
// no system call, so a debug log does not slow the requests.
//
//   - The lines keep their order.
//   - An ERROR line is written at once, after the lines before it, so the
//     line before an exit (a WAL failure) is never lost.
//   - When max bytes wait, new lines are dropped; after the next block, a
//     WARN line gives how many.
//   - Close writes what waits and stops the goroutine.
type Async struct {
	out io.Writer
	max int

	// mu guards buf; wmu orders the writes to out. A writer takes wmu
	// before it lets mu go, so the blocks keep the order of the lines.
	mu    sync.Mutex
	wmu   sync.Mutex
	buf   []byte
	spare []byte

	dropped atomic.Int64

	wake chan struct{}
	stop chan struct{}
	done chan struct{}

	closeOnce sync.Once
}

// NewAsync starts the goroutine that writes to out. max <= 0 is AsyncMax.
func NewAsync(out io.Writer, max int) *Async {
	if max <= 0 {
		max = AsyncMax
	}

	a := &Async{
		out:  out,
		max:  max,
		buf:  make([]byte, 0, asyncFlushAt),
		wake: make(chan struct{}, 1),
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}

	go a.loop()

	return a
}

// Write keeps a copy of p. It never blocks on out, except for an ERROR
// line.
func (a *Async) Write(p []byte) (int, error) {
	if isError(p) {
		a.flush(p)

		return len(p), nil
	}

	a.mu.Lock()

	if len(a.buf)+len(p) > a.max {
		a.mu.Unlock()
		a.dropped.Add(1)

		return len(p), nil
	}

	a.buf = append(a.buf, p...)
	full := len(a.buf) >= asyncFlushAt

	a.mu.Unlock()

	if full {
		select {
		case a.wake <- struct{}{}:
		default:
		}
	}

	return len(p), nil
}

// Flush writes what waits now.
func (a *Async) Flush() {
	a.flush(nil)
}

// Close writes what waits and stops the goroutine. It is safe to call more
// than once; a Write after Close is written at once.
func (a *Async) Close() error {
	a.closeOnce.Do(func() {
		close(a.stop)
		<-a.done
	})

	a.flush(nil)

	return nil
}

func (a *Async) loop() {
	defer close(a.done)

	tick := time.NewTicker(asyncEvery)

	defer tick.Stop()

	for {
		select {
		case <-a.stop:
			return
		case <-tick.C:
		case <-a.wake:
		}

		a.flush(nil)
	}
}

// flush takes the waiting lines, adds tail, and writes them in one call.
func (a *Async) flush(tail []byte) {
	a.mu.Lock()

	block := a.buf
	a.buf = a.spare[:0]

	a.wmu.Lock()
	a.mu.Unlock()

	defer a.wmu.Unlock()

	if len(block) > 0 {
		_, _ = a.out.Write(block)
	}

	// The lines were dropped after the block, so the warning comes after it.
	if n := a.dropped.Swap(0); n > 0 {
		fmt.Fprintf(a.out, "level=WARN msg=\"log lines dropped: the log buffer was full\" lines=%d\n", n)
	}

	if len(tail) > 0 {
		_, _ = a.out.Write(tail)
	}

	// The block is the next spare buffer, unless it grew too large to keep.
	a.mu.Lock()

	if cap(block) <= 2*asyncFlushAt {
		a.spare = block[:0]
	} else {
		a.spare = nil
	}

	a.mu.Unlock()
}

// isError tells whether a text or JSON slog line has the ERROR level. The
// level is near the start of the line.
func isError(p []byte) bool {
	head := p[:min(len(p), 96)]

	return bytes.Contains(head, []byte("level=ERROR")) || bytes.Contains(head, []byte(`"level":"ERROR"`))
}
