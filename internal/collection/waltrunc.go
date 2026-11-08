package collection

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/thadeu/ndb/internal/blob"
	"github.com/thadeu/ndb/internal/feed"
	"github.com/thadeu/ndb/internal/wal"
)

// WAL truncation (plan §7.1, §8.3, §10.4). With wal_retention set, a WAL file
// may be deleted when all of these hold:
//
//  1. it is sealed: the checkpoint in meta.bolt (durable) is past it, so a
//     restart never reads it;
//  2. no watch consumer needs it: every stored cursor, and every stream that
//     reads the WAL now, is at a tx after the last tx of the file;
//  3. it is older than wal_retention, by the _ts of the first record of the
//     next file (the moment it was closed);
//  4. no PITR snapshot needs it: it is before the WAL position of the
//     oldest snapshot (pitr.go).
//
// The files go in order from the oldest and the walk stops at the first file
// that stays, so the kept files are always contiguous. The active (last) file
// stays. Without wal_retention nothing is ever deleted.

// firstRecord reads the tx and time of the first record of a WAL file. ok is
// false for an empty file.
func (f *engineFeed) firstRecord(path string) (uint64, time.Time, bool) {
	file, err := f.fs.Open(path)
	if err != nil {
		return 0, time.Time{}, false
	}

	defer file.Close()

	line, _, err := wal.NewLineReader(io.NewSectionReader(file, 0, 1<<62), 0, f.maxLine).Next()
	if err != nil {
		return 0, time.Time{}, false
	}

	rec, err := wal.Decode(line)
	if err != nil {
		return 0, time.Time{}, false
	}

	return rec.Tx, rec.TS, true
}

func (f *engineFeed) files() ([]string, error) {
	return filepath.Glob(filepath.Join(f.dir, "*.ndjson"))
}

// firstKept is the first tx of the oldest WAL file (1 when nothing was
// deleted). ok is false when it is unknown: no file, or an empty one.
func (f *engineFeed) firstKept() (uint64, bool) {
	files, err := f.files()
	if err != nil || len(files) == 0 {
		return 0, false
	}

	tx, _, ok := f.firstRecord(files[0])

	return tx, ok
}

// pinHistory checks that the WAL still holds the history after from, and
// pins it while the stream reads it. The returned func releases the pin.
func (f *engineFeed) pinHistory(from enginePos) (func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	first, ok := f.firstKept()

	// A WAL that begins right after the checkpoint tx keeps that tx for a
	// consumer that has read it all.
	if ok && f.base > 0 && first == f.base+1 {
		first = f.base
	}

	if !ok {
		first = f.base
	}

	if first > 1 && from.tx < first && from.n < nowOps {
		return nil, fmt.Errorf("%w: the oldest kept tx is %d", feed.ErrHistoryTruncated, first)
	}

	if f.pins == nil {
		f.pins = map[int]uint64{}
	}

	id := f.nextPin
	f.nextPin++

	f.pins[id] = max(from.tx, 1)

	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()

		delete(f.pins, id)
	}, nil
}

// TruncateWAL deletes the WAL files that rules 1 to 3 above allow, and
// returns how many. It does nothing without wal_retention or without
// segments (nothing is sealed then).
func (c *Collection) TruncateWAL() (int, error) {
	if c.walKeep <= 0 || c.seal == nil {
		return 0, nil
	}

	f := c.feed

	files, err := f.files()
	if err != nil || len(files) < 2 {
		return 0, err
	}

	cp, err := readCheckpoint(c.seal.meta)
	if err != nil {
		return 0, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	floor, err := c.needFloor()
	if err != nil {
		return 0, err
	}

	sealSeq := cp.Pos.Seq

	if seq, ok := c.pitrFloor(); ok {
		sealSeq = min(sealSeq, seq)
	}

	deleted := 0

	for i := 0; i+1 < len(files); i++ {
		if !c.deletable(files[i], files[i+1], sealSeq, cp.Pos.Tx, floor) {
			break
		}

		if err := f.fs.Remove(files[i]); err != nil {
			return deleted, err
		}

		deleted++
	}

	if deleted == 0 {
		return 0, nil
	}

	return deleted, f.fs.SyncDir(f.dir)
}

// deletable tells whether file may go, given the file after it. sealSeq and
// sealTx are the checkpoint WAL position; floor is the lowest tx a reader
// still needs.
func (c *Collection) deletable(file, next string, sealSeq int, sealTx, floor uint64) bool {
	var seq int

	if _, err := fmt.Sscanf(filepath.Base(file), "%06d.ndjson", &seq); err != nil || seq >= sealSeq {
		return false
	}

	nextTx, nextTS, ok := c.feed.firstRecord(next)

	switch {
	case !ok:
		return false
	case nextTx-1 > sealTx:
		return false
	case nextTx > floor:
		return false
	}

	return c.clock().Sub(nextTS) >= c.walKeep
}

// needFloor is the lowest tx that a stored cursor or an open stream still
// needs; max uint64 when none. The caller holds feed.mu. A consumer that has
// a stored cursor "" and never acked wants the whole history: it holds back
// the deletion, unless the history is gone already (it cannot be served).
func (c *Collection) needFloor() (uint64, error) {
	f := c.feed
	floor := ^uint64(0)

	for _, tx := range f.pins {
		floor = min(floor, tx)
	}

	if c.cursors == nil {
		return floor, nil
	}

	tokens, err := c.cursors()
	if err != nil {
		return 0, err
	}

	first, known := f.firstKept()

	for _, tok := range tokens {
		pos, err := parseEnginePos(tok)
		if err != nil {
			continue
		}

		if tok == "" && known && first > 1 {
			continue
		}

		floor = min(floor, max(pos.tx, 1))
	}

	return floor, nil
}

// KeepsWAL reports that wal_retention is set: the kept WAL files are history
// that watch replays read, and the blob GC must treat them as live.
func (c *Collection) KeepsWAL() bool {
	return c.walKeep > 0
}

// WALBlobRefs lists the blob hashes that the kept WAL files refer to. With
// truncation on, a watch replay can still read these records, so the blob GC
// must treat them as live.
func (c *Collection) WALBlobRefs() ([]string, error) {
	return c.feed.blobRefs()
}

// blobRefs scans every kept WAL file. A file that a truncation removes
// meanwhile is skipped: its records are not readable any more either.
func (f *engineFeed) blobRefs() ([]string, error) {
	files, err := f.files()
	if err != nil {
		return nil, err
	}

	var out []string

	for _, path := range files {
		refs, err := f.fileBlobRefs(path)
		if err != nil {
			return nil, err
		}

		out = append(out, refs...)
	}

	return out, nil
}

func (f *engineFeed) fileBlobRefs(path string) ([]string, error) {
	file, err := f.fs.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	defer file.Close()

	var out []string

	lr := wal.NewLineReader(io.NewSectionReader(file, 0, 1<<62), 0, f.maxLine)

	for {
		line, _, err := lr.Next()

		if errors.Is(err, io.EOF) || errors.Is(err, wal.ErrPartialLine) {
			return out, nil
		}

		if err != nil {
			return nil, err
		}

		out = append(out, blob.Refs(line)...)
	}
}
