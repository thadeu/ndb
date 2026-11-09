package bench

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// Direct mode: the app writes NDJSON files and ndb only reads them (write:
// direct). Two runs, each on a daemon of its own:
//
//  1. cold: the files are complete before ndb starts; ndb reads them all
//     before it answers, so the start time is the load time;
//  2. live: ndb starts on empty folders, then the app appends the cdrs in
//     64 KiB writes (lines cut in two between writes) and starts a new
//     file every 100k lines; the catch-up is how long ndb takes, after the
//     last write, to have every line.
//
// Each run checks that every line is a document (the count, and 1000 ids
// read back). The reads then run on the cold daemon.

const (
	directChunk   = 64 << 10
	directPerFile = 100_000
	directSample  = 1000
)

var directStatuses = []string{"answered", "answered", "answered", "busy", "failed", "no_answer"}

func directPlan(n int) []planned {
	sec := func(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

	return []planned{
		{"direct: write files", sec(float64(n) / 250_000)},
		{"direct: start and read the files", sec(float64(n) / 40_000)},
		{"direct reads", sec(5)},
		{"direct: append live", sec(float64(n) / 150_000)},
		{"direct: catch up", sec(5)},
	}
}

func direct(dir string, n int) []string {
	users := max(n/10, 1)
	lines := directCold(dir, n, users)

	return append(lines, directLive(dir, n, users)...)
}

// directCold writes the files, then starts ndb on them.
func directCold(dir string, n, users int) []string {
	base := filepath.Join(dir, "direct-cold")

	var written atomic.Int64

	prog.step("direct: write files", func() string { return fmt.Sprintf("%d of %d lines", written.Load(), n+users) })

	writeUsers(filepath.Join(base, "files", "users"), users)
	written.Add(int64(users))

	cdrs := filepath.Join(base, "files", "cdrs")
	check(os.MkdirAll(cdrs, 0o755))

	for f, from := 0, 0; from < n; f, from = f+1, from+directPerFile {
		to := min(from+directPerFile, n)
		path := filepath.Join(cdrs, fmt.Sprintf("cdrs-%03d.ndjson", f))

		check(os.WriteFile(path, cdrLines(from, to, users), 0o644))
		written.Add(int64(to - from))
	}

	prog.end(fmt.Sprintf("%d cdrs and %d users, %s", n, users, du(filepath.Join(base, "files"))))

	d := directDaemon(base)

	prog.step("direct: start and read the files", nil)

	start := time.Now()
	d.start()
	took := time.Since(start)

	missing := d.directMissing(n, users)

	prog.end(fmt.Sprintf("%.1f s, %.0f lines/s, %d missing", took.Seconds(), float64(n+users)/took.Seconds(), missing))
	record("direct.cold_missing", float64(missing))

	lines := []string{
		fmt.Sprintf("| direct cold start | %d cdrs and %d users read in %.1f s (%.0f lines/s), %d missing |", n, users, took.Seconds(), float64(n+users)/took.Seconds(), missing),
	}

	lines = append(lines, timed(d, "direct reads", directProbes())...)

	d.stop()

	return lines
}

// directLive starts ndb on the users only, then appends the cdrs.
func directLive(dir string, n, users int) []string {
	base := filepath.Join(dir, "direct-live")

	writeUsers(filepath.Join(base, "files", "users"), users)

	cdrs := filepath.Join(base, "files", "cdrs")
	check(os.MkdirAll(cdrs, 0o755))

	d := directDaemon(base)
	d.start()

	defer d.stop()

	var appended atomic.Int64

	prog.step("direct: append live", func() string { return fmt.Sprintf("%d of %d lines", appended.Load(), n) })

	start := time.Now()

	for f, from := 0, 0; from < n; f, from = f+1, from+directPerFile {
		to := min(from+directPerFile, n)

		appendChunked(filepath.Join(cdrs, fmt.Sprintf("cdrs-%03d.ndjson", f)), cdrLines(from, to, users))
		appended.Add(int64(to - from))
	}

	wrote := time.Since(start)

	prog.end(fmt.Sprintf("%d lines in %.1f s", n, wrote.Seconds()))

	var have atomic.Int64

	prog.step("direct: catch up", func() string { return fmt.Sprintf("%d of %d", have.Load(), n) })

	caught := time.Now()

	for time.Since(caught) < 2*time.Minute {
		have.Store(int64(d.count("/cdrs")))

		if have.Load() >= int64(n) {
			break
		}

		time.Sleep(100 * time.Millisecond)
	}

	catchUp := time.Since(caught)
	missing := d.directMissing(n, users)

	prog.end(fmt.Sprintf("%.1f s after the last write, %d missing", catchUp.Seconds(), missing))
	record("direct.live_missing", float64(missing))
	record("direct.live_catchup_ms", float64(catchUp.Milliseconds()))

	return []string{
		fmt.Sprintf("| direct live | %d cdrs appended in %.1f s (64 KiB writes, a new file every %dk lines); every line in %.1f s after the last write, %d missing |",
			n, wrote.Seconds(), directPerFile/1000, catchUp.Seconds(), missing),
	}
}

// directDaemon is a daemon whose ndb.yml declares the two direct
// collections of base/files.
func directDaemon(base string) *daemon {
	files, err := filepath.Abs(filepath.Join(base, "files"))
	check(err)

	conf := fmt.Sprintf(`collections:
  users:
    write: direct
    path: %s/users/*.ndjson
    indexes: [plan]
  cdrs:
    write: direct
    path: %s/cdrs/*.ndjson
    time: started_at
    indexes: [user_id, status]
`, files, files)

	check(os.WriteFile(filepath.Join(base, "ndb.yml"), []byte(conf), 0o644))

	return &daemon{dir: base, ready: 10 * time.Minute}
}

// directMissing is how many lines are not documents: the cdrs and users
// short of their counts, and the sampled cdr ids that are not found.
func (d *daemon) directMissing(n, users int) int {
	missing := max(n-d.count("/cdrs"), 0) + max(users-d.count("/users"), 0)
	r := rand.New(rand.NewPCG(5, 5))

	for range directSample {
		if code, _, _ := d.call("GET", fmt.Sprintf("/cdrs/_doc/cdr-%07d", r.IntN(n)), nil); code != 200 {
			missing++
		}
	}

	return missing
}

func (d *daemon) count(coll string) int {
	var c struct {
		Count int `json:"count"`
	}

	code, body, _ := d.call("POST", coll+"/_count", []byte("{}"))
	if code != 200 || json.Unmarshal(body, &c) != nil {
		return 0
	}

	return c.Count
}

func writeUsers(dir string, users int) {
	check(os.MkdirAll(dir, 0o755))

	var b strings.Builder

	plans := []string{"free", "pro", "team"}

	for i := range users {
		fmt.Fprintf(&b, `{"_id":"user-%06d","name":"User %d","plan":%q,"city":"city-%02d"}`+"\n", i, i, plans[i%3], i%40)
	}

	check(os.WriteFile(filepath.Join(dir, "users.ndjson"), []byte(b.String()), 0o644))
}

// cdrLines are the cdrs from to to, in time order over yesterday.
func cdrLines(from, to, users int) []byte {
	r := rand.New(rand.NewPCG(uint64(from), 11))
	day := yesterday()

	var b strings.Builder

	for i := from; i < to; i++ {
		at := day.Add(time.Duration(float64(i) / float64(directPerFile*10) * float64(24*time.Hour)))
		status := directStatuses[r.IntN(len(directStatuses))]
		dur := 0

		if status == "answered" {
			dur = 5 + r.IntN(900)
		}

		fmt.Fprintf(&b, `{"_id":"cdr-%07d","user_id":"user-%06d","status":%q,"duration":%d,"started_at":%q,"caller":"+5511%08d","callee":"+55%010d","cost":%.4f}`+"\n",
			i, r.IntN(users), status, dur, at.Format(time.RFC3339), r.IntN(1e8), r.IntN(1e9), float64(dur)*0.0012)
	}

	return []byte(b.String())
}

// appendChunked appends data to path in directChunk writes, the way an app
// that buffers its output does: a line may end in the next write.
func appendChunked(path string, data []byte) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	check(err)

	w := bufio.NewWriterSize(f, directChunk)

	for len(data) > 0 {
		k := min(directChunk, len(data))

		_, err := w.Write(data[:k])
		check(err)
		check(w.Flush())

		data = data[k:]
	}

	check(f.Close())
}

// directProbes are the reads of the cdrs and their users.
func directProbes() []probe {
	return []probe{
		{"get a cdr by id", "GET", "/cdrs/_doc/cdr-0000123", ""},
		{"last 20 cdrs of a user (indexed user_id)", "POST", "/cdrs/_search", `{"query":{"user_id":"user-000042"},"sort":{"started_at":-1},"limit":20}`},
		{"count of the answered cdrs (indexed status)", "POST", "/cdrs/_count", `{"query":{"status":"answered"}}`},
		{"count of all cdrs", "POST", "/cdrs/_count", `{}`},
		{"talk time per user, top 10", "POST", "/cdrs/_aggregate", `{"pipeline":[{"$match":{"status":"answered"}},{"$group":{"_id":"$user_id","total":{"$sum":"$duration"}}},{"$sort":{"total":-1}},{"$limit":10}]}`},
		{"100 failed cdrs with their user (belongs_to)", "POST", "/cdrs/_search", `{"query":{"status":"failed"},"limit":100,"lookup":{"user":{"belongs_to":"users","fk":"user_id"}}}`},
		{"20 pro users with their last 5 cdrs (has_many)", "POST", "/users/_search", `{"query":{"plan":"pro"},"limit":20,"lookup":{"cdrs":{"has_many":"cdrs","fk":"user_id","sort":{"started_at":-1},"limit":5}}}`},
	}
}
