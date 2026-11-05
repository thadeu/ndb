package tailer

import (
	"encoding/json"
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
}

func newQuarantine(fsys fs.FS, dir, name string) *quarantine {
	return &quarantine{fs: fsys, dir: filepath.Join(dir, "quarantine"), path: filepath.Join(dir, "quarantine", name+".ndjson")}
}

type quarantineRow struct {
	TS        string `json:"ts"`
	File      string `json:"file"`
	Offset    int64  `json:"offset"`
	Reason    string `json:"reason"`
	Line      string `json:"line"`
	Truncated bool   `json:"truncated,omitempty"`
}

// add writes one row. line is cut to its first 4 KiB. A failure to write
// the row is returned, but the line still counts.
func (q *quarantine) add(file string, off int64, reason string, line []byte, whole bool) error {
	q.count.Add(1)

	row := quarantineRow{
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

func (q *quarantine) close() error {
	if q.f == nil {
		return nil
	}

	return q.f.Close()
}
