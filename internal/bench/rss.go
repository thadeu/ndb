package bench

import (
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// rss is the daemon memory in MB.
func (d *daemon) rss() int64 {
	return d.rssKB() / 1024
}

// rssKB is the daemon memory in KB. On macOS it is the physical footprint
// (what Activity Monitor shows): the RSS of ps there still counts the pages
// that Go gave back with madvise until the system takes them, so after a
// load it stays near its peak (127 MB of RSS for a footprint of 35 MB in the
// idle part). On Linux the RSS of ps drops when Go gives memory back.
func (d *daemon) rssKB() int64 {
	pid := strconv.Itoa(d.cmd.Process.Pid)

	if runtime.GOOS == "darwin" {
		if kb, ok := footprintKB(pid); ok {
			return kb
		}
	}

	out, err := exec.Command("ps", "-o", "rss=", "-p", pid).Output()

	if err != nil {
		return 0
	}

	kb, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)

	return kb
}

var footprintLine = regexp.MustCompile(`phys_footprint:\s+([0-9.]+)\s*(B|KB|MB|GB)`)

var footprintUnit = map[string]float64{"B": 1.0 / 1024, "KB": 1, "MB": 1024, "GB": 1024 * 1024}

// footprintKB reads phys_footprint from footprint(1), about 10 ms a call.
func footprintKB(pid string) (int64, bool) {
	out, err := exec.Command("footprint", "-p", pid).Output()

	if err != nil {
		return 0, false
	}

	return parseFootprint(string(out))
}

func parseFootprint(out string) (int64, bool) {
	m := footprintLine.FindStringSubmatch(out)

	if m == nil {
		return 0, false
	}

	n, err := strconv.ParseFloat(m[1], 64)

	if err != nil {
		return 0, false
	}

	return int64(n * footprintUnit[m[2]]), true
}
