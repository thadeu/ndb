package bench

import (
	"fmt"
	"math/rand/v2"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// concurrentFor is how long each level of clients reads.
const concurrentFor = 15 * time.Second

// clientLevels are the numbers of clients that read at the same time, like
// apps that share one daemon.
var clientLevels = []int{1, 3, 10}

func clientsStep(n int) string { return fmt.Sprintf("concurrent: %d clients", n) }

// concurrent: n clients read at the same time, each one the reads of the
// other parts in a random order, back to back, for concurrentFor. The
// daemon has no pool: each read runs when it comes, so this shows what the
// time of a read and the RSS of the daemon become when apps share it.
func concurrent(d *daemon, probes []probe) []string {
	var lines []string

	for _, n := range clientLevels {
		var done atomic.Int64

		prog.step(clientsStep(n), func() string { return fmt.Sprintf("%d reads", done.Load()) })

		peak := d.phase()
		s := readTogether(d, probes, n, &done)
		rss := peak()

		prog.end(fmt.Sprintf("%.0f reads/s, p99 %.1f ms, RSS peak %d MB", s.perSec, s.p(.99), rss))

		key := fmt.Sprintf("concurrent.%d.", n)

		record(key+"p99", s.p(.99))
		record(key+"rss", float64(rss))
		record(key+"errors", float64(s.failed))

		lines = append(lines, fmt.Sprintf("| %d clients | %.0f reads/s, p50 %.1f ms, p99 %.1f ms, max %.1f ms, %d errors, RSS peak %d MB |",
			n, s.perSec, s.p(.5), s.p(.99), s.p(1), s.failed, rss))
	}

	return lines
}

// readTogether runs n clients for concurrentFor and gives their latencies.
func readTogether(d *daemon, probes []probe, n int, done *atomic.Int64) writeStats {
	var (
		mu     sync.Mutex
		lats   []time.Duration
		failed atomic.Int64
		wg     sync.WaitGroup
	)

	stop := time.Now().Add(concurrentFor)
	start := time.Now()

	for c := range n {
		wg.Add(1)

		go func() {
			defer wg.Done()

			r := rand.New(rand.NewPCG(uint64(c), 23))

			var mine []time.Duration

			for time.Now().Before(stop) {
				p := probes[r.IntN(len(probes))]

				code, _, took := d.call(p.method, p.path, []byte(p.body))
				if code == 0 || (code >= 300 && code != http.StatusNotFound) {
					failed.Add(1)
				}

				mine = append(mine, took)
				done.Add(1)
			}

			mu.Lock()
			lats = append(lats, mine...)
			mu.Unlock()
		}()
	}

	wg.Wait()

	slices.Sort(lats)

	return writeStats{
		written: int64(len(lats)),
		failed:  failed.Load(),
		perSec:  float64(len(lats)) / time.Since(start).Seconds(),
		lats:    lats,
	}
}
