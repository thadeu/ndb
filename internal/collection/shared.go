package collection

import (
	"math"
	"sync"
)

// DefaultWriteBuffer is the memtable budget of a daemon: the documents of
// every collection that are not sealed yet.
const DefaultWriteBuffer = 32 << 20

// Shared is what the collections of one daemon share, so the memory of the
// daemon does not grow with the number of collections (or partitions):
//   - one write buffer budget: over it, the largest memtable is sealed;
//   - one slot for heavy work: a seal or a compaction runs alone.
//
// A nil *Shared gives each collection its own budget and slot (tests, tools).
type Shared struct {
	writeBuffer int64

	mu   sync.Mutex
	mem  map[*Collection]int64
	slot chan struct{}
}

// NewShared returns the shared state of a daemon. writeBuffer ≤ 0 is the
// default.
func NewShared(writeBuffer int64) *Shared {
	if writeBuffer <= 0 {
		writeBuffer = DefaultWriteBuffer
	}

	return &Shared{writeBuffer: writeBuffer, mem: map[*Collection]int64{}, slot: make(chan struct{}, 1)}
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
