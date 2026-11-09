package bench

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// streams: the Redis Streams parity of docs/dev/streams.md on a daemon of
// its own. Appends from 1 and 10 clients (strict and relaxed) and in
// batches, groups read and acked by 1, 3 and 10 workers, the time from an
// append to its delivery to a blocked read, a claim of the entries of a
// dead worker, and a kill -9 while apps append and workers ack: no acked
// append, no delivered entry and no ack may be lost.

const (
	streamAppendFor = 3 * time.Second
	streamEntries   = 30_000
)

var streamWorkers = []int{1, 3, 10}

func streamsPlan() []planned {
	steps := []planned{{"streams: appends", 4 * streamAppendFor}, {"streams: load", 3 * time.Second}}

	for _, n := range streamWorkers {
		steps = append(steps, planned{fmt.Sprintf("streams: %d workers", n), 4 * time.Second})
	}

	return append(steps, planned{"streams: latency", 3 * time.Second}, planned{"streams: claim", time.Second}, planned{"streams: crash", 6 * time.Second})
}

func streamsPart(dir string) []string {
	d := &daemon{dir: filepath.Join(dir, "streams")}

	check(os.MkdirAll(d.dir, 0o755))
	check(os.WriteFile(filepath.Join(d.dir, "ndb.yml"), []byte("idle_close: 10m\n"), 0o644))
	d.start()

	defer d.stop()

	d.must("PUT", "/relaxed", `{"stream":true,"durability":"relaxed"}`)

	var lines []string

	lines = append(lines, streamAppends(d)...)
	lines = append(lines, streamWorkersPart(d)...)
	lines = append(lines, streamLatency(d))
	lines = append(lines, streamClaim(d))
	lines = append(lines, streamCrash(d)...)

	return lines
}

// streamAppends: one entry per call from 1 and 10 clients, strict and
// relaxed, and batches of 100.
func streamAppends(d *daemon) []string {
	prog.step("streams: appends", nil)

	var lines []string

	for _, c := range []struct {
		stream  string
		clients int
		key     string
	}{{"strict", 1, "append_1"}, {"strict", 10, "append_10"}, {"relaxed", 10, "append_relaxed_10"}} {
		s := appendFor(d, "/"+c.stream, c.clients, 1)

		record("streams."+c.key+"_p99", s.p(.99))

		lines = append(lines, fmt.Sprintf("| append, %s, %d clients | %.0f entries/s, p50 %.1f ms, p99 %.1f ms, %d failed |", c.stream, c.clients, s.perSec, s.p(.5), s.p(.99), s.failed))
		record("streams."+c.key+"_failed", float64(s.failed))
	}

	b := appendFor(d, "/batched", 1, 100)

	lines = append(lines, fmt.Sprintf("| append, strict, batches of 100 | %.0f entries/s, p99 %.1f ms per batch |", b.perSec, b.p(.99)))

	prog.end("done")

	return lines
}

type streamStats struct {
	lats   []float64
	perSec float64
	failed int
}

func (s streamStats) p(q float64) float64 {
	if len(s.lats) == 0 {
		return 0
	}

	return s.lats[int(q*float64(len(s.lats)-1))]
}

func appendFor(d *daemon, stream string, clients, batch int) streamStats {
	var (
		mu     sync.Mutex
		lats   []float64
		failed int
		wg     sync.WaitGroup
		n      atomic.Int64
	)

	body := []byte(`{"doc":{"call_id":"c1","type":"answer","customer":"cu1","ts":"2026-11-09T10:00:00Z"}}`)

	if batch > 1 {
		body = []byte(`{"docs":[` + strings.TrimSuffix(strings.Repeat(`{"call_id":"c1","type":"answer"},`, batch), ",") + `]}`)
	}

	stop := time.Now().Add(streamAppendFor)

	for range clients {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for time.Now().Before(stop) {
				code, _, took := d.call("POST", stream+"/_streams/append", body)

				mu.Lock()
				lats = append(lats, float64(took.Microseconds())/1000)

				if code != 201 {
					failed++
				}

				mu.Unlock()

				n.Add(int64(batch))
			}
		}()
	}

	wg.Wait()

	slices.Sort(lats)

	return streamStats{lats: lats, perSec: float64(n.Load()) / streamAppendFor.Seconds(), failed: failed}
}

// streamWorkersPart: streamEntries entries, then for 1, 3 and 10 workers a
// group of their own read and ack all of them, 100 at a time.
func streamWorkersPart(d *daemon) []string {
	prog.step("streams: load", nil)

	batch := `{"docs":[` + strings.TrimSuffix(strings.Repeat(`{"call_id":"c1","type":"answer","n":1},`, 1000), ",") + `]}`

	for range streamEntries / 1000 {
		d.must("POST", "/work/_streams/append", batch)
	}

	prog.end(fmt.Sprintf("%d entries", streamEntries))

	var lines []string

	for _, n := range streamWorkers {
		prog.step(fmt.Sprintf("streams: %d workers", n), nil)

		group := fmt.Sprintf("g%d", n)

		d.must("PUT", "/work/_streams/groups/"+group, `{"start":"0"}`)

		var (
			done atomic.Int64
			wg   sync.WaitGroup
		)

		start := time.Now()

		for w := range n {
			wg.Add(1)

			go func() {
				defer wg.Done()

				read := fmt.Sprintf(`{"consumer":"w%d","count":100}`, w)

				for {
					_, out, _ := d.call("POST", "/work/_streams/groups/"+group+"/read", []byte(read))
					ids := rowIDs(out)

					if len(ids) == 0 {
						return
					}

					ack, _ := json.Marshal(map[string][]string{"ids": ids})
					d.call("POST", "/work/_streams/groups/"+group+"/ack", ack)

					done.Add(int64(len(ids)))
				}
			}()
		}

		wg.Wait()

		rate := float64(done.Load()) / time.Since(start).Seconds()

		record(fmt.Sprintf("streams.workers_%d", n), rate)

		lines = append(lines, fmt.Sprintf("| group read + ack, %d workers | %.0f entries/s, %d of %d delivered |", n, rate, done.Load(), streamEntries))
		record(fmt.Sprintf("streams.workers_%d_missing", n), float64(streamEntries-done.Load()))
		prog.end(fmt.Sprintf("%.0f entries/s", rate))
	}

	return lines
}

// rowIDs reads the _id of each NDJSON row.
func rowIDs(out []byte) []string {
	var ids []string

	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<26)

	for sc.Scan() {
		var r struct {
			ID string `json:"_id"`
		}

		if json.Unmarshal(sc.Bytes(), &r) == nil && r.ID != "" {
			ids = append(ids, r.ID)
		}
	}

	return ids
}

// streamLatency: a worker waits with block_ms; an app appends an entry
// every 5 ms with its send time; the latency is the time to the worker.
func streamLatency(d *daemon) string {
	prog.step("streams: latency", nil)

	d.must("PUT", "/lat/_streams/groups/g", `{"start":"$"}`)

	const sends = 300

	var (
		mu   sync.Mutex
		lats []float64
	)

	got := make(chan struct{})

	go func() {
		defer close(got)

		for len(lats) < sends {
			_, out, _ := d.call("POST", "/lat/_streams/groups/g/read", []byte(`{"consumer":"w","count":100,"block_ms":2000,"no_ack":true}`))
			now := time.Now().UnixNano()
			n := 0

			sc := bufio.NewScanner(bytes.NewReader(out))

			for sc.Scan() {
				var r struct {
					T int64 `json:"t"`
				}

				if json.Unmarshal(sc.Bytes(), &r) == nil && r.T > 0 {
					mu.Lock()
					lats = append(lats, float64(now-r.T)/1e6)
					mu.Unlock()

					n++
				}
			}

			if n == 0 && len(out) == 0 {
				mu.Lock()
				short := len(lats) < sends
				mu.Unlock()

				if !short {
					return
				}
			}
		}
	}()

	for range sends {
		d.call("POST", "/lat/_streams/append", fmt.Appendf(nil, `{"doc":{"t":%d}}`, time.Now().UnixNano()))
		time.Sleep(5 * time.Millisecond)
	}

	select {
	case <-got:
	case <-time.After(5 * time.Second):
	}

	mu.Lock()
	defer mu.Unlock()

	slices.Sort(lats)

	s := streamStats{lats: lats}

	record("streams.latency_p99", s.p(.99))
	record("streams.latency_missing", float64(sends-len(lats)))
	prog.end(fmt.Sprintf("p99 %.1f ms", s.p(.99)))

	return fmt.Sprintf("| append to delivery (block_ms) | p50 %.1f ms, p99 %.1f ms, max %.1f ms, %d of %d delivered |", s.p(.5), s.p(.99), s.p(1), len(lats), sends)
}

// streamClaim: a worker reads 100 entries and dies; another one claims
// them with a cursor.
func streamClaim(d *daemon) string {
	prog.step("streams: claim", nil)

	d.must("PUT", "/work/_streams/groups/claim", `{"start":"0"}`)
	d.must("POST", "/work/_streams/groups/claim/read", `{"consumer":"dead","count":100}`)

	var res struct {
		Entries []json.RawMessage `json:"entries"`
	}

	claimed := 0
	cursor := "0-0"

	for range 10 {
		out := d.must("POST", "/work/_streams/groups/claim/claim", fmt.Sprintf(`{"consumer":"alive","min_idle_ms":0,"cursor":%q,"count":100}`, cursor))

		var page struct {
			Cursor  string            `json:"cursor"`
			Entries []json.RawMessage `json:"entries"`
		}

		check(json.Unmarshal(out, &page))

		claimed += len(page.Entries)
		res.Entries = append(res.Entries, page.Entries...)
		cursor = page.Cursor

		if cursor == "0-0" {
			break
		}
	}

	record("streams.claim_missing", float64(100-claimed))
	prog.end(fmt.Sprintf("%d of 100 claimed", claimed))

	return fmt.Sprintf("| claim of a dead worker | %d of 100 entries claimed |", claimed)
}

// streamCrash: apps append and a worker reads and acks; kill -9; after the
// restart every acked append is there, every delivered and unacked entry
// is pending, and no acked entry is pending.
func streamCrash(d *daemon) []string {
	prog.step("streams: crash", nil)

	d.must("PUT", "/crash/_streams/groups/g", `{"start":"0"}`)

	var (
		mu        sync.Mutex
		appended  []string
		delivered = map[string]bool{}
		acked     = map[string]bool{}
		unsure    = map[string]bool{}
		wg        sync.WaitGroup
	)

	stop := make(chan struct{})

	for range 5 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for {
				select {
				case <-stop:
					return
				default:
				}

				code, out, _ := d.call("POST", "/crash/_streams/append", []byte(`{"doc":{"n":1}}`))

				var r struct {
					ID string `json:"_id"`
				}

				if code == 201 && json.Unmarshal(out, &r) == nil {
					mu.Lock()
					appended = append(appended, r.ID)
					mu.Unlock()
				}
			}
		}()
	}

	wg.Add(1)

	go func() {
		defer wg.Done()

		for {
			select {
			case <-stop:
				return
			default:
			}

			code, out, _ := d.call("POST", "/crash/_streams/groups/g/read", []byte(`{"consumer":"w","count":50}`))
			ids := rowIDs(out)

			if code != 200 {
				continue
			}

			mu.Lock()

			for _, id := range ids {
				delivered[id] = true
			}

			mu.Unlock()

			half := ids[:len(ids)/2]

			if len(half) == 0 {
				continue
			}

			ack, _ := json.Marshal(map[string][]string{"ids": half})

			code, _, _ = d.call("POST", "/crash/_streams/groups/g/ack", ack)

			mu.Lock()

			for _, id := range half {
				if code == 200 {
					acked[id] = true
				} else {
					// No answer: the kill came while the ack ran, so it may
					// or may not be on disk.
					unsure[id] = true
				}
			}

			mu.Unlock()
		}
	}()

	time.Sleep(2 * time.Second)

	d.kill()
	close(stop)
	wg.Wait()

	d.start()

	missing := 0

	for _, id := range appended {
		if code, _, _ := d.call("GET", "/crash/_doc/"+id, nil); code != 200 {
			missing++
		}
	}

	out := d.must("POST", "/crash/_streams/groups/g/pending", `{"start":"-","end":"+","count":10000}`)

	var pend []struct {
		ID string `json:"_id"`
	}

	check(json.Unmarshal(out, &pend))

	pending := map[string]bool{}

	for _, p := range pend {
		pending[p.ID] = true
	}

	lost, back := 0, 0

	for id := range delivered {
		if unsure[id] {
			continue
		}

		switch {
		case acked[id] && pending[id]:
			back++
		case !acked[id] && !pending[id]:
			lost++
		}
	}

	record("streams.crash_missing", float64(missing))
	record("streams.crash_pel_lost", float64(lost))
	record("streams.crash_ack_lost", float64(back))
	prog.end(fmt.Sprintf("%d acked appends, %d missing; %d delivered, %d lost from the PEL, %d acks lost", len(appended), missing, len(delivered), lost, back))

	return []string{
		fmt.Sprintf("| crash (kill -9) | %d acked appends, %d missing, restart ok |", len(appended), missing),
		fmt.Sprintf("| crash: group state | %d delivered, %d acked; after restart %d lost from the PEL, %d acks lost |", len(delivered), len(acked), lost, back),
	}
}
