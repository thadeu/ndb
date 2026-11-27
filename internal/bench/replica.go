package bench

import (
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Replication (M31), on two daemons of its own on this host: an owner on
// TCP and a copy that pulls it. A writer sends _bulk to the owner without a
// stop; a prober writes one document to the owner and times until the copy
// has it (the lag). Then GET by _id on the copy, as in the mixed part.

// replicaFor is how long the load runs (a var for the test).
var replicaFor = 5 * time.Second

const replicaKey = "NDB_ENCRYPTION_KEY=5151515151515151515151515151515151515151515151515151515151515151"

func replicaPlan() []planned {
	return []planned{
		{"replica: lag under _bulk", replicaFor + 2*time.Second},
		{"replica: GET on the copy", mixedFor},
	}
}

func replicaPart(dir string) []string {
	owner := &daemon{dir: filepath.Join(dir, "owner"), tcp: freeTCP(), env: []string{replicaKey}}

	check(os.MkdirAll(owner.dir, 0o755))
	owner.start()

	defer owner.stop()

	copyDir := filepath.Join(dir, "copy")

	check(os.MkdirAll(copyDir, 0o755))

	cp := &daemon{dir: copyDir, env: []string{replicaKey, "NDB_REPLICA_OF=tcp://" + owner.tcp}, ready: 15 * time.Second}

	cp.start()

	defer cp.stop()

	owner.must("PUT", "/hot", `{"indexes":["group"]}`)
	owner.must("PUT", "/probe", `{}`)

	prog.step("replica: lag under _bulk", nil)

	lags, written := replicaLoad(owner, cp)

	prog.end(fmt.Sprintf("lag p50 %.1f ms, p99 %.1f ms, %d docs written", msq(lags, .5), msq(lags, .99), written))

	record("replica.lag_p99_ms", msq(lags, .99))

	replicaSynced(cp, written)

	prog.step("replica: GET on the copy", nil)

	r := rand.New(rand.NewPCG(31, 1))

	var get []time.Duration

	for stop := time.Now().Add(mixedFor); time.Now().Before(stop); {
		code, _, took := cp.call("GET", fmt.Sprintf("/hot/_doc/h%06d", r.IntN(int(written))), nil)
		if code != 200 {
			check(fmt.Errorf("replica: GET on the copy answered %d", code))
		}

		get = append(get, took)
	}

	slices.Sort(get)

	prog.end(fmt.Sprintf("get p50 %.0f µs, p99 %.0f µs", us(get, .5), us(get, .99)))

	record("replica.get_id_us", us(get, .5))

	return []string{
		fmt.Sprintf("| lag of the copy under _bulk | p50 %.1f ms, p99 %.1f ms, max %.1f ms (%d probes, %d docs written) |", msq(lags, .5), msq(lags, .99), msq(lags, 1), len(lags), written),
		fmt.Sprintf("| GET by _id on the copy | p50 %.0f µs, p99 %.0f µs |", us(get, .5), us(get, .99)),
	}
}

// replicaLoad sends _bulk to the owner for replicaFor and, at the same
// time, probes the lag of the copy. It gives the sorted lags and the count
// of hot documents written.
func replicaLoad(owner, cp *daemon) ([]time.Duration, int64) {
	var (
		wg      sync.WaitGroup
		stop    atomic.Bool
		written atomic.Int64
	)

	wg.Add(1)

	go func() {
		defer wg.Done()

		for i := 0; !stop.Load(); i++ {
			owner.must("POST", "/hot/_bulk", mixedBulk("h", i*mixedBatch))
			written.Add(mixedBatch)
		}
	}()

	var lags []time.Duration

	for i, end := 0, time.Now().Add(replicaFor); time.Now().Before(end); i++ {
		id := fmt.Sprintf("p%06d", i)

		owner.must("POST", "/probe/_doc", fmt.Sprintf(`{"_id":%q}`, id))

		acked := time.Now()

		for {
			if code, _, _ := cp.call("GET", "/probe/_doc/"+id, nil); code == 200 {
				break
			}

			if time.Since(acked) > 10*time.Second {
				check(fmt.Errorf("replica: the copy did not get %s in 10 s", id))
			}

			time.Sleep(200 * time.Microsecond)
		}

		lags = append(lags, time.Since(acked))

		time.Sleep(20 * time.Millisecond)
	}

	stop.Store(true)
	wg.Wait()

	slices.Sort(lags)

	return lags, written.Load()
}

// replicaSynced waits until the copy has every hot document and reads
// local.
func replicaSynced(cp *daemon, written int64) {
	for start := time.Now(); time.Since(start) < 30*time.Second; time.Sleep(50 * time.Millisecond) {
		_, body, _ := cp.call("GET", "/_replication", nil)

		if strings.Contains(string(body), `"synced":true`) && cp.count("/hot") == int(written) {
			return
		}
	}

	check(fmt.Errorf("replica: the copy did not catch up in 30 s"))
}

// freeTCP is a loopback TCP address that is free now.
func freeTCP() string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	check(err)

	defer ln.Close()

	return ln.Addr().String()
}

// msq is the q quantile of the sorted lats, in ms.
func msq(lats []time.Duration, q float64) float64 {
	if len(lats) == 0 {
		return 0
	}

	return float64(lats[int(float64(len(lats)-1)*q)].Microseconds()) / 1e3
}
