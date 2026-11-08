package collection

import (
	"container/heap"
	"sync"
	"sync/atomic"
	"time"
)

// The periodic work of the collections (seal, WAL truncation, compaction,
// TTL sweep) runs from one scheduler, not from a ticker per collection and
// job. A job runs at its deadline and gives the next one, or none when it
// has nothing left to do; a write gives the seal job a deadline again. A
// collection at rest has no deadline and costs no CPU, and the scheduler
// goroutine exits when no job has one.

var sched = &scheduler{wake: make(chan struct{}, 1)}

type scheduler struct {
	mu      sync.Mutex
	queue   jobQueue
	looping bool
	wake    chan struct{}
}

// job is one kind of periodic work of one collection. It never runs twice
// at once.
type job struct {
	run func() time.Time

	// at and idx are the deadline and the queue position while queued (idx
	// -1 otherwise); again is a deadline asked while the job runs. Guarded
	// by sched.mu. due mirrors at for the fast path of poke (0: not queued).
	at      time.Time
	idx     int
	busy    bool
	again   time.Time
	stopped bool
	due     atomic.Int64
	wg      sync.WaitGroup
}

func newJob(run func() time.Time) *job {
	return &job{run: run, idx: -1}
}

// poke gives j the deadline t, or keeps an earlier one.
func (j *job) poke(t time.Time) {
	if j == nil {
		return
	}

	if due := j.due.Load(); due != 0 && due <= t.UnixNano() {
		return
	}

	s := sched

	s.mu.Lock()
	defer s.mu.Unlock()

	switch {
	case j.stopped:
	case j.busy:
		if j.again.IsZero() || t.Before(j.again) {
			j.again = t
		}
	case j.idx >= 0:
		if t.Before(j.at) {
			j.at = t
			j.due.Store(t.UnixNano())
			heap.Fix(&s.queue, j.idx)
			s.signal()
		}
	default:
		s.push(j, t)
	}
}

// stop removes j and waits for a run in progress. j never runs again.
func (j *job) stop() {
	if j == nil {
		return
	}

	s := sched

	s.mu.Lock()

	j.stopped = true
	j.due.Store(0)

	if j.idx >= 0 {
		heap.Remove(&s.queue, j.idx)
	}

	s.mu.Unlock()

	j.wg.Wait()
}

// push queues j at t. Callers hold s.mu.
func (s *scheduler) push(j *job, t time.Time) {
	j.at = t
	j.due.Store(t.UnixNano())
	heap.Push(&s.queue, j)
	s.signal()
}

// signal starts the loop, or wakes it for a new earliest deadline. Callers
// hold s.mu.
func (s *scheduler) signal() {
	if !s.looping {
		s.looping = true

		go s.loop()

		return
	}

	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// loop starts each job whose deadline has come, and sleeps until the next
// one. It returns when the queue is empty.
func (s *scheduler) loop() {
	timer := time.NewTimer(time.Hour)
	timer.Stop()

	s.mu.Lock()

	for len(s.queue) > 0 {
		j := s.queue[0]

		if wait := time.Until(j.at); wait > 0 {
			s.mu.Unlock()
			timer.Reset(wait)

			select {
			case <-timer.C:
			case <-s.wake:
				timer.Stop()
			}

			s.mu.Lock()

			continue
		}

		heap.Pop(&s.queue)
		j.due.Store(0)
		j.busy = true
		j.wg.Add(1)

		go s.exec(j)
	}

	s.looping = false
	s.mu.Unlock()
}

// exec runs j and queues it at the deadline it gives (or one asked while
// it ran).
func (s *scheduler) exec(j *job) {
	defer j.wg.Done()

	next := j.run()

	s.mu.Lock()
	defer s.mu.Unlock()

	if !j.again.IsZero() && (next.IsZero() || j.again.Before(next)) {
		next = j.again
	}

	j.busy, j.again = false, time.Time{}

	if !j.stopped && !next.IsZero() {
		s.push(j, next)
	}
}

// pending is how many jobs have a deadline (tests).
func (s *scheduler) pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.queue)
}

type jobQueue []*job

func (q jobQueue) Len() int           { return len(q) }
func (q jobQueue) Less(a, b int) bool { return q[a].at.Before(q[b].at) }

func (q jobQueue) Swap(a, b int) {
	q[a], q[b] = q[b], q[a]
	q[a].idx, q[b].idx = a, b
}

func (q *jobQueue) Push(x any) {
	j := x.(*job)
	j.idx = len(*q)
	*q = append(*q, j)
}

func (q *jobQueue) Pop() any {
	old := *q
	j := old[len(old)-1]
	old[len(old)-1] = nil
	j.idx = -1
	*q = old[:len(old)-1]

	return j
}
