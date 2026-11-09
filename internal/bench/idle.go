package bench

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// idleWindow is how long the idle phase watches the daemon do nothing.
const idleWindow = 10 * time.Second

// idleDays is how many daily partitions the idle phase gives one
// partitioned collection, on top of the plain ones.
const idleDays = 365

// idle: many small collections that stop writing, on a daemon of their own
// with idle_close 2s, plus a partitioned collection with a year of daily
// partitions. What an idle collection costs is what the daemon holds for it
// once it sleeps: disk (blocks, not sizes), CPU, RSS and open files. Then
// the first call on a sleeping collection (the wake), and a kill -9 while
// writes wake collections and others go back to sleep: every acked write
// must be there after the restart.
func idle(dir string, n int) []string {
	var made atomic.Int32

	total := n + idleDays

	prog.step("idle: create", func() string { return fmt.Sprintf("%d of %d collections", made.Load(), total) })

	d := &daemon{dir: filepath.Join(dir, "idle")}
	check(os.MkdirAll(d.dir, 0o755))
	check(os.WriteFile(filepath.Join(d.dir, "ndb.yml"), []byte("idle_close: 2s\n"), 0o644))
	d.start()

	defer d.stop()

	time.Sleep(time.Second)

	rss0, fds0 := d.rssKB(), d.fds()

	for i := range n {
		coll := fmt.Sprintf("/idle_%03d", i)

		d.must("PUT", coll, `{"indexes":["status"]}`)
		d.must("POST", coll+"/_bulk", idleDocs(i))
		made.Add(1)
	}

	d.must("PUT", "/logs", `{"time":"ts","partition_by":"day"}`)
	d.must("POST", "/logs/_bulk", yearDocs())
	made.Add(idleDays)

	prog.end(fmt.Sprintf("%d collections of 10 docs, %d daily partitions", n, idleDays))

	var sleeping atomic.Int64

	prog.step("idle: sleep", func() string { return fmt.Sprintf("%d of %d asleep", sleeping.Load(), total) })

	slept := d.waitAsleep(total, &sleeping)

	prog.end(fmt.Sprintf("%d of %d asleep", slept, total))
	prog.step("idle: at rest", nil)

	cpu0 := d.cpu()
	time.Sleep(idleWindow)
	cpu := d.cpu() - cpu0

	blocks, reserve := diskOf(filepath.Join(d.dir, "data"))
	disk := blocks / 1024
	rss, fds := d.rssKB()-rss0, d.fds()-fds0
	pct := 100 * cpu.Seconds() / idleWindow.Seconds()

	prog.end(fmt.Sprintf("%d ms of CPU in %s, %s open files", cpu.Milliseconds(), idleWindow, count(fds, fds0)))

	record("idle.asleep", float64(total-slept))
	record("idle.disk_kb", float64(disk)/float64(total))
	record("idle.reserve_kb", float64(reserve)/1024/float64(total))
	record("idle.cpu_pct", pct)
	record("idle.rss_kb", float64(rss)/float64(total))

	if fds0 >= 0 {
		record("idle.fds", float64(fds)/float64(total))
	}

	prog.step("idle: wake", nil)

	wake := d.wakeTime(n)

	prog.end(fmt.Sprintf("median %.1f ms", wake))
	record("idle.wake_ms", wake)

	prog.step("idle: crash", nil)

	acked, missing := d.crashWhileWaking(n)

	prog.end(fmt.Sprintf("%d acked, %d missing", acked, missing))
	record("idle.missing", float64(missing))

	return []string{
		fmt.Sprintf("| idle collections | %d of 10 docs and %d daily partitions, %d asleep |", n, idleDays, slept),
		fmt.Sprintf("| idle disk | %.1f MB, %d KB per collection, %.1f KB of it past the data |", float64(disk)/1024, disk/int64(total), float64(reserve)/1024/float64(total)),
		fmt.Sprintf("| idle CPU | %.1f%% of a core (%d ms in %s) |", pct, cpu.Milliseconds(), idleWindow),
		fmt.Sprintf("| idle RSS | +%d MB, %d KB per collection |", rss/1024, rss/int64(total)),
		fmt.Sprintf("| idle files | +%s open |", count(fds, fds0)),
		fmt.Sprintf("| idle wake | first call on a sleeping collection: median %.1f ms |", wake),
		fmt.Sprintf("| idle crash | kill -9 while collections wake and sleep: %d acked, %d missing |", acked, missing),
	}
}

// waitAsleep waits until want collections sleep (at most 30 s), and gives
// how many do.
func (d *daemon) waitAsleep(want int, asleep *atomic.Int64) int {
	deadline := time.Now().Add(30 * time.Second)

	for {
		var stats struct {
			Totals struct {
				Asleep int `json:"asleep"`
			} `json:"totals"`
		}

		check(json.Unmarshal(d.must("GET", "/_stats", ""), &stats))
		asleep.Store(int64(stats.Totals.Asleep))

		if stats.Totals.Asleep >= want || time.Now().After(deadline) {
			return stats.Totals.Asleep
		}

		time.Sleep(500 * time.Millisecond)
	}
}

// wakeTime is the median time of the first call on 9 sleeping collections.
func (d *daemon) wakeTime(n int) float64 {
	var times []time.Duration

	for i := range 9 {
		_, _, took := d.call("POST", fmt.Sprintf("/idle_%03d/_count", i*n/9), []byte(`{}`))
		times = append(times, took)
	}

	slices.Sort(times)

	return ms(times[len(times)/2])
}

// crashWhileWaking writes to random collections for 4 s (each write wakes
// a sleeping one; the ones left alone sleep again after 2 s), kills the
// daemon in the middle, restarts it and reads back every acked write.
func (d *daemon) crashWhileWaking(n int) (int, int) {
	type ack struct{ coll, id string }

	var (
		mu    sync.Mutex
		acked []ack
		stop  atomic.Bool
		wg    sync.WaitGroup
	)

	for w := range 4 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			r := rand.New(rand.NewPCG(uint64(w), 19))

			for i := 0; !stop.Load(); i++ {
				coll := fmt.Sprintf("/idle_%03d", r.IntN(n))
				id := fmt.Sprintf("crash-%d-%d", w, i)

				if code, _, _ := d.call("POST", coll+"/_doc", []byte(`{"_id":"`+id+`","status":"crash"}`)); code >= 200 && code < 300 {
					mu.Lock()
					acked = append(acked, ack{coll, id})
					mu.Unlock()
				}

				time.Sleep(5 * time.Millisecond)
			}
		}()
	}

	time.Sleep(3 * time.Second)
	d.kill()
	stop.Store(true)
	wg.Wait()
	d.start()

	missing := 0

	for _, a := range acked {
		if code, _, _ := d.call("GET", a.coll+"/_doc/"+a.id, nil); code != 200 {
			missing++
		}
	}

	return len(acked), missing
}

// yearDocs are one doc a day for the last idleDays days.
func yearDocs() string {
	var b strings.Builder

	b.WriteString(`{"docs":[`)

	day := time.Now().UTC().Truncate(24 * time.Hour)

	for i := range idleDays {
		if i > 0 {
			b.WriteByte(',')
		}

		fmt.Fprintf(&b, `{"ts":%q,"n":%d}`, day.Add(-time.Duration(i)*24*time.Hour).Format(time.RFC3339), i)
	}

	b.WriteString(`]}`)

	return b.String()
}

func idleDocs(i int) string {
	var b strings.Builder

	b.WriteString(`{"docs":[`)

	for j := range 10 {
		if j > 0 {
			b.WriteByte(',')
		}

		fmt.Fprintf(&b, `{"status":"new","n":%d,"note":"collection %d"}`, j, i)
	}

	b.WriteString(`]}`)

	return b.String()
}

func count(n, base int) string {
	if base < 0 {
		return "n/a"
	}

	return strconv.Itoa(n)
}

// cpu is the CPU time the daemon used so far (user and system).
func (d *daemon) cpu() time.Duration {
	if stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", d.cmd.Process.Pid)); err == nil {
		f := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+2:]))
		utime, _ := strconv.ParseInt(f[11], 10, 64)
		stime, _ := strconv.ParseInt(f[12], 10, 64)

		return time.Duration(utime+stime) * 10 * time.Millisecond
	}

	b, err := exec.Command("ps", "-o", "time=", "-p", strconv.Itoa(d.cmd.Process.Pid)).Output()
	check(err)

	return clock(strings.TrimSpace(string(b)))
}

// clock reads the time of ps: [[hh:]mm:]ss[.cc].
func clock(s string) time.Duration {
	var total float64

	for _, part := range strings.Split(s, ":") {
		v, _ := strconv.ParseFloat(part, 64)
		total = total*60 + v
	}

	return time.Duration(total * float64(time.Second))
}

// fds is how many files the daemon has open, or -1 when it cannot tell.
func (d *daemon) fds() int {
	pid := strconv.Itoa(d.cmd.Process.Pid)

	if entries, err := os.ReadDir("/proc/" + pid + "/fd"); err == nil {
		return len(entries)
	}

	b, err := exec.Command("lsof", "-p", pid).Output()
	if err != nil {
		return -1
	}

	return strings.Count(string(b), "\n") - 1
}

// blocksKB is the disk the files under dir hold: their blocks, which count
// what fallocate reserved past the size.
func blocksKB(dir string) int64 {
	total, _ := diskOf(dir)

	return total / 1024
}

// diskOf gives the blocks of the files under dir, and the part of them past
// each file's size, rounded to a block (the reserve, not data).
func diskOf(dir string) (blocks, reserve int64) {
	filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}

		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return nil
		}

		held := st.Blocks * 512
		blocks += held
		reserve += max(held-(info.Size()+4095)/4096*4096, 0)

		return nil
	})

	return blocks, reserve
}
