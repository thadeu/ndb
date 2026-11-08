package bench

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// idleWindow is how long the idle phase watches the daemon do nothing.
const idleWindow = 10 * time.Second

// idle: many small collections that stop writing, on a daemon of their own.
// What an idle collection costs is what the daemon holds for it at rest:
// disk (blocks, not sizes), CPU, RSS and open files.
func idle(dir string, n int) []string {
	fmt.Fprintf(out, "idle: %d collections of 10 docs, then %s at rest\n", n, idleWindow)

	d := &daemon{dir: filepath.Join(dir, "idle")}
	check(os.MkdirAll(d.dir, 0o755))
	d.start()

	defer d.stop()

	time.Sleep(time.Second)

	rss0, fds0 := d.rss(), d.fds()

	for i := range n {
		coll := fmt.Sprintf("/idle_%03d", i)

		d.must("PUT", coll, `{"indexes":["status"]}`)
		d.must("POST", coll+"/_bulk", idleDocs(i))
	}

	time.Sleep(3 * time.Second)

	cpu0 := d.cpu()
	time.Sleep(idleWindow)
	cpu := d.cpu() - cpu0

	disk := blocksKB(filepath.Join(d.dir, "data"))
	rss, fds := d.rss()-rss0, d.fds()-fds0
	pct := 100 * cpu.Seconds() / idleWindow.Seconds()

	record("idle.disk_kb", float64(disk)/float64(n))
	record("idle.cpu_pct", pct)
	record("idle.rss_mb", float64(rss))

	if fds0 >= 0 {
		record("idle.fds", float64(fds)/float64(n))
	}

	return []string{
		fmt.Sprintf("| idle collections | %d, 10 docs each |", n),
		fmt.Sprintf("| idle disk | %.1f MB, %d KB per collection |", float64(disk)/1024, disk/int64(n)),
		fmt.Sprintf("| idle CPU | %.1f%% of a core (%d ms in %s) |", pct, cpu.Milliseconds(), idleWindow),
		fmt.Sprintf("| idle RSS | +%d MB |", rss),
		fmt.Sprintf("| idle files | +%s open |", count(fds, fds0)),
	}
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
	var total int64

	filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}

		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			total += st.Blocks * 512
		}

		return nil
	})

	return total / 1024
}
