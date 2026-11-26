package bench

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Fast reads (M29), on a daemon of its own: GET by _id and a small _search
// on a hot collection, first alone, then while a second client sends _bulk
// without a stop. The times are of one client over the unix socket, in µs.

const (
	mixedDocs   = 20_000
	mixedGroups = 2_000
	mixedBatch  = 1_000
)

// mixedFor is how long each phase reads (a var for the test).
var mixedFor = 3 * time.Second

func mixedPlan() []planned {
	return []planned{
		{"mixed: load", 2 * time.Second},
		{"mixed: alone", mixedFor},
		{"mixed: with a writer", mixedFor},
	}
}

func mixedPart(dir string) []string {
	d := &daemon{dir: filepath.Join(dir, "mixed")}

	check(os.MkdirAll(d.dir, 0o755))
	d.start()

	defer d.stop()

	d.must("PUT", "/hot", `{"indexes":["group"]}`)

	prog.step("mixed: load", nil)

	for from := 0; from < mixedDocs; from += mixedBatch {
		d.must("POST", "/hot/_bulk", mixedBulk("h", from))
	}

	prog.end(fmt.Sprintf("%d documents", mixedDocs))

	prog.step("mixed: alone", nil)

	get, search := mixedReadsOnce(d)

	prog.end(fmt.Sprintf("get p50 %.0f µs, search p50 %.0f µs", us(get, .5), us(search, .5)))

	record("get.id_us", us(get, .5))
	record("search.small_us", us(search, .5))

	prog.step("mixed: with a writer", nil)

	stop := make(chan struct{})

	var (
		wg      sync.WaitGroup
		written atomic.Int64
	)

	wg.Add(1)

	go func() {
		defer wg.Done()

		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}

			d.must("POST", "/hot/_bulk", mixedBulk("w", i*mixedBatch))
			written.Add(mixedBatch)
		}
	}()

	mget, msearch := mixedReadsOnce(d)

	close(stop)
	wg.Wait()

	prog.end(fmt.Sprintf("get p99 %.0f µs, search p99 %.0f µs, %d docs written", us(mget, .99), us(msearch, .99), written.Load()))

	record("mixed.get_p99_us", us(mget, .99))
	record("mixed.search_p99_us", us(msearch, .99))

	line := func(name string, alone, mixed []time.Duration) string {
		return fmt.Sprintf("| %s | alone p50 %.0f µs, p99 %.0f µs; with a writer p50 %.0f µs, p99 %.0f µs |",
			name, us(alone, .5), us(alone, .99), us(mixed, .5), us(mixed, .99))
	}

	return []string{
		line("GET by _id", get, mget),
		line("small _search (10 docs)", search, msearch),
		fmt.Sprintf("| *the writer* | %d docs in _bulk of %d |", written.Load(), mixedBatch),
	}
}

// mixedBulk is a _bulk body of mixedBatch docs with the _id prefix p.
func mixedBulk(p string, from int) string {
	var b strings.Builder

	b.WriteString(`{"docs":[`)

	for i := from; i < from+mixedBatch; i++ {
		if i > from {
			b.WriteByte(',')
		}

		fmt.Fprintf(&b, `{"_id":"%s%06d","group":"%s%04d","n":%d,"note":"a short note of the document"}`, p, i, p, i%mixedGroups, i)
	}

	b.WriteString(`]}`)

	return b.String()
}

// mixedReadsOnce runs GETs by _id and small searches, one after the other,
// for mixedFor, and gives their sorted latencies.
func mixedReadsOnce(d *daemon) (get, search []time.Duration) {
	r := rand.New(rand.NewPCG(29, 1))

	for stop := time.Now().Add(mixedFor); time.Now().Before(stop); {
		code, _, took := d.call("GET", fmt.Sprintf("/hot/_doc/h%06d", r.IntN(mixedDocs)), nil)
		if code != 200 {
			check(fmt.Errorf("mixed: GET answered %d", code))
		}

		get = append(get, took)

		body := fmt.Sprintf(`{"query":{"group":"h%04d"},"limit":10}`, r.IntN(mixedGroups))

		code, _, took = d.call("POST", "/hot/_search", []byte(body))
		if code != 200 {
			check(fmt.Errorf("mixed: _search answered %d", code))
		}

		search = append(search, took)
	}

	slices.Sort(get)
	slices.Sort(search)

	return get, search
}

// us is the q quantile of the sorted lats, in µs.
func us(lats []time.Duration, q float64) float64 {
	if len(lats) == 0 {
		return 0
	}

	return float64(lats[int(float64(len(lats)-1)*q)].Nanoseconds()) / 1e3
}
