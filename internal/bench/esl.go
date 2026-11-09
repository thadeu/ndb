// Package bench is the ESL outbox benchmark (ndb bench): an app that talks
// to FreeSWITCH
// writes 7 docs per call (6 events and a finishcall) and workers consume them.
// It starts its own daemon in a temp dir and runs four parts in about two and a half
// minutes:
//
//  1. stress load: concurrent calls write one doc at a time (strict) while
//     a _watch consumer acks them; a kill -9 while writing, then every acked
//     write read back; a bulk load of calls into a daily partition;
//  2. queries: by id, by indexed fields, sorted pages, counts;
//  3. aggregations: the dashboards of the ESL;
//  4. idle collections: hundreds of small collections at rest, on a daemon
//     of their own (disk, CPU, RSS and files per collection).
//
// Then each measurement is checked against its budget (checks.go): the run
// prints PASS or FAIL per check and fails when one misses.
//
// Options pick the parts (none is all of them). The queries and the
// aggregations load the calls first without measuring it. The daemon RSS is
// sampled all the time.
package bench

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Options of a run. Bin is the ndb binary the daemon runs from. Stress,
// Query and Aggs pick the parts to measure; none of them is all.
type Options struct {
	Bin   string
	Live  time.Duration
	Rate  int
	Calls int
	Dir   string
	Keep  bool

	Stress bool
	Query  bool
	Aggs   bool
	Lookup bool
	Idle   bool

	// Concurrent: clients that read at the same time (1, 3 and 10).
	Concurrent bool

	// Direct: users and cdrs in NDJSON files that ndb tails (write:
	// direct), read at start and appended live, then read. DirectRows is
	// the number of cdrs (users are a tenth).
	Direct     bool
	DirectRows int

	// Partition is the partition_by of the loaded calls: day (default) puts
	// them all in yesterday; month spreads them from five months ago to now,
	// in six monthly partitions.
	Partition string

	// Data keeps the loaded calls in this dir for the next runs (a
	// dataset): the first run loads them, the next ones read them. Only the
	// read parts go with it.
	Data string

	// Collections is how many collections the idle part creates.
	Collections int

	// PITR is the pitr of the daemon (off, 1d); empty keeps its default.
	PITR string

	// YML are lines added to the ndb.yml of the daemon (memory_limit:
	// 512MiB), to compare settings.
	YML []string

	// Log gets a line per step as the run goes, with the ETA (stderr in
	// ndb bench); nil logs nothing.
	Log io.Writer
}

func (o Options) all() bool {
	return !o.Stress && !o.Query && !o.Aggs && !o.Lookup && !o.Idle && !o.Concurrent && !o.Direct
}

// reads tells whether a part needs the loaded calls.
func (o Options) reads() bool {
	return o.Stress || o.Query || o.Aggs || o.Lookup || o.Concurrent || o.all()
}

func settings(yml []string) string {
	if len(yml) == 0 {
		return ""
	}

	return ", " + strings.Join(yml, ", ")
}

// plan is the steps of the run with a first estimate of each, for the ETA.
func (o Options) plan(reuse bool) []planned {
	sec := func(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }
	steps := []planned{{"daemon start", sec(0.5)}}

	if o.Stress || o.all() {
		steps = append(steps, planned{"live writes", o.Live}, planned{"crash", sec(17)})
	}

	if o.reads() && !reuse {
		steps = append(steps, planned{"bulk load", sec(loadSeconds(o.Calls))}, planned{"settle", sec(12)})
	}

	if o.Query || o.all() {
		steps = append(steps, planned{"queries", sec(4)})
	}

	if o.Aggs || o.all() {
		steps = append(steps, planned{"aggregations", sec(4)})
	}

	if o.Lookup || o.all() {
		steps = append(steps, planned{"lookups", sec(3)})
	}

	if o.Concurrent || o.all() {
		for _, n := range clientLevels {
			steps = append(steps, planned{clientsStep(n), concurrentFor})
		}
	}

	if o.all() || o.Data != "" {
		steps = append(steps, planned{"wake: loaded partition", loadedIdle + 3*time.Second})
	}

	if o.Direct {
		steps = append(steps, directPlan(o.DirectRows)...)
	}

	if o.Idle || o.all() {
		steps = append(steps, planned{"idle: create", sec(float64(o.Collections)/40 + 2)}, planned{"idle: sleep", sec(4)},
			planned{"idle: at rest", sec(10)}, planned{"idle: wake", sec(0.2)}, planned{"idle: crash", sec(5)})
	}

	return steps
}

// Defaults run in about two and a half minutes.
func Defaults() Options {
	return Options{Live: 30 * time.Second, Rate: 400, Calls: 30_000, Collections: 300, DirectRows: 500_000}
}

var (
	bin     *string
	liveFor *time.Duration
	rate    *int
	calls   *int
	out     io.Writer = os.Stdout
)

// failure is a fatal error of a run (check panics with it).
type failure struct{ err error }

// ErrFailed is the error of a run whose measurements miss a budget.
var ErrFailed = errors.New("bench: checks failed")

// Run starts a daemon from o.Bin in a temp dir (under o.Dir), runs the
// phases and writes their table to w.
func Run(o Options, w io.Writer) (err error) {
	bin, liveFor, rate, calls, out = &o.Bin, &o.Live, &o.Rate, &o.Calls, w

	defer func() {
		if r := recover(); r != nil {
			f, ok := r.(failure)
			if !ok {
				panic(r)
			}

			err = f.err
		}
	}()

	dir, err := os.MkdirTemp(o.Dir, "ndb-bench-")
	check(err)

	if o.Keep {
		fmt.Fprintln(out, "data dir:", dir)
	} else {
		defer os.RemoveAll(dir)
	}

	ds := openDataset(o.Data, o.Calls, cmp.Or(o.Partition, "day"))
	lay = ds.layout()

	mainDir := dir

	if o.Data != "" {
		mainDir = o.Data
		check(os.MkdirAll(mainDir, 0o755))
	}

	conf := "idle_close: " + loadedIdle.String() + "\n"

	switch {
	case o.PITR != "":
		conf += "pitr: " + o.PITR + "\n"
	case o.Data != "":
		conf += "pitr: off\n"
	}

	for _, line := range o.YML {
		conf += line + "\n"
	}

	check(os.WriteFile(filepath.Join(mainDir, "ndb.yml"), []byte(conf), 0o644))

	prog = newProgress(cmp.Or[io.Writer](o.Log, io.Discard), o.plan(ds.found))

	defer prog.end("")

	prog.step("daemon start", nil)

	d := &daemon{dir: mainDir}
	d.start()

	prog.end("")

	defer d.stop()

	go d.sample()

	measured = map[string]float64{}

	var report []string

	if o.Stress || o.all() {
		report = append(report, "| **stress load** | |")
		report = append(report, live(d)...)
		report = append(report, crash(d)...)
	}

	if o.reads() {
		loaded := ds.load(d)

		if o.Stress || o.all() {
			report = append(report, loaded...)
		} else {
			report = append(report, loaded[0])
			delete(measured, "load.docs_s")
			delete(measured, "load.rss")
		}
	}

	if o.Query || o.all() {
		report = append(report, "| **queries** (median of 5, min–max) | |")
		report = append(report, timed(d, "queries", queries(d))...)
	}

	if o.Aggs || o.all() {
		report = append(report, "| **aggregations** (median of 5, min–max) | |")
		report = append(report, timed(d, "aggregations", aggregations())...)
	}

	var joins []probe

	if o.Lookup || o.Concurrent || o.all() {
		joins = lookups(d)
	}

	if o.Lookup || o.all() {
		report = append(report, "| **lookups** (median of 5, min–max) | |")
		report = append(report, timed(d, "lookups", joins)...)
	}

	if o.Concurrent || o.all() {
		mix := slices.Concat(queries(d), aggregations(), joins)

		report = append(report, "| **concurrent reads** (the reads above, in a random order) | |")
		report = append(report, concurrent(d, mix)...)
	}

	if o.all() || o.Data != "" {
		report = append(report, wakeLoaded(d)...)
	}

	if o.Direct {
		report = append(report, "| **direct mode** (NDJSON files the app writes) | |")
		report = append(report, direct(dir, o.DirectRows)...)
	}

	if o.Idle || o.all() {
		report = append(report, "| **idle collections** | |")
		report = append(report, idle(dir, o.Collections)...)
	}

	fmt.Fprintln(out)
	fmt.Fprintf(out, "ndb bench, ESL outbox, %s/%s, %d CPU, pitr %s%s\n\n", runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), cmp.Or(o.PITR, "default"), settings(o.YML))
	fmt.Fprintln(out, "| Phase | Result |")
	fmt.Fprintln(out, "|---|---|")

	for _, line := range report {
		fmt.Fprintln(out, line)
	}

	fmt.Fprintf(out, "| disk | %s |\n", du(mainDir))

	if verify(out, o.Calls == Defaults().Calls) > 0 {
		return ErrFailed
	}

	return nil
}

// daemon is the ndb process under test, with its RSS peak per phase.
type daemon struct {
	dir string

	// ready is how long start waits for /_health (default 5 s): a daemon
	// that reads big direct files answers only when it has read them.
	ready time.Duration

	cmd  *exec.Cmd
	http *http.Client

	mu   sync.Mutex
	peak int64
}

// sock is the socket of the daemon: in its dir, or in the system temp dir
// when that path is past the unix socket limit (~104 bytes on macOS).
func (d *daemon) sock() string {
	path := filepath.Join(d.dir, "ndb.sock")

	if len(path) < 100 {
		return path
	}

	return filepath.Join(os.TempDir(), fmt.Sprintf("ndb-bench-%x.sock", crc32.ChecksumIEEE([]byte(path))))
}

func (d *daemon) start() {
	if conn, err := net.Dial("unix", d.sock()); err == nil {
		conn.Close()
		check(fmt.Errorf("a daemon already listens on %s: stop it first", d.sock()))
	}

	os.Remove(d.sock())

	d.cmd = exec.Command(*bin, "-config", filepath.Join(d.dir, "ndb.yml"), "-data-dir", filepath.Join(d.dir, "data"), "-unix", d.sock())
	d.cmd.Stderr = io.Discard
	check(d.cmd.Start())

	d.http = &http.Client{Transport: &http.Transport{
		DialContext:         func(context.Context, string, string) (net.Conn, error) { return net.Dial("unix", d.sock()) },
		MaxIdleConnsPerHost: 512,
	}}

	wait := cmp.Or(d.ready, 5*time.Second)

	for start := time.Now(); time.Since(start) < wait; time.Sleep(25 * time.Millisecond) {
		if code, _, _ := d.call("GET", "/_health", nil); code == 200 {
			return
		}
	}

	// A daemon that did not answer in time is not left running.
	d.kill()
	check(errors.New("the daemon did not start"))
}

func (d *daemon) kill() {
	d.cmd.Process.Kill()
	d.cmd.Wait()
}

func (d *daemon) stop() {
	if d.cmd != nil && d.cmd.ProcessState == nil {
		d.kill()
	}
}

func (d *daemon) sample() {
	for range time.Tick(250 * time.Millisecond) {
		mb := d.rss()

		d.mu.Lock()
		d.peak = max(d.peak, mb)
		d.mu.Unlock()
	}
}

// phase resets the RSS peak and returns a func that reads it.
func (d *daemon) phase() func() int64 {
	d.mu.Lock()
	d.peak = d.rss()
	d.mu.Unlock()

	return func() int64 {
		d.mu.Lock()
		defer d.mu.Unlock()

		return d.peak
	}
}

func (d *daemon) call(method, path string, body []byte) (int, []byte, time.Duration) {
	req, _ := http.NewRequest(method, "http://ndb"+path, bytes.NewReader(body))
	req.Header.Set("Accept", "application/x-ndjson")

	start := time.Now()

	resp, err := d.http.Do(req)
	if err != nil {
		return 0, []byte(err.Error()), time.Since(start)
	}

	defer resp.Body.Close()

	b, _ := io.ReadAll(resp.Body)

	return resp.StatusCode, b, time.Since(start)
}

func (d *daemon) must(method, path, body string) []byte {
	code, out, _ := d.call(method, path, []byte(body))
	if code == 0 || code >= 300 {
		check(fmt.Errorf("%s %s: %d %s", method, path, code, out))
	}

	return out
}

// live: concurrent calls write one doc at a time, a watcher acks.
func live(d *daemon) []string {
	d.must("PUT", "/outbox", `{"time":"ts","indexes":["status","call_id"],"durability":"strict"}`)

	peak := d.phase()
	w := write(d, *liveFor, nil)

	prog.step("live writes", func() string {
		return fmt.Sprintf("%d docs, %d failed, lag %d", w.written.Load(), w.failed.Load(), w.written.Load()-w.consumed.Load())
	})

	stats := w.wait()
	rss := peak()

	prog.end(fmt.Sprintf("%d docs, %d failed, p99 %.1f ms", stats.written, stats.failed, stats.p(.99)))

	record("live.failed", float64(stats.failed))
	record("live.lag", float64(stats.written-stats.consumed))
	record("live.p99", stats.p(.99))
	record("live.rss", float64(rss))

	return []string{
		fmt.Sprintf("| live writes | %d docs, %.0f/s, %d failed |", stats.written, stats.perSec, stats.failed),
		fmt.Sprintf("| live latency | p50 %.1f ms, p99 %.1f ms, p99.9 %.1f ms, max %.1f ms |", stats.p(.5), stats.p(.99), stats.p(.999), stats.p(1)),
		fmt.Sprintf("| live consumer | lag %d |", stats.written-stats.consumed),
		fmt.Sprintf("| live RSS | peak %d MB |", rss),
	}
}

// crash: kill -9 while writing, restart, every acked write must be there.
func crash(d *daemon) []string {
	var (
		acked  []string
		killed atomic.Bool
	)

	w := write(d, 15*time.Second, &acked)

	prog.step("crash", func() string {
		state := "writing"

		if killed.Load() {
			state = "killed and restarted, writing"
		}

		return fmt.Sprintf("%s, %d docs", state, w.written.Load())
	})

	time.Sleep(7 * time.Second)
	d.kill()
	killed.Store(true)

	start := time.Now()
	d.start()
	restart := time.Since(start)

	w.wait()

	missing := 0

	for _, id := range acked {
		if code, _, _ := d.call("GET", "/outbox/_doc/"+id, nil); code != 200 {
			missing++
		}
	}

	prog.end(fmt.Sprintf("%d acked, %d missing, restart %d ms", len(acked), missing, restart.Milliseconds()))
	record("crash.missing", float64(missing))
	record("crash.restart", float64(restart.Milliseconds()))

	return []string{
		fmt.Sprintf("| crash | %d acked writes, %d missing, restart %d ms |", len(acked), missing, restart.Milliseconds()),
	}
}

// load: bulk load of calls into a daily partition.
func load(d *daemon, retention string) []string {
	var loaded atomic.Int64

	prog.step("bulk load", func() string { return fmt.Sprintf("%d of %d docs", loaded.Load(), *calls*7) })
	d.must("PUT", "/events", `{"time":"ts","indexes":["call_id","customer","type"],"partition_by":"`+lay.by+`","retention":"`+retention+`","max_line_size":16777216}`)

	peak := d.phase()
	start := time.Now()

	const writers = 4

	var wg sync.WaitGroup

	for w := range writers {
		wg.Add(1)

		go func() {
			defer wg.Done()

			r := rand.New(rand.NewPCG(uint64(w), 7))
			batch := make([]map[string]any, 0, 1400)

			flush := func() {
				body, _ := json.Marshal(map[string]any{"docs": batch})
				d.must("POST", "/events/_bulk", string(body))
				loaded.Add(int64(len(batch)))
				batch = batch[:0]
			}

			for n := w; n < *calls; n += writers {
				at := lay.start.Add(time.Duration(float64(n) / float64(*calls) * float64(lay.span)))
				batch = append(batch, callDocs(r, at, n)...)

				if len(batch) >= 1400 {
					flush()
				}
			}

			if len(batch) > 0 {
				flush()
			}
		}()
	}

	wg.Wait()

	el := time.Since(start).Seconds()

	prog.end(fmt.Sprintf("%.0f docs/s", float64(*calls*7)/el))
	prog.step("settle", nil)

	last := lay.part(lay.start.Add(lay.span - time.Nanosecond))
	settle := d.settle(last)

	sealedMB, sealing := d.sealEvents()

	if sealedMB > 0 {
		d.settle(last)
	}

	rss := peak()

	prog.end(fmt.Sprintf("seals and compactions done, %.1f MB of memtable sealed", sealedMB))

	record("load.docs_s", float64(*calls*7)/el)
	record("load.rss", float64(rss))

	return []string{
		fmt.Sprintf("| load | %d calls, %d docs in %.0f s, %.0f docs/s |", *calls, *calls*7, el, float64(*calls*7)/el),
		fmt.Sprintf("| load settle | seals and compactions done %.0f s after the load; %.1f MB of memtable sealed with _compact in %.1f s |", settle.Seconds(), sealedMB, sealing.Seconds()),
		fmt.Sprintf("| load RSS | peak %d MB, after %d MB |", rss, d.rss()),
	}
}

// memtableLine is one ndb_memtable_bytes series of /_metrics.
var memtableLine = regexp.MustCompile(`^ndb_memtable_bytes\{collection="(events[^"]*)"\} (\d+)$`)

// sealEvents seals the documents that the load left in the memtables of the
// events partitions, with _compact (a seal, then a merge). A memtable is
// sealed at write_buffer or after 10 minutes, so after the load some runs
// kept about 19k documents unsealed and others none: a count then decoded
// them (11 ms against 0.4 ms), and two runs of one binary did not compare.
// It gives the MB sealed and the time it took.
func (d *daemon) sealEvents() (float64, time.Duration) {
	start := time.Now()

	var total int64

	for line := range strings.Lines(string(d.must("GET", "/_metrics", ""))) {
		m := memtableLine.FindStringSubmatch(strings.TrimSpace(line))

		if m == nil || m[2] == "0" {
			continue
		}

		n, _ := strconv.ParseInt(m[2], 10, 64)
		total += n

		d.must("POST", "/"+m[1]+"/_compact", "")
	}

	return float64(total) / (1 << 20), time.Since(start)
}

// settle waits until the segments of coll stop changing (no seal or
// compaction for 3 s), so the reads do not share the CPU with them.
func (d *daemon) settle(coll string) time.Duration {
	start := time.Now()
	last, same := "", 0

	for time.Since(start) < time.Minute && same < 6 {
		var stats struct {
			Segments int   `json:"segments"`
			Bytes    any   `json:"bytes"`
			Rows     int64 `json:"rows"`
		}

		json.Unmarshal(d.must("GET", coll+"/_stats", ""), &stats)

		cur := fmt.Sprint(stats.Segments, stats.Bytes)

		if cur == last {
			same++
		} else {
			last, same = cur, 0
		}

		time.Sleep(500 * time.Millisecond)
	}

	return time.Since(start) - 3*time.Second
}

// probe is one read of the queries or aggregations parts.
type probe struct{ name, method, path, body string }

// layout is how the load spreads the calls in time and in partitions.
type layout struct {
	by    string        // partition_by: day or month
	start time.Time     // the first call
	span  time.Duration // the calls spread over [start, start+span)
	day   time.Time     // the day the probes of one hour read
}

// lay is the layout of the run.
var lay = dayLayout(yesterday())

func yesterday() time.Time {
	return time.Now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)
}

// dayLayout puts every call in one day, one partition.
func dayLayout(day time.Time) layout {
	return layout{by: "day", start: day, span: 24 * time.Hour, day: day}
}

// monthLayout spreads the calls from the first day of five months before
// end to end: six monthly partitions, the last one partial. The probes of
// one hour read the 11th of the month before end's.
func monthLayout(start, end time.Time) layout {
	return layout{by: "month", start: start, span: end.Sub(start), day: monthOf(end).AddDate(0, -1, 10)}
}

func monthOf(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// part is the path of the partition that holds t.
func (l layout) part(t time.Time) string {
	if l.by == "month" {
		return "/events-" + t.UTC().Format("2006-01")
	}

	return "/events-" + t.UTC().Format(time.DateOnly)
}

// current is the start of the last month of a month layout: "up to the
// previous month" is before it.
func (l layout) current() time.Time {
	return monthOf(l.start.Add(l.span))
}

// past is the path of the list of the months of a month layout before the
// current one: /events-2026-06,events-2026-07,...
func (l layout) past() string {
	var names []string

	for m := monthOf(l.start); m.Before(l.current()); m = m.AddDate(0, 1, 0) {
		names = append(names, strings.TrimPrefix(l.part(m), "/"))
	}

	return "/" + strings.Join(names, ",")
}

func day() time.Time { return lay.day }

// loadSeconds is the estimate of a load of calls: it slows down as the
// partition grows (10k docs/s at 30k calls, 4.4k at 286k).
func loadSeconds(calls int) float64 {
	docs := float64(calls * 7)

	return docs / (10_000 / (1 + docs/1_600_000))
}

// queries are the reads of the ESL: lookups, pages and counts.
func queries(d *daemon) []probe {
	part := lay.part(day())

	var row struct {
		ID       string `json:"_id"`
		CallID   string `json:"call_id"`
		Customer string `json:"customer"`
	}

	check(json.Unmarshal(bytes.TrimSpace(d.must("POST", part+"/_search", `{"limit":1}`)), &row))

	hour := fmt.Sprintf(`{"$gte":%q,"$lt":%q}`, day().Add(10*time.Hour).Format(time.RFC3339), day().Add(11*time.Hour).Format(time.RFC3339))

	return append([]probe{
		{"get by id", "GET", part + "/_doc/" + row.ID, ""},
		{"get by id, missing", "GET", part + "/_doc/none", ""},
		{"events of a call (indexed call_id)", "POST", "/events/_search", fmt.Sprintf(`{"query":{"call_id":%q},"sort":{"seq":1}}`, row.CallID)},
		{"last 20 of a customer", "POST", "/events/_search", `{"query":{"customer":"cust-042"},"sort":{"ts":-1},"limit":20}`},
		{"page 3 of a customer's finishcalls", "POST", "/events/_search", `{"query":{"customer":"cust-042","type":"finishcall"},"sort":{"ts":-1},"limit":20,"skip":40}`},
		{"a customer in one hour", "POST", "/events/_search", fmt.Sprintf(`{"query":{"customer":"cust-042","ts":%s},"limit":100}`, hour)},
		{"long calls, not indexed", "POST", "/events/_search", `{"query":{"type":"finishcall","duration":{"$gte":590}},"limit":50}`},
		{"count of a customer", "POST", "/events/_count", `{"query":{"customer":"cust-042"}}`},
		{"count of the finishcalls", "POST", "/events/_count", `{"query":{"type":"finishcall"}}`},
		{"count of one hour", "POST", "/events/_count", fmt.Sprintf(`{"query":{"ts":%s}}`, hour)},
		{"count of all", "POST", "/events/_count", `{}`},
	}, monthQueries()...)
}

// monthQueries are the reads of a month layout over a time window: up to
// the previous month (by the collection and by the events-* alias), and one
// month.
func monthQueries() []probe {
	if lay.by != "month" {
		return nil
	}

	cur := lay.current()
	before := fmt.Sprintf(`{"$lt":%q}`, cur.Format(time.RFC3339))
	month := fmt.Sprintf(`{"$gte":%q,"$lt":%q}`, cur.AddDate(0, -1, 0).Format(time.RFC3339), cur.Format(time.RFC3339))

	return []probe{
		{"count up to the previous month", "POST", "/events/_count", `{"query":{"ts":` + before + `}}`},
		{"count up to the previous month (events-*)", "POST", "/events-*/_count", `{"query":{"ts":` + before + `}}`},
		{"count up to the previous month (list of 5 months)", "POST", lay.past() + "/_count", `{"query":{"ts":` + before + `}}`},
		{"last 20 of a customer, up to the previous month", "POST", "/events/_search", `{"query":{"customer":"cust-042","ts":` + before + `},"sort":{"ts":-1},"limit":20}`},
		{"count of the previous month", "POST", "/events/_count", `{"query":{"ts":` + month + `}}`},
	}
}

// loadedIdle is the idle_close of the main daemon: short, so the loaded
// partition sleeps after the reads and its wake can be timed.
const loadedIdle = 20 * time.Second

// wakeLoaded waits for the partition of the load to sleep (no call for
// loadedIdle), then times the first call on it: the cost of opening a big
// collection again.
func wakeLoaded(d *daemon) []string {
	part := lay.part(day())

	var row struct {
		ID string `json:"_id"`
	}

	check(json.Unmarshal(bytes.TrimSpace(d.must("POST", part+"/_search", `{"limit":1}`)), &row))

	prog.step("wake: loaded partition", nil)

	var stats struct {
		Asleep bool  `json:"asleep"`
		Rows   int   `json:"rows"`
		Total  int64 `json:"total"`
	}

	for deadline := time.Now().Add(loadedIdle + 30*time.Second); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		check(json.Unmarshal(d.must("GET", part+"/_stats", ""), &stats))

		if stats.Asleep {
			break
		}
	}

	if !stats.Asleep {
		prog.end("it did not sleep")
		record("wake.loaded_asleep", 1)

		return []string{"| wake | the loaded partition did not sleep |"}
	}

	code, body, took := d.call("GET", part+"/_doc/"+row.ID, nil)
	if code != 200 {
		check(fmt.Errorf("wake: %d %s", code, body))
	}

	prog.end(fmt.Sprintf("%.1f ms for %d rows, %.0f MB", ms(took), stats.Rows, float64(stats.Total)/(1<<20)))
	record("wake.loaded_asleep", 0)
	record("wake.loaded_ms", ms(took))

	return []string{
		fmt.Sprintf("| wake | first call on the loaded partition after it slept (%d rows, %.0f MB): %.1f ms |", stats.Rows, float64(stats.Total)/(1<<20), ms(took)),
	}
}

// lookups are the reads that bring the customer of a call, or the last
// calls of a customer, in the same answer (M18). They load 400 customers
// first, not measured.
func lookups(d *daemon) []probe {
	d.must("PUT", "/customers", `{"indexes":["plan"]}`)

	var have struct {
		Count int `json:"count"`
	}

	check(json.Unmarshal(d.must("POST", "/customers/_count", "{}"), &have))

	if have.Count < 400 {
		addCustomers(d)
	}

	finishcalls := `"query":{"customer":"cust-042","type":"finishcall"},"sort":{"ts":-1},"limit":20`
	belongs := `"customer":{"belongs_to":"customers","fk":"customer"}`

	return lookupProbes(finishcalls, belongs)
}

// addCustomers writes the 400 customers that the calls point to.
func addCustomers(d *daemon) {
	var b strings.Builder

	b.WriteString(`{"docs":[`)

	for i := range 400 {
		if i > 0 {
			b.WriteByte(',')
		}

		fmt.Fprintf(&b, `{"_id":"cust-%03d","name":"Customer %d","plan":%q}`, i, i, []string{"free", "pro", "team"}[i%3])
	}

	b.WriteString(`]}`)
	d.must("POST", "/customers/_bulk", b.String())
}

func lookupProbes(finishcalls, belongs string) []probe {
	return []probe{
		{"20 finishcalls with their customer (belongs_to)", "POST", "/events/_search", `{` + finishcalls + `,"lookup":{` + belongs + `}}`},
		{"100 finishcalls of any customer, belongs_to", "POST", "/events/_search", `{"query":{"type":"finishcall"},"limit":100,"lookup":{` + belongs + `}}`},
		{"20 customers with their last 5 events (has_many)", "POST", "/customers/_search", `{"query":{"plan":"pro"},"limit":20,"lookup":{"events":{"has_many":"events","fk":"customer","sort":{"ts":-1},"limit":5}}}`},
		{"a customer, its last finishcall (has_one)", "POST", "/customers/_search", `{"query":{"_id":"cust-042"},"lookup":{"last":{"has_one":"events","fk":"customer","query":{"type":"finishcall"},"sort":{"ts":-1}}}}`},
		{"customers, last 3 events, each with its customer (2 levels)", "POST", "/customers/_search", `{"query":{"plan":"team"},"limit":10,"lookup":{"events":{"has_many":"events","fk":"customer","sort":{"ts":-1},"limit":3,"lookup":{` + belongs + `}}}}`},
		{"top 5 customers by calls, with the customer ($lookup)", "POST", "/events/_aggregate", `{"pipeline":[{"$match":{"type":"finishcall"}},{"$group":{"_id":"$customer","n":{"$count":{}}}},{"$sort":{"n":-1}},{"$limit":5},{"$lookup":{"from":"customers","localField":"_id","foreignField":"_id","as":"customer"}}]}`},
	}
}

// aggregations are the dashboards of the ESL.
func aggregations() []probe {
	agg := func(name, pipeline string) probe {
		return probe{name, "POST", "/events/_aggregate", `{"pipeline":` + pipeline + `}`}
	}

	probes := []probe{
		agg("calls per customer, top 5", `[{"$match":{"type":"finishcall"}},{"$group":{"_id":"$customer","n":{"$count":{}}}},{"$sort":{"n":-1}},{"$limit":5}]`),
		agg("events per type", `[{"$group":{"_id":"$type","n":{"$count":{}}}}]`),
		agg("avg duration per hour", `[{"$match":{"type":"finishcall"}},{"$group":{"_id":{"$dateTrunc":{"date":"$ts","unit":"hour"}},"avg":{"$avg":"$duration"}}}]`),
		agg("talk time per customer, top 10", `[{"$match":{"type":"finishcall"}},{"$group":{"_id":"$customer","total":{"$sum":"$duration"},"max":{"$max":"$duration"}}},{"$sort":{"total":-1}},{"$limit":10}]`),
		agg("hangup causes", `[{"$match":{"type":"finishcall"}},{"$group":{"_id":"$hangup_cause","n":{"$count":{}}}}]`),
		agg("one customer per hour", `[{"$match":{"customer":"cust-042"}},{"$group":{"_id":{"$dateTrunc":{"date":"$ts","unit":"hour"}},"n":{"$count":{}}}}]`),
		agg("distinct callers per customer, top 10 ($distinct)", `[{"$match":{"type":"finishcall"}},{"$group":{"_id":"$customer","callers":{"$distinct":"$caller"}}},{"$sort":{"callers":-1}},{"$limit":10}]`),
		agg("distinct callers of all calls ($distinct)", `[{"$match":{"type":"finishcall"}},{"$group":{"_id":null,"callers":{"$distinct":"$caller"}}}]`),
		agg("hangup causes of 5 customers ($addToSet)", `[{"$match":{"type":"finishcall"}},{"$group":{"_id":"$customer","causes":{"$addToSet":"$hangup_cause"}}},{"$sort":{"_id":1}},{"$limit":5}]`),
		agg("customers with more calls than the mean ($having)", fmt.Sprintf(`[{"$match":{"type":"finishcall"}},{"$group":{"_id":"$customer","n":{"$count":{}}}},{"$having":{"n":{"$gt":%d}}},{"$sort":{"n":-1}}]`, *calls/400)),
	}

	if lay.by != "month" {
		return probes
	}

	before := fmt.Sprintf(`[{"$match":{"ts":{"$lt":%q}}},{"$group":{"_id":"$type","n":{"$count":{}}}}]`, lay.current().Format(time.RFC3339))

	return append(probes,
		agg("events per type, up to the previous month", before),
		probe{"events per type, up to the previous month (events-*)", "POST", "/events-*/_aggregate", `{"pipeline":` + before + `}`},
		probe{"events per type, up to the previous month (list of 5 months)", "POST", lay.past() + "/_aggregate", `{"pipeline":` + before + `}`},
	)
}

// timed runs each probe 5 times and gives one line per probe: the median,
// the min and the max; then the RSS peak of the part.
func timed(d *daemon, part string, probes []probe) []string {
	var done atomic.Int32

	prog.step(part, func() string { return fmt.Sprintf("%d of %d", done.Load(), len(probes)) })

	peak := d.phase()

	var (
		lines []string
		worst time.Duration
	)

	for _, p := range probes {
		var times []time.Duration

		for range 5 {
			code, body, took := d.call(p.method, p.path, []byte(p.body))
			if code >= 300 && code != http.StatusNotFound {
				check(fmt.Errorf("%s: %d %s", p.name, code, body))
			}

			times = append(times, took)
		}

		slices.Sort(times)
		done.Add(1)
		worst = max(worst, times[2])
		lines = append(lines, fmt.Sprintf("| %s | %.1f ms (%.1f–%.1f) |", p.name, ms(times[2]), ms(times[0]), ms(times[4])))
	}

	prog.end(fmt.Sprintf("slowest %.1f ms", ms(worst)))
	record(part+".worst", ms(worst))

	return append(lines, fmt.Sprintf("| %s RSS | peak %d MB |", part, peak()))
}

// writer is the live writers and the consumer of one phase.
type writer struct {
	wg       sync.WaitGroup
	start    time.Time
	stop     chan struct{}
	written  atomic.Int64
	failed   atomic.Int64
	consumed atomic.Int64

	mu   sync.Mutex
	lats []time.Duration
}

type writeStats struct {
	written, failed, consumed int64
	perSec                    float64
	lats                      []time.Duration
}

func (s writeStats) p(q float64) float64 {
	if len(s.lats) == 0 {
		return 0
	}

	return ms(s.lats[int(float64(len(s.lats)-1)*q)])
}

// write runs 350 concurrent calls for dur at the live rate, one doc per
// request, and a consumer. acked, when set, gets the id of each acked write.
func write(d *daemon, dur time.Duration, acked *[]string) *writer {
	const concurrent = 350

	w := &writer{start: time.Now(), stop: make(chan struct{})}
	gap := time.Duration(float64(time.Second) * concurrent / float64(*rate))
	deadline := time.Now().Add(dur)
	seed := uint64(time.Now().UnixNano())

	var ackMu sync.Mutex

	go w.consume(d)

	for g := range concurrent {
		w.wg.Add(1)

		go func() {
			defer w.wg.Done()

			r := rand.New(rand.NewPCG(uint64(g), seed))
			time.Sleep(time.Duration(r.Int64N(int64(gap))))

			for n := g; time.Now().Before(deadline); n += concurrent {
				for _, doc := range callDocs(r, time.Now(), n) {
					doc["_id"] = fmt.Sprintf("%s-%d", doc["call_id"], doc["seq"])
					body, _ := json.Marshal(doc)

					t0 := time.Now()
					code, _, lat := d.call("POST", "/outbox/_doc", body)

					if code == 0 || code >= 300 {
						w.failed.Add(1)
					} else {
						w.written.Add(1)

						w.mu.Lock()
						w.lats = append(w.lats, lat)
						w.mu.Unlock()

						if acked != nil {
							ackMu.Lock()
							*acked = append(*acked, doc["_id"].(string))
							ackMu.Unlock()
						}
					}

					time.Sleep(max(0, gap-time.Since(t0)))

					if !time.Now().Before(deadline) {
						return
					}
				}
			}
		}()
	}

	return w
}

func (w *writer) wait() writeStats {
	w.wg.Wait()

	el := time.Since(w.start).Seconds()

	for range 40 {
		if w.consumed.Load() >= w.written.Load() {
			break
		}

		time.Sleep(50 * time.Millisecond)
	}

	close(w.stop)

	slices.Sort(w.lats)

	return writeStats{written: w.written.Load(), failed: w.failed.Load(), consumed: w.consumed.Load(), perSec: float64(w.written.Load()) / el, lats: w.lats}
}

// consume reads the outbox with _watch and acks every 200 events; it
// reconnects after a restart.
func (w *writer) consume(d *daemon) {
	for {
		select {
		case <-w.stop:
			return
		default:
		}

		req, _ := http.NewRequest("POST", "http://ndb/outbox/_watch", strings.NewReader(`{"consumer":"esl","from":"now"}`))

		resp, err := d.http.Do(req)
		if err != nil {
			time.Sleep(100 * time.Millisecond)

			continue
		}

		w.read(d, resp.Body)
		resp.Body.Close()
	}
}

func (w *writer) read(d *daemon, body io.Reader) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 1<<20), 16<<20)

	n := 0

	for sc.Scan() {
		var ev struct {
			Pos string `json:"pos"`
			Op  string `json:"op"`
		}

		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Pos == "" {
			continue
		}

		if ev.Op == "put" {
			w.consumed.Add(1)
		}

		if n++; n%200 == 0 {
			d.call("POST", "/outbox/_ack", []byte(`{"consumer":"esl","pos":"`+ev.Pos+`"}`))
		}

		select {
		case <-w.stop:
			return
		default:
		}
	}
}

var eventTypes = []string{"channel_create", "channel_answer", "channel_bridge", "dtmf", "channel_hold", "channel_hangup"}

var vocab = strings.Fields("hello thanks account invoice payment card number please wait transfer agent support problem order delivery refund cancel plan upgrade internet signal slow router reset password email address confirm protocol yes no maybe tomorrow today morning afternoon value discount promotion contract")

// callDocs are the 7 docs of one call: 6 events (~1 KB) and a finishcall
// (~4 KB).
func callDocs(r *rand.Rand, at time.Time, n int) []map[string]any {
	base := map[string]any{
		"call_id":  fmt.Sprintf("call-%d-%016x", n, r.Uint64()),
		"customer": fmt.Sprintf("cust-%03d", r.IntN(400)),
		"caller":   fmt.Sprintf("+55119%08d", r.IntN(1e8)),
		"callee":   fmt.Sprintf("+55%010d", r.IntN(1e9)),
	}

	docs := make([]map[string]any, 0, 7)

	for i, t := range eventTypes {
		docs = append(docs, with(base, map[string]any{
			"type": t, "seq": i, "status": "pending", "vars": vars(r),
			"ts": at.Add(time.Duration(i) * 10 * time.Second).UTC().Format(time.RFC3339Nano),
		}))
	}

	return append(docs, with(base, map[string]any{
		"type": "finishcall", "seq": 6, "status": "pending", "vars": vars(r),
		"ts":       at.Add(70 * time.Second).UTC().Format(time.RFC3339Nano),
		"duration": r.IntN(600), "hangup_cause": "NORMAL_CLEARING", "transcript": words(r, 420),
	}))
}

func with(base, fields map[string]any) map[string]any {
	for k, v := range base {
		fields[k] = v
	}

	return fields
}

func vars(r *rand.Rand) map[string]string {
	m := make(map[string]string, 22)

	for i := range 22 {
		m[fmt.Sprintf("variable_%02d", i)] = fmt.Sprintf("%x", r.Uint64())
	}

	return m
}

func words(r *rand.Rand, n int) string {
	var b strings.Builder

	for range n {
		b.WriteString(vocab[r.IntN(len(vocab))])
		b.WriteByte(' ')
	}

	return b.String()
}

func du(dir string) string {
	var total int64

	filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			total += info.Size()
		}

		return nil
	})

	return fmt.Sprintf("%.0f MB", float64(total)/(1<<20))
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func check(err error) {
	if err != nil {
		panic(failure{err})
	}
}
