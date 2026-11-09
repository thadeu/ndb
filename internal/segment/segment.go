// Package segment holds the immutable columnar files that a sealed memtable
// becomes (plan §7.3, §7.4). Each row keeps its document id, version, source
// transaction, tombstone flag and the full raw document.
package segment

import (
	"bufio"
	"io"
	"path/filepath"

	"github.com/thadeu/ndb/internal/fs"
)

// Row is one document, or one tombstone (Del), as stored in a segment.
type Row struct {
	ID  string
	V   uint64
	Seq uint64
	Tx  uint64
	Del bool
	Raw []byte
}

// Column is one query column: its name and its type (string, int, float,
// bool, json, or list<scalar>). Mixed is set when other kinds of the same
// path went to _rest: the column then does not hold every value of the path,
// so its zone map and bloom filter cannot prove absence.
type Column struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Mixed bool   `json:"mixed,omitempty"`
}

// Info describes an encoded segment. The collection stores it in meta.bolt.
type Info struct {
	Rows    int      `json:"rows"`
	Columns []Column `json:"columns"`
}

// Writer encodes rows into the bytes of one segment file. indexed names the
// columns that get a bloom filter.
type Writer interface {
	Encode(rows []Row, indexed []string) ([]byte, Info, error)
}

// Reader reads one segment file. Entries reads the metadata of every row;
// Raw loads one document. The rest are the column reads of a scan (scan.go).
type Reader interface {
	Len() int
	Entries(fn func(row int, r Row) error) error
	Raw(row int) ([]byte, error)
	Close() error

	Groups() []Group
	ColumnOf(path string) (Column, bool)
	Zone(group int, path string) (Zone, bool)
	Bloom(group int, path string, v Scalar) (may, known bool)
	ReadGroup(group int, paths []string) (GroupRows, error)
	ReadCells(group int, paths []string, keep func(row int) bool) (GroupRows, error)
}

func NewWriter() Writer {
	return parquetWriter{}
}

// Open opens the segment file at path. Close releases the file.
func Open(fsys fs.FS, path string) (Reader, error) {
	f, err := fsys.Open(path)
	if err != nil {
		return nil, err
	}

	info, err := f.Stat()
	if err != nil {
		f.Close()

		return nil, err
	}

	r, err := decode(f, info.Size())
	if err != nil {
		f.Close()

		return nil, err
	}

	r.closer = f

	return r, nil
}

// Decode reads a segment from memory or any io.ReaderAt.
func Decode(ra io.ReaderAt, size int64) (Reader, error) {
	return decode(ra, size)
}

// Publish writes data to path atomically: tmp file, fsync, rename, fsync of
// the directory (plan §8.2). A failed write leaves only a tmp file, which
// the next start removes.
func Publish(fsys fs.FS, path string, data []byte) error {
	return publish(fsys, path, func(w io.Writer) error {
		_, err := w.Write(data)

		return err
	})
}

// PublishRows encodes rows straight into the file at path, atomically like
// Publish. The segment is never held in memory: only one row group is.
func PublishRows(fsys fs.FS, path string, rows Rows, indexed []string) (Info, error) {
	var info Info

	err := publish(fsys, path, func(w io.Writer) error {
		bw := bufio.NewWriterSize(w, 1<<20)

		var err error

		if info, err = encodeTo(bw, rows, indexed); err != nil {
			return err
		}

		return bw.Flush()
	})

	return info, err
}

func publish(fsys fs.FS, path string, write func(io.Writer) error) error {
	tmp := path + ".tmp"

	f, err := fsys.Create(tmp)
	if err != nil {
		return err
	}

	if err := write(f); err != nil {
		f.Close()

		return err
	}

	if err := f.Sync(); err != nil {
		f.Close()

		return err
	}

	if err := f.Close(); err != nil {
		return err
	}

	if err := fsys.Rename(tmp, path); err != nil {
		return err
	}

	return fsys.SyncDir(filepath.Dir(path))
}
