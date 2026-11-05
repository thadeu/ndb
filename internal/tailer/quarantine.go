package tailer

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/thadeu/ndb/internal/fs"
)

const quarantineLineMax = 4096

// quarantine keeps the lines ndb could not apply, with where they were.
type quarantine struct {
	fs    fs.FS
	path  string
	dir   string
	f     fs.File
	count atomic.Int64
	seen  map[quarantineKey]struct{}
}

// quarantineKey is the identity of a quarantined line: where it sits.
type quarantineKey struct {
	file string
	off  int64
}

func newQuarantine(fsys fs.FS, dir, name string) *quarantine {
	q := &quarantine{fs: fsys, dir: filepath.Join(dir, "quarantine"), path: filepath.Join(dir, "quarantine", name+".ndjson")}
	q.load()

	return q
}

// load reads the keys already in the file, so a rebuild from offset 0 does
// not append the same line again. count starts at the distinct lines held.
func (q *quarantine) load() {
	q.seen = map[quarantineKey]struct{}{}

	rows, _, err := q.rows(-1)
	if err != nil {
		return
	}

	for _, r := range rows {
		q.seen[quarantineKey{r.File, r.Offset}] = struct{}{}
	}

	q.count.Store(int64(len(q.seen)))
}

// QuarantineRow is one line that ndb could not apply: where it was, why, and
// its first 4 KiB.
type QuarantineRow struct {
	TS        string `json:"ts"`
	File      string `json:"file"`
	Offset    int64  `json:"offset"`
	Reason    string `json:"reason"`
	Line      string `json:"line"`
	Truncated bool   `json:"truncated,omitempty"`
}

// add writes one row. line is cut to its first 4 KiB. A line already held
// (same file and offset) is not written again and does not count again. A
// failure to write the row is returned, but the line still counts.
func (q *quarantine) add(file string, off int64, reason string, line []byte, whole bool) error {
	key := quarantineKey{file, off}

	if _, dup := q.seen[key]; dup {
		return nil
	}

	q.seen[key] = struct{}{}
	q.count.Add(1)

	row := QuarantineRow{
		TS:     time.Now().UTC().Format(time.RFC3339Nano),
		File:   file,
		Offset: off,
		Reason: reason,
	}

	if len(line) > quarantineLineMax {
		line = line[:quarantineLineMax]
		whole = false
	}

	row.Line = string(line)
	row.Truncated = !whole

	data, err := json.Marshal(row)
	if err != nil {
		return err
	}

	if q.f == nil {
		if err := q.fs.MkdirAll(q.dir); err != nil {
			return err
		}

		if q.f, err = q.fs.OpenAppend(q.path); err != nil {
			return err
		}
	}

	_, err = q.f.Write(append(data, '\n'))

	return err
}

// rows reads the quarantine file. A line that was quarantined twice (a direct
// collection rebuilds from offset 0 at each start) counts once, at its last
// row. It returns the last limit rows, oldest first, and the distinct total.
func (q *quarantine) rows(limit int) ([]QuarantineRow, int, error) {
	f, err := q.fs.Open(q.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}

	if err != nil {
		return nil, 0, err
	}

	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}

	sc := bufio.NewScanner(io.NewSectionReader(readerAt{f}, 0, info.Size()))

	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)

	type key = quarantineKey

	var all []QuarantineRow

	last := map[key]int{}

	for sc.Scan() {
		var row QuarantineRow

		if json.Unmarshal(sc.Bytes(), &row) != nil {
			continue
		}

		last[key{row.File, row.Offset}] = len(all)
		all = append(all, row)
	}

	if err := sc.Err(); err != nil {
		return nil, 0, err
	}

	var out []QuarantineRow

	for i, row := range all {
		if last[key{row.File, row.Offset}] == i {
			out = append(out, row)
		}
	}

	total := len(out)

	if limit >= 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}

	return out, total, nil
}

// readerAt adapts a fs.File, which has ReadAt but no Read, to io.ReaderAt.
type readerAt struct{ f fs.File }

func (r readerAt) ReadAt(p []byte, off int64) (int, error) { return r.f.ReadAt(p, off) }

func (q *quarantine) close() error {
	if q.f == nil {
		return nil
	}

	return q.f.Close()
}
