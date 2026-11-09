package bench

import (
	"fmt"
	"io"
	"sync"
)

// A gate is a budget of one measurement. After the phases, the bench
// compares each measurement it took with its budget and prints PASS or
// FAIL; a FAIL makes the run fail, so a change that makes ndb slower,
// bigger or less safe shows up in make bench. A part that did not run
// leaves its checks out.
//
// The budgets are loose on purpose (about 3x what the bench machine
// measures), so a slower machine passes and a regression does not. Raise
// one only with the reason in the commit.
type gate struct {
	metric string
	what   string
	max    float64
	min    float64
	unit   string

	// sized: a time budget for the default size of the load (-calls
	// 30000). A bigger load reads more, so the check shows the time but does
	// not judge it; memory, lost writes and errors are judged at any size.
	sized bool
}

var gates = []gate{
	{metric: "live.failed", what: "live writes that failed", max: 0},
	{metric: "live.lag", what: "live consumer lag at the end", max: 0, unit: "docs"},
	{metric: "live.p99", what: "live write p99", max: 15, unit: "ms"},
	{metric: "live.rss", what: "live RSS peak", max: 150, unit: "MB"},
	{metric: "crash.missing", what: "acked writes lost by a kill -9", max: 0},
	{metric: "crash.restart", what: "restart after a kill -9", max: 3000, unit: "ms"},
	{metric: "load.docs_s", what: "bulk load", min: 3_000, max: -1, unit: "docs/s"},
	{metric: "load.rss", what: "load RSS peak", max: 600, unit: "MB"},
	{metric: "queries.worst", what: "slowest query (median)", max: 150, unit: "ms", sized: true},
	{metric: "lookups.worst", what: "slowest lookup (median)", max: 250, unit: "ms", sized: true},
	{metric: "aggregations.worst", what: "slowest aggregation (median)", max: 200, unit: "ms", sized: true},
	{metric: "concurrent.1.errors", what: "reads that failed, 1 client", max: 0},
	{metric: "concurrent.3.errors", what: "reads that failed, 3 clients", max: 0},
	{metric: "concurrent.10.errors", what: "reads that failed, 10 clients", max: 0},
	{metric: "concurrent.3.p99", what: "read p99 with 3 clients", max: 500, unit: "ms", sized: true},
	{metric: "concurrent.10.rss", what: "RSS peak with 10 clients", max: 450, unit: "MB"},
	{metric: "direct.cold_missing", what: "lines of the direct files not read at start", max: 0},
	{metric: "direct.live_missing", what: "lines appended live that are not documents", max: 0},
	{metric: "wake.loaded_asleep", what: "the loaded partition stayed open after idle_close", max: 0},
	{metric: "wake.loaded_ms", what: "first call on the loaded partition after it slept", max: 50, unit: "ms", sized: true},
	{metric: "idle.asleep", what: "idle collections still open after idle_close", max: 0},
	{metric: "idle.wake_ms", what: "first call on a sleeping collection (median)", max: 50, unit: "ms"},
	{metric: "idle.missing", what: "acked writes lost by a kill -9 while collections wake", max: 0},
	{metric: "idle.disk_kb", what: "disk per idle collection", max: 128, unit: "KB"},
	{metric: "idle.reserve_kb", what: "disk reserved past the data, per idle collection", max: 16, unit: "KB"},
	{metric: "idle.cpu_pct", what: "CPU of the idle collections", max: 0.5, unit: "% core"},
	{metric: "idle.rss_kb", what: "RSS per idle collection", max: 128, unit: "KB"},
	{metric: "idle.fds", what: "open files per idle collection", max: 0.1},
}

var (
	measuredMu sync.Mutex
	measured   = map[string]float64{}
)

func record(metric string, v float64) {
	measuredMu.Lock()
	defer measuredMu.Unlock()

	measured[metric] = v
}

// verify prints the checks of the measurements taken and gives how many
// failed.
func verify(w io.Writer, defaultSize bool) int {
	failed := 0

	fmt.Fprintln(w)
	fmt.Fprintln(w, "| Check | Measured | Budget | Result |")
	fmt.Fprintln(w, "|---|---|---|---|")

	for _, c := range gates {
		v, ok := measured[c.metric]
		if !ok {
			continue
		}

		result := "PASS"

		switch {
		case c.sized && !defaultSize:
			result = "not judged (bigger load)"
		case !c.pass(v):
			result = "**FAIL**"
			failed++
		}

		fmt.Fprintf(w, "| %s | %s | %s | %s |\n", c.what, num(v, c.unit), c.budget(), result)
	}

	return failed
}

func (c gate) pass(v float64) bool {
	if c.max >= 0 && v > c.max {
		return false
	}

	return v >= c.min
}

func (c gate) budget() string {
	if c.max < 0 {
		return "≥ " + num(c.min, c.unit)
	}

	return "≤ " + num(c.max, c.unit)
}

func num(v float64, unit string) string {
	s := fmt.Sprintf("%.1f", v)

	if v == float64(int64(v)) {
		s = fmt.Sprintf("%d", int64(v))
	}

	if unit == "" {
		return s
	}

	return s + " " + unit
}
