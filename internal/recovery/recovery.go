// Package recovery replays a collection WAL after a start or a crash.
//
// Rules (plan §8.4):
//   - the first bad line (cut, bad CRC) in the LAST segment is the real end
//     of the log: an unacked commit. The file is truncated there.
//   - a bad line in an older, sealed segment is real corruption: stop with
//     ErrCorruptSealed. Never drop sealed data in silence.
//   - tx numbers must grow by one; a gap is corruption. A truncate marker
//     may jump: it is the first record of a truncated collection, and its
//     tx follows the last tx before the truncate.
package recovery

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/thadeu/ndb/internal/fs"
	"github.com/thadeu/ndb/internal/wal"
)

var (
	ErrCorruptSealed = errors.New("recovery: corrupt line in a sealed WAL segment")
	ErrTxGap         = errors.New("recovery: gap in tx numbers")
)

// Position is a place in the WAL: the checkpoint says everything before it
// is already applied, and Tx is the last tx before it.
type Position struct {
	Seq    int
	Offset int64
	Tx     uint64
}

type Config struct {
	FS      fs.FS
	Dir     string
	MaxLine int
	From    Position
}

type Result struct {
	LastTx    uint64
	End       Position
	Truncated bool
}

// Replay calls apply for every record after cfg.From, in order.
func Replay(cfg Config, apply func(wal.Record)) (Result, error) {
	if err := removeTmp(cfg); err != nil {
		return Result{}, err
	}

	segs, err := segments(cfg.Dir)
	if err != nil {
		return Result{}, err
	}

	res := Result{LastTx: cfg.From.Tx, End: cfg.From}

	for i, seg := range segs {
		if seg.seq < cfg.From.Seq {
			continue
		}

		start := int64(0)

		if seg.seq == cfg.From.Seq {
			start = cfg.From.Offset
		}

		last := i == len(segs)-1

		if err := replaySegment(cfg, seg, start, last, &res, apply); err != nil {
			return res, err
		}
	}

	return res, nil
}

func replaySegment(cfg Config, seg segFile, start int64, last bool, res *Result, apply func(wal.Record)) error {
	f, err := cfg.FS.Open(seg.path)
	if err != nil {
		return err
	}

	defer f.Close()

	lr := wal.NewLineReader(io.NewSectionReader(f, start, 1<<62), start, cfg.MaxLine)

	for {
		line, off, err := lr.Next()

		if errors.Is(err, io.EOF) {
			res.End = Position{Seq: seg.seq, Offset: lr.Offset(), Tx: res.LastTx}

			return nil
		}

		var rec wal.Record

		if err == nil {
			rec, err = wal.Decode(line)
		}

		if err != nil {
			if !last {
				return fmt.Errorf("%w: %s offset %d: %v", ErrCorruptSealed, seg.path, off, err)
			}

			return truncate(cfg, seg, off, res)
		}

		if rec.Tx != res.LastTx+1 && !(rec.IsTruncate() && rec.Tx > res.LastTx) {
			return fmt.Errorf("%w: %s offset %d: tx %d after %d", ErrTxGap, seg.path, off, rec.Tx, res.LastTx)
		}

		apply(rec)

		res.LastTx = rec.Tx
	}
}

// truncate cuts the last segment at off, in place (never a rewrite), and
// syncs it.
func truncate(cfg Config, seg segFile, off int64, res *Result) error {
	f, err := cfg.FS.OpenAppend(seg.path)
	if err != nil {
		return err
	}

	defer f.Close()

	if err := f.Truncate(off); err != nil {
		return err
	}

	if err := f.Sync(); err != nil {
		return err
	}

	res.Truncated = true
	res.End = Position{Seq: seg.seq, Offset: off, Tx: res.LastTx}

	return nil
}

type segFile struct {
	seq  int
	path string
}

func segments(dir string) ([]segFile, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.ndjson"))
	if err != nil {
		return nil, err
	}

	segs := make([]segFile, 0, len(paths))

	for _, p := range paths {
		var seq int

		if _, err := fmt.Sscanf(filepath.Base(p), "%06d.ndjson", &seq); err != nil {
			return nil, fmt.Errorf("recovery: bad segment name %s", p)
		}

		segs = append(segs, segFile{seq: seq, path: p})
	}

	return segs, nil
}

func removeTmp(cfg Config) error {
	paths, err := filepath.Glob(filepath.Join(cfg.Dir, "*"))
	if err != nil {
		return err
	}

	for _, p := range paths {
		if strings.HasSuffix(p, ".tmp") {
			if err := cfg.FS.Remove(p); err != nil {
				return err
			}
		}
	}

	return nil
}
