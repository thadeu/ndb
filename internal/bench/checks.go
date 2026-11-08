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
	{metric: "queries.worst", what: "slowest query (median)", max: 700, unit: "ms"},
	// The has_many of 20 rows runs 20 sorted searches of ~180 ms (a sort
	// by ts decodes every matching doc; backlog: top-N from the column).
	{metric: "lookups.worst", what: "slowest lookup (median)", max: 4000, unit: "ms"},
	{metric: "aggregations.worst", what: "slowest aggregation (median)", max: 600, unit: "ms"},
	{metric: "wake.loaded_asleep", what: "the loaded partition stayed open after idle_close", max: 0},
	{metric: "wake.loaded_ms", what: "first call on the loaded partition after it slept", max: 100, unit: "ms"},
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
func verify(w io.Writer) int {
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

		if !c.pass(v) {
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
