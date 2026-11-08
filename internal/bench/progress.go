package bench

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// pulseEvery is how often a long step logs where it is.
const pulseEvery = 5 * time.Second

// progress logs the run as it goes, to its own writer (stderr), apart from
// the report: a line when a step starts, one every pulseEvery while it runs,
// and one when it ends, each with the time since the start and the ETA of
// the run. The ETA adds the estimate of each step left (the current one
// minus its time so far, never below a second).
type progress struct {
	w     io.Writer
	start time.Time
	plan  []planned

	mu     sync.Mutex
	cur    int
	began  time.Time
	detail func() string
	stop   chan struct{}
	done   chan struct{}
}

type planned struct {
	name string
	est  time.Duration
}

var prog = newProgress(io.Discard, nil)

func newProgress(w io.Writer, plan []planned) *progress {
	return &progress{w: w, start: time.Now(), plan: plan, cur: -1}
}

// step starts the planned step name; detail, when set, tells the pulses
// where it is. It ends the step before it.
func (p *progress) step(name string, detail func() string) {
	p.end("")

	p.mu.Lock()
	defer p.mu.Unlock()

	p.cur = p.index(name)
	p.began = time.Now()
	p.detail = detail
	p.stop, p.done = make(chan struct{}), make(chan struct{})

	p.logf("▶ %s", name)

	go p.pulse(p.stop, p.done)
}

// end logs the current step as done, with what it found. The time it took
// is its estimate from then on.
func (p *progress) end(result string) {
	p.mu.Lock()
	stop, done := p.stop, p.done
	p.stop = nil
	p.mu.Unlock()

	if stop == nil {
		return
	}

	close(stop)
	<-done

	p.mu.Lock()
	defer p.mu.Unlock()

	took := time.Since(p.began)
	name := p.plan[p.cur].name

	if result != "" {
		result = ": " + result
	}

	p.plan[p.cur].est = took
	p.cur++
	p.logf("✓ %s in %s%s", name, round(took), result)
}

func (p *progress) pulse(stop, done chan struct{}) {
	defer close(done)

	t := time.NewTicker(pulseEvery)
	defer t.Stop()

	for {
		select {
		case <-stop:
			return
		case <-t.C:
			p.mu.Lock()

			line := fmt.Sprintf("… %s %s", p.plan[p.cur].name, round(time.Since(p.began)))

			if p.detail != nil {
				line += ", " + p.detail()
			}

			p.logf("%s", line)
			p.mu.Unlock()
		}
	}
}

// logf writes one line: [elapsed] text · ETA. Callers hold p.mu.
func (p *progress) logf(format string, args ...any) {
	fmt.Fprintf(p.w, "[%s] %s · ETA %s\n", clockOf(time.Since(p.start)), fmt.Sprintf(format, args...), round(p.eta()))
}

// eta is the estimate of what is left. Callers hold p.mu.
func (p *progress) eta() time.Duration {
	var left time.Duration

	for i := max(p.cur, 0); i < len(p.plan); i++ {
		est := p.plan[i].est

		if i == p.cur && p.stop != nil {
			est = max(est-time.Since(p.began), time.Second)
		}

		left += est
	}

	return left
}

func (p *progress) index(name string) int {
	for i, s := range p.plan {
		if s.name == name {
			return i
		}
	}

	p.plan = append(p.plan, planned{name: name})

	return len(p.plan) - 1
}

func round(d time.Duration) time.Duration {
	if d < time.Second {
		return d.Round(10 * time.Millisecond)
	}

	if d < 10*time.Second {
		return d.Round(100 * time.Millisecond)
	}

	return d.Round(time.Second)
}

func clockOf(d time.Duration) string {
	s := int(d.Seconds())

	return fmt.Sprintf("%02d:%02d", s/60, s%60)
}
