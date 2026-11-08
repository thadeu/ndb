package engine

import (
	"fmt"
	"runtime/debug"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/config"
)

// An engine collection that no call used for idle_close (default 10
// minutes) sleeps: it seals its memtable and closes, so it holds no open
// file, no WAL reserve, no memtable and no scheduler job. The next call
// opens it again (a few ms: the WAL tail after the seal is empty). A
// collection with a watch consumer, or a call in flight, stays open.
//
// The work that only looks at a sleeping collection does not wake it:
// Stats answers from what it held when it slept, and a PITR snapshot is
// taken only if it was written since the newest one. Direct collections
// never sleep (their tailer follows the app files).

// IdleTick is how often the daemon looks for collections to close (tests
// lower it). The check runs at least twice per idle_close.
var IdleTick = time.Minute

// resting is what a sleeping collection tells without opening.
type resting struct {
	rows     int
	indexes  []collection.IndexInfo
	segments int
	lastTx   uint64
}

func idleCloseOf(cfg config.Config) time.Duration {
	d, err := config.ParseIdleClose(cfg.IdleClose)
	if err != nil {
		return 0
	}

	return d
}

// idleLoop closes the idle collections until the DB closes.
func (db *DB) idleLoop() {
	defer db.gcWait.Done()

	if db.idleClose <= 0 {
		return
	}

	tick := time.NewTicker(min(IdleTick, db.idleClose/2))
	defer tick.Stop()

	for {
		select {
		case <-db.ctx.Done():
			return
		case <-tick.C:
			db.SleepIdle(db.idleClose)
		}
	}
}

// IdleClose is the idle_close of the DB (0: collections never sleep).
func (db *DB) IdleClose() time.Duration {
	return db.idleClose
}

// SleepIdle closes the collections that no call used for idle, and returns
// how many it closed. When some closed, the memory they freed goes back to
// the OS now (a GC and a scavenge), not over the next minutes.
func (db *DB) SleepIdle(idle time.Duration) int {
	db.mu.RLock()

	cols := make([]*Collection, 0, len(db.cols))

	for _, col := range db.cols {
		cols = append(cols, col)
	}

	db.mu.RUnlock()

	slept := 0

	for _, col := range cols {
		ok, err := col.sleep(idle)
		if err != nil && db.ctx.Err() == nil {
			db.log.Error("closing an idle collection failed", "collection", col.name, "err", err)
		}

		if ok {
			slept++
		}
	}

	if slept > 0 {
		debug.FreeOSMemory()
	}

	return slept
}

// sleep seals and closes the collection if it is idle and nothing holds it
// open. A call in flight holds live for reading, so TryLock skips it.
func (c *Collection) sleep(idle time.Duration) (bool, error) {
	if !c.engine() || c.db == nil || time.Since(time.Unix(0, c.used.Load())) < idle {
		return false, nil
	}

	if !c.live.TryLock() {
		return false, nil
	}

	defer c.live.Unlock()

	if c.asleep || c.dropped || c.eng.Subscribers() > 0 {
		return false, nil
	}

	if err := c.eng.Seal(); err != nil {
		return false, fmt.Errorf("seal: %w", err)
	}

	rest := resting{rows: c.rd.Len(), indexes: c.rd.Indexes(), segments: c.eng.Segments()}

	if err := c.eng.Close(); err != nil {
		return false, err
	}

	rest.lastTx = c.eng.LastTx()

	c.rest, c.asleep = rest, true

	return true, nil
}

// wake opens a sleeping collection again, with the definition of now.
func (c *Collection) wake() error {
	c.live.Lock()
	defer c.live.Unlock()

	if !c.asleep || c.dropped {
		return nil
	}

	def, ok := c.db.cat.Get(c.name)
	if !ok {
		return fmt.Errorf("%w: %s", catalog.ErrUnknown, c.name)
	}

	fresh, err := c.db.openEngine(def, c.dir, c.wm)
	if err != nil {
		return fmt.Errorf("open %s again: %w", c.name, err)
	}

	c.eng, c.rd = fresh.eng, fresh.rd
	c.asleep = false
	c.used.Store(time.Now().UnixNano())

	return nil
}

// restingTx tells whether the collection sleeps, and its last tx then.
func (c *Collection) restingTx() (uint64, bool) {
	c.live.RLock()
	defer c.live.RUnlock()

	return c.rest.lastTx, c.asleep
}

// closeForSwap closes the collection for a truncate, a drop or a rename,
// under live, and gives its last tx. A sleeping one is closed already.
func (c *Collection) closeForSwap() (uint64, error) {
	if c.asleep {
		c.asleep = false

		return c.rest.lastTx, nil
	}

	// Close syncs and publishes every record first (relaxed mode too), so
	// LastTx after it is the real last tx.
	err := c.eng.Close()

	return c.eng.LastTx(), err
}
