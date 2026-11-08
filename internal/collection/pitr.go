package collection

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/thadeu/ndb/internal/recovery"
	"github.com/thadeu/ndb/internal/wal"
)

// PITR snapshots (M17). A snapshot is the state of one checkpoint: hard
// links to its segment files (they never change), a copy of meta.bolt (the
// checkpoint and the live bits) and point.json. Taken under the commit lock,
// so no seal or compaction commits in between. With the WAL from the
// checkpoint position on, it rebuilds the collection at any later instant.
//
// The snapshots of a collection are under <data>/pitr/<unix ms>/.

// PITRDir is the dir of the snapshots under a collection dir.
const PITRDir = "pitr"

// PointFile describes a snapshot.
const PointFile = "point.json"

// Point is one snapshot: when it was taken, the WAL position its segments
// cover, and the last tx of the collection when it was taken (a later one
// means writes since).
type Point struct {
	At  time.Time         `json:"at"`
	Pos recovery.Position `json:"pos"`
	Tx  uint64            `json:"tx"`
}

// PITRSnapshot writes a snapshot of the collection under its pitr dir and
// returns it. A collection without segments gets a snapshot too: its
// meta.bolt is empty and the WAL holds everything.
func (c *Collection) PITRSnapshot(now time.Time) (Point, error) {
	s := c.seal

	if s == nil {
		return Point{}, errors.New("pitr: the collection has no segments dir")
	}

	root := filepath.Join(s.dir, PITRDir)
	final := filepath.Join(root, strconv.FormatInt(now.UnixMilli(), 10))
	tmp := final + ".tmp"

	if err := os.RemoveAll(tmp); err != nil {
		return Point{}, err
	}

	if err := os.MkdirAll(filepath.Join(tmp, segmentDir), 0o755); err != nil {
		return Point{}, err
	}

	last := c.LastTx()

	p, err := c.linkCheckpoint(tmp, now)
	p.Tx = last
	if err == nil {
		err = writePoint(tmp, p)
	}

	if err == nil {
		err = os.Rename(tmp, final)
	}

	if err != nil {
		os.RemoveAll(tmp)

		return Point{}, err
	}

	return p, syncPath(root)
}

// linkCheckpoint links the segment files of the current checkpoint into dst
// and copies meta.bolt, under the commit lock.
func (c *Collection) linkCheckpoint(dst string, now time.Time) (Point, error) {
	s := c.seal

	s.commitMu.Lock()
	defer s.commitMu.Unlock()

	cp, err := readCheckpoint(s.meta)
	if err != nil {
		return Point{}, err
	}

	keep := map[string]bool{}

	for _, name := range cp.Segments {
		keep[strings.TrimSuffix(name, ".parquet")] = true
	}

	paths, err := filepath.Glob(filepath.Join(s.dir, segmentDir, "*"))
	if err != nil {
		return Point{}, err
	}

	for _, path := range paths {
		base, _, _ := strings.Cut(filepath.Base(path), ".")

		if !keep[base] || strings.HasSuffix(path, ".tmp") {
			continue
		}

		if err := os.Link(path, filepath.Join(dst, segmentDir, filepath.Base(path))); err != nil {
			return Point{}, err
		}
	}

	err = s.meta.View(func(tx *bolt.Tx) error {
		return tx.CopyFile(filepath.Join(dst, metaFile), 0o644)
	})

	return Point{At: now.UTC(), Pos: cp.Pos}, err
}

func writePoint(dir string, p Point) error {
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}

	f, err := os.Create(filepath.Join(dir, PointFile))
	if err != nil {
		return err
	}

	_, err = f.Write(data)

	if err == nil {
		err = f.Sync()
	}

	if cerr := f.Close(); err == nil {
		err = cerr
	}

	return err
}

// Snap is a snapshot and its dir.
type Snap struct {
	Point
	Dir string
}

// Snaps lists the snapshots under a collection dir, oldest first. A dir that
// a crash left half written (.tmp) is not listed.
func Snaps(dir string) ([]Snap, error) {
	entries, err := os.ReadDir(filepath.Join(dir, PITRDir))
	if os.IsNotExist(err) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	var out []Snap

	for _, e := range entries {
		if !e.IsDir() || strings.HasSuffix(e.Name(), ".tmp") {
			continue
		}

		path := filepath.Join(dir, PITRDir, e.Name())

		data, err := os.ReadFile(filepath.Join(path, PointFile))
		if err != nil {
			continue
		}

		snap := Snap{Dir: path}

		if err := json.Unmarshal(data, &snap.Point); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}

		out = append(out, snap)
	}

	slices.SortFunc(out, func(a, b Snap) int { return a.At.Compare(b.At) })

	return out, nil
}

// pitrFloor is the lowest WAL file the oldest snapshot needs; ok is false
// without snapshots.
func (c *Collection) pitrFloor() (int, bool) {
	if c.seal == nil {
		return 0, false
	}

	snaps, err := Snaps(c.seal.dir)
	if err != nil || len(snaps) == 0 {
		return 0, false
	}

	floor := snaps[0].Pos.Seq

	for _, s := range snaps {
		floor = min(floor, s.Pos.Seq)
	}

	return max(floor, 1), true
}

func syncPath(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}

	defer f.Close()

	return f.Sync()
}

// ErrOutOfWindow: no snapshot and no full WAL reach back to the instant.
var ErrOutOfWindow = errors.New("out_of_window")

// maxPITRLine bounds one WAL line while a restore reads it.
const maxPITRLine = 64 << 20

// BuildPITR makes dst, a collection dir, hold the state of the collection
// dir src at until: the segments and meta.bolt of the newest snapshot at or
// before until, and the WAL records from its position up to until (by the
// record time). Opening dst replays them. src may be in use: its closed WAL
// files are linked, the rest is copied.
func BuildPITR(src, dst string, until time.Time) error {
	base, err := baseFor(src, until)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Join(dst, segmentDir), 0o755); err != nil {
		return err
	}

	if base.Dir != "" {
		if err := linkTree(filepath.Join(base.Dir, segmentDir), filepath.Join(dst, segmentDir)); err != nil {
			return err
		}

		if err := copyFile(filepath.Join(base.Dir, metaFile), filepath.Join(dst, metaFile), -1); err != nil {
			return err
		}
	}

	from := max(base.Pos.Seq, 1)

	if _, err := os.Stat(filepath.Join(src, walName(from))); err != nil {
		return fmt.Errorf("%w: the WAL file %s that the snapshot needs is gone", ErrOutOfWindow, walName(from))
	}

	return copyWAL(src, dst, from, until)
}

// baseFor is the newest snapshot at or before until. Without one, a WAL
// that starts at its first file holds the whole history: the base is empty.
func baseFor(src string, until time.Time) (Snap, error) {
	snaps, err := Snaps(src)
	if err != nil {
		return Snap{}, err
	}

	for i := len(snaps) - 1; i >= 0; i-- {
		if !snaps[i].At.After(until) {
			return snaps[i], nil
		}
	}

	if _, err := os.Stat(filepath.Join(src, walName(1))); err == nil {
		return Snap{}, nil
	}

	return Snap{}, fmt.Errorf("%w: no snapshot at or before %s", ErrOutOfWindow, until.UTC().Format(time.RFC3339))
}

// Oldest is the oldest instant that BuildPITR can rebuild from src: its
// first snapshot, or its first record when the WAL is whole.
func Oldest(src string) (time.Time, bool) {
	if _, err := os.Stat(filepath.Join(src, walName(1))); err == nil {
		_, ts, ok := firstRecordOf(filepath.Join(src, walName(1)))

		return ts, ok
	}

	snaps, err := Snaps(src)
	if err != nil || len(snaps) == 0 {
		return time.Time{}, false
	}

	return snaps[0].At, true
}

func walName(seq int) string {
	return fmt.Sprintf("%06d.ndjson", seq)
}

// copyWAL copies the WAL files of src from seq on into dst, up to the first
// record after until. A closed file with no such record is linked.
func copyWAL(src, dst string, from int, until time.Time) error {
	files, err := filepath.Glob(filepath.Join(src, "*.ndjson"))
	if err != nil {
		return err
	}

	for i, path := range files {
		var seq int

		if _, err := fmt.Sscanf(filepath.Base(path), "%06d.ndjson", &seq); err != nil || seq < from {
			continue
		}

		end, stop, err := cutAt(path, until)
		if err != nil {
			return err
		}

		out := filepath.Join(dst, filepath.Base(path))

		if !stop && i < len(files)-1 {
			err = os.Link(path, out)
		} else {
			err = copyFile(path, out, end)
		}

		if err != nil || stop {
			return err
		}
	}

	return nil
}

// cutAt is the end of the last whole record of the WAL file at or before
// until; stop tells that a later record (or a cut line) follows it.
func cutAt(path string, until time.Time) (int64, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false, err
	}

	defer f.Close()

	lr := wal.NewLineReader(io.NewSectionReader(f, 0, 1<<62), 0, maxPITRLine)

	var end int64

	for {
		line, _, err := lr.Next()

		if errors.Is(err, io.EOF) {
			return end, false, nil
		}

		if err != nil {
			return end, true, nil
		}

		rec, err := wal.Decode(line)
		if err != nil || rec.TS.After(until) {
			return end, true, nil
		}

		end = lr.Offset()
	}
}

func firstRecordOf(path string) (uint64, time.Time, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, time.Time{}, false
	}

	defer f.Close()

	line, _, err := wal.NewLineReader(io.NewSectionReader(f, 0, 1<<62), 0, maxPITRLine).Next()
	if err != nil {
		return 0, time.Time{}, false
	}

	rec, err := wal.Decode(line)
	if err != nil {
		return 0, time.Time{}, false
	}

	return rec.Tx, rec.TS, true
}

// linkTree hard links every file of src into dst.
func linkTree(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, e := range entries {
		if err := os.Link(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}

	return nil
}

// copyFile copies the first n bytes of src to dst (all of it with n < 0),
// and syncs dst.
func copyFile(src, dst string, n int64) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}

	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}

	var r io.Reader = in

	if n >= 0 {
		r = io.LimitReader(in, n)
	}

	_, err = io.Copy(out, r)

	if err == nil {
		err = out.Sync()
	}

	if cerr := out.Close(); err == nil {
		err = cerr
	}

	return err
}
