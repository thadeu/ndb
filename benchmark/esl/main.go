// Command esl is the ESL outbox benchmark: an app that talks to FreeSWITCH
// writes 7 docs per call (6 events and a finishcall) and workers consume them.
// It starts its own daemon in a temp dir and runs four phases in about two
// minutes:
//
//  1. live: concurrent calls write one doc at a time (strict), a _watch
//     consumer acks them;
//  2. crash: kill -9 while writing, restart, read back every acked write;
//  3. load: bulk load of calls into a daily partition;
//  4. reads: by id, by indexed fields, counts, aggregates.
//
// The daemon RSS is sampled all the time. Run it with make bench.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	bin     = flag.String("ndb", "bin/ndb", "the ndb binary")
	liveFor = flag.Duration("live", 30*time.Second, "how long the live phase writes")
	rate    = flag.Int("rate", 400, "live docs per second")
	calls   = flag.Int("calls", 30_000, "calls of the load phase (7 docs each)")
	keep    = flag.Bool("keep", false, "keep the data dir")
)

func main() {
	flag.Parse()

	dir, err := os.MkdirTemp("", "ndb-bench-")
	check(err)

	if !*keep {
		defer os.RemoveAll(dir)
	}

	d := &daemon{dir: dir}
	d.start()

	defer d.stop()

	go d.sample()

	var report []string

	report = append(report, live(d)...)
	report = append(report, crash(d)...)
	report = append(report, load(d)...)
	report = append(report, reads(d)...)

	fmt.Println()
	fmt.Println("| Phase | Result |")
	fmt.Println("|---|---|")

	for _, line := range report {
		fmt.Println(line)
	}

	fmt.Printf("| disk | %s |\n", du(dir))
}

// daemon is the ndb process under test, with its RSS peak per phase.
type daemon struct {
	dir  string
	cmd  *exec.Cmd
	http *http.Client

	mu   sync.Mutex
	peak int64
}

func (d *daemon) sock() string { return filepath.Join(d.dir, "ndb.sock") }

func (d *daemon) start() {
	os.Remove(d.sock())

	d.cmd = exec.Command(*bin, "-config", filepath.Join(d.dir, "none.yml"), "-data-dir", filepath.Join(d.dir, "data"), "-unix", d.sock())
	d.cmd.Stderr = io.Discard
	check(d.cmd.Start())

	d.http = &http.Client{Transport: &http.Transport{
		DialContext:         func(context.Context, string, string) (net.Conn, error) { return net.Dial("unix", d.sock()) },
		MaxIdleConnsPerHost: 512,
	}}

	for range 200 {
		if code, _, _ := d.call("GET", "/_health", nil); code == 200 {
			return
		}

		time.Sleep(25 * time.Millisecond)
	}

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

// rss is the daemon RSS in MB (ps works on Linux and macOS).
func (d *daemon) rss() int64 {
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(d.cmd.Process.Pid)).Output()
	if err != nil {
		return 0
	}

	kb, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)

	return kb / 1024
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
	fmt.Printf("live: %s at %d docs/s, strict, with a _watch consumer\n", *liveFor, *rate)
	d.must("PUT", "/outbox", `{"time":"ts","indexes":["status","call_id"],"durability":"strict"}`)

	peak := d.phase()
	w := write(d, *liveFor, nil)
	stats := w.wait()

	return []string{
		fmt.Sprintf("| live writes | %d docs, %.0f/s, %d failed |", stats.written, stats.perSec, stats.failed),
		fmt.Sprintf("| live latency | p50 %.1f ms, p99 %.1f ms, p99.9 %.1f ms, max %.1f ms |", stats.p(.5), stats.p(.99), stats.p(.999), stats.p(1)),
		fmt.Sprintf("| live consumer | lag %d |", stats.written-stats.consumed),
		fmt.Sprintf("| live RSS | peak %d MB |", peak()),
	}
}

// crash: kill -9 while writing, restart, every acked write must be there.
func crash(d *daemon) []string {
	fmt.Println("crash: kill -9 while writing, restart, read back")

	var acked []string

	w := write(d, 15*time.Second, &acked)

	time.Sleep(7 * time.Second)
	d.kill()

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

	return []string{
		fmt.Sprintf("| crash | %d acked writes, %d missing, restart %d ms |", len(acked), missing, restart.Milliseconds()),
	}
}

// load: bulk load of calls into a daily partition.
func load(d *daemon) []string {
	fmt.Printf("load: %d calls (%d docs) into a daily partition\n", *calls, *calls*7)
	d.must("PUT", "/events", `{"time":"ts","indexes":["call_id","customer","type"],"partition_by":"day","retention":"7d","max_line_size":16777216}`)

	peak := d.phase()
	day := time.Now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)
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
				batch = batch[:0]
			}

			for n := w; n < *calls; n += writers {
				at := day.Add(time.Duration(float64(n) / float64(*calls) * float64(24*time.Hour)))
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
	settle := d.settle("/events-" + day.Format("2006-01-02"))

	return []string{
		fmt.Sprintf("| load | %d docs in %.0f s, %.0f docs/s |", *calls*7, el, float64(*calls*7)/el),
		fmt.Sprintf("| load settle | seals and compactions done %.0f s after the load |", settle.Seconds()),
		fmt.Sprintf("| load RSS | peak %d MB, after %d MB |", peak(), d.rss()),
	}
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

// reads: the queries of the ESL, the median of 3 runs each.
func reads(d *daemon) []string {
	fmt.Println("reads")

	peak := d.phase()
	part := "/events-" + time.Now().UTC().Add(-24*time.Hour).Format("2006-01-02")

	var row struct {
		ID     string `json:"_id"`
		CallID string `json:"call_id"`
	}

	check(json.Unmarshal(bytes.TrimSpace(d.must("POST", part+"/_search", `{"limit":1}`)), &row))

	queries := []struct{ name, method, path, body string }{
		{"get by id", "GET", part + "/_doc/" + row.ID, ""},
		{"search call_id (indexed)", "POST", "/events/_search", fmt.Sprintf(`{"query":{"call_id":%q}}`, row.CallID)},
		{"last 20 of a customer", "POST", "/events/_search", `{"query":{"customer":"cust-042"},"sort":{"ts":-1},"limit":20}`},
		{"count of a customer", "POST", "/events/_count", `{"query":{"customer":"cust-042"}}`},
		{"count finishcall", "POST", "/events/_count", `{"query":{"type":"finishcall"}}`},
		{"count all", "POST", "/events/_count", `{}`},
		{"calls per customer, top 5", "POST", "/events/_aggregate", `{"pipeline":[{"$match":{"type":"finishcall"}},{"$group":{"_id":"$customer","n":{"$count":{}}}},{"$sort":{"n":-1}},{"$limit":5}]}`},
		{"avg duration per hour", "POST", "/events/_aggregate", `{"pipeline":[{"$match":{"type":"finishcall"}},{"$group":{"_id":{"$dateTrunc":{"date":"$ts","unit":"hour"}},"avg":{"$avg":"$duration"}}}]}`},
	}

	var out []string

	for _, q := range queries {
		var times []time.Duration

		for range 3 {
			code, body, took := d.call(q.method, q.path, []byte(q.body))
			if code >= 300 {
				check(fmt.Errorf("%s: %d %s", q.name, code, body))
			}

			times = append(times, took)
		}

		slices.Sort(times)
		out = append(out, fmt.Sprintf("| %s | %.1f ms |", q.name, ms(times[1])))
	}

	return append(out, fmt.Sprintf("| reads RSS | peak %d MB |", peak()))
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
		fmt.Fprintln(os.Stderr, "bench:", err)
		os.Exit(1)
	}
}
