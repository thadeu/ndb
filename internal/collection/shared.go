package collection

import (
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultWriteBuffer is the memtable budget of a daemon: the documents of
// every collection that are not sealed yet.
const DefaultWriteBuffer = 32 << 20

// Shared is what the collections of one daemon share, so the memory of the
// daemon does not grow with the number of collections (or partitions):
//   - one write buffer budget: over it, the largest memtable is sealed (a
//     job checks it while some memtable holds writes);
//   - one slot for heavy work: a seal or a compaction runs alone.
//
// A nil *Shared gives each collection its own budget and slot (tests, tools).
type Shared struct {
	writeBuffer int64

	mu   sync.Mutex
	mem  map[*Collection]int64
	slot chan struct{}

	// tick is the budget check period, the shortest seal tick (ns).
	tick atomic.Int64

	budgetJob *job
}

// NewShared returns the shared state of a daemon. writeBuffer ≤ 0 is the
// default.
func NewShared(writeBuffer int64) *Shared {
	if writeBuffer <= 0 {
		writeBuffer = DefaultWriteBuffer
	}

	s := &Shared{writeBuffer: writeBuffer, mem: map[*Collection]int64{}, slot: make(chan struct{}, 1)}

	s.tick.Store(int64(defaultSealTick))

	s.budgetJob = newJob(s.checkBudget)

	return s
}

// join adds c to the budget, checked every tick (the shortest of the
// collections).
func (s *Shared) join(c *Collection, tick time.Duration) {
	if s == nil {
		return
	}

	s.mu.Lock()
	s.mem[c] = 0
	s.tick.Store(min(s.tick.Load(), int64(tick)))
	s.mu.Unlock()
}

// written gives the budget check a deadline after a write.
func (s *Shared) written() {
	if s == nil {
		return
	}

	s.budgetJob.poke(time.Now().Add(time.Duration(s.tick.Load())))
}

// checkBudget reads the memtable sizes and, over the write buffer, gives
// the largest memtable a seal now. It checks again a tick later while some
// memtable holds writes, and stops when none does.
func (s *Shared) checkBudget() time.Time {
	s.mu.Lock()

	cols := make([]*Collection, 0, len(s.mem))

	for c := range s.mem {
		cols = append(cols, c)
	}

	tick := time.Duration(s.tick.Load())

	s.mu.Unlock()

	var (
		total   int64
		largest *Collection
		most    int64
		sizes   = make([]int64, len(cols))
	)

	for i, c := range cols {
		c.mu.RLock()
		sizes[i] = c.memBytes
		c.mu.RUnlock()

		total += sizes[i]

		if sizes[i] > most {
			largest, most = c, sizes[i]
		}
	}

	s.mu.Lock()

	for i, c := range cols {
		if _, ok := s.mem[c]; ok {
			s.mem[c] = sizes[i]
		}
	}

	s.mu.Unlock()

	if total > s.writeBuffer && largest != nil {
		largest.seal.sealJob.poke(time.Now())
	}

	if total == 0 {
		return time.Time{}
	}

	return time.Now().Add(tick)
}

// overBudget records the memtable size of c and tells if c must seal to keep
// the daemon under its write buffer: the sum is over the budget and c holds
// the largest memtable.
func (s *Shared) overBudget(c *Collection, mem int64) bool {
	if s == nil {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.mem[c] = mem

	var total, largest int64

	for _, n := range s.mem {
		total += n
		largest = max(largest, n)
	}

	return total > s.writeBuffer && mem > 0 && mem == largest
}

// budget is the write buffer; without a Shared, no limit.
func (s *Shared) budget() int64 {
	if s == nil {
		return math.MaxInt64
	}

	return s.writeBuffer
}

// forget removes a closed collection from the budget.
func (s *Shared) forget(c *Collection) {
	if s == nil {
		return
	}

	s.mu.Lock()
	delete(s.mem, c)
	s.mu.Unlock()
}

// acquire takes the heavy work slot. It returns false when stop closes first.
func (s *Shared) acquire(stop <-chan struct{}) bool {
	if s == nil {
		return true
	}

	select {
	case s.slot <- struct{}{}:
		return true
	case <-stop:
		return false
	}
}

func (s *Shared) release() {
	if s != nil {
		<-s.slot
	}
}
