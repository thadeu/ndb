package collection

import (
	"sync/atomic"
	"testing"
	"time"
)

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)

	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s", what)
		}

		time.Sleep(time.Millisecond)
	}
}

// A job runs at its deadline, again at the one it gives, and not at all
// once it gives none.
func TestJobRunsUntilItHasNothingToDo(t *testing.T) {
	var runs atomic.Int32

	j := newJob(func() time.Time {
		if runs.Add(1) < 3 {
			return time.Now().Add(time.Millisecond)
		}

		return time.Time{}
	})

	t.Cleanup(j.stop)

	j.poke(time.Now())

	eventually(t, "three runs", func() bool { return runs.Load() == 3 })
	time.Sleep(20 * time.Millisecond)

	if runs.Load() != 3 {
		t.Fatalf("runs = %d after it gave no deadline", runs.Load())
	}
}

// An earlier poke moves the deadline; a later one keeps it.
func TestPokeKeepsTheEarliestDeadline(t *testing.T) {
	ran := make(chan time.Time, 1)

	j := newJob(func() time.Time {
		ran <- time.Now()

		return time.Time{}
	})

	t.Cleanup(j.stop)

	start := time.Now()

	j.poke(start.Add(time.Hour))
	j.poke(start.Add(5 * time.Millisecond))
	j.poke(start.Add(time.Minute))

	select {
	case at := <-ran:
		if at.Sub(start) > time.Second {
			t.Fatalf("ran after %s", at.Sub(start))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the earlier poke did not move the deadline")
	}
}

// A poke while the job runs is kept for after the run.
func TestPokeWhileRunningRunsAgain(t *testing.T) {
	var runs atomic.Int32

	release := make(chan struct{})

	j := newJob(func() time.Time {
		if runs.Add(1) == 1 {
			<-release
		}

		return time.Time{}
	})

	t.Cleanup(j.stop)

	j.poke(time.Now())

	eventually(t, "the first run", func() bool { return runs.Load() == 1 })
	j.poke(time.Now())
	close(release)

	eventually(t, "the run asked while it ran", func() bool { return runs.Load() == 2 })
}

// stop waits for a run in progress and drops the deadline.
func TestStopWaitsAndDropsTheJob(t *testing.T) {
	var runs atomic.Int32

	started := make(chan struct{})

	j := newJob(func() time.Time {
		if runs.Add(1) == 1 {
			close(started)
			time.Sleep(20 * time.Millisecond)
		}

		return time.Now()
	})

	j.poke(time.Now())
	<-started
	j.stop()

	n := runs.Load()

	time.Sleep(20 * time.Millisecond)

	if runs.Load() != n {
		t.Fatal("a stopped job ran again")
	}

	j.poke(time.Now())
	time.Sleep(10 * time.Millisecond)

	if runs.Load() != n {
		t.Fatal("a poke ran a stopped job")
	}
}
