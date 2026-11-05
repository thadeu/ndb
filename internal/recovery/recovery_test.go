package recovery_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/fs"
	"github.com/thadeu/ndb/internal/recovery"
	"github.com/thadeu/ndb/internal/wal"
)

func line(t *testing.T, tx uint64) []byte {
	t.Helper()

	l, err := wal.Encode(nil, wal.Record{Tx: tx, TS: time.Unix(0, 0), Ops: []wal.Op{
		{Kind: wal.Put, ID: fmt.Sprint("id", tx), V: 1, Doc: json.RawMessage(`{"x":1}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}

	return append(l, '\n')
}

func segment(t *testing.T, dir string, seq int, content ...[]byte) string {
	t.Helper()

	path := filepath.Join(dir, fmt.Sprintf("%06d.ndjson", seq))

	var all []byte

	for _, c := range content {
		all = append(all, c...)
	}

	if err := os.WriteFile(path, all, 0o644); err != nil {
		t.Fatal(err)
	}

	return path
}

func replay(t *testing.T, dir string) ([]uint64, recovery.Result, error) {
	t.Helper()

	var txs []uint64

	res, err := recovery.Replay(recovery.Config{FS: fs.OS{}, Dir: dir, MaxLine: 1 << 20}, func(rec wal.Record) {
		txs = append(txs, rec.Tx)
	})

	return txs, res, err
}

func TestReplayEmptyDir(t *testing.T) {
	txs, res, err := replay(t, t.TempDir())

	if err != nil || len(txs) != 0 || res.LastTx != 0 {
		t.Fatalf("txs=%v res=%+v err=%v", txs, res, err)
	}
}

func TestReplayAllSegmentsInOrder(t *testing.T) {
	dir := t.TempDir()

	segment(t, dir, 1, line(t, 1), line(t, 2))
	segment(t, dir, 2, line(t, 3))

	txs, res, err := replay(t, dir)
	if err != nil {
		t.Fatal(err)
	}

	if fmt.Sprint(txs) != "[1 2 3]" || res.LastTx != 3 {
		t.Fatalf("txs=%v res=%+v", txs, res)
	}
}

func TestReplayTruncatesCutLastLine(t *testing.T) {
	dir := t.TempDir()
	good := append(line(t, 1), line(t, 2)...)
	cut := line(t, 3)[:20]

	path := segment(t, dir, 1, good, cut)

	txs, res, err := replay(t, dir)
	if err != nil {
		t.Fatal(err)
	}

	if fmt.Sprint(txs) != "[1 2]" || !res.Truncated {
		t.Fatalf("txs=%v res=%+v", txs, res)
	}

	data, _ := os.ReadFile(path)

	if string(data) != string(good) {
		t.Fatalf("file not truncated to the last good line: %q", data)
	}
}

func TestReplayTruncatesBadCRCInLastSegment(t *testing.T) {
	dir := t.TempDir()
	bad := line(t, 2)
	bad[10] ^= 0xff

	path := segment(t, dir, 1, line(t, 1), bad, line(t, 3))

	txs, _, err := replay(t, dir)
	if err != nil {
		t.Fatal(err)
	}

	if fmt.Sprint(txs) != "[1]" {
		t.Fatalf("txs=%v", txs)
	}

	data, _ := os.ReadFile(path)

	if string(data) != string(line(t, 1)) {
		t.Fatalf("file not truncated at the bad line")
	}
}

func TestReplayFailsOnDamageInSealedSegment(t *testing.T) {
	dir := t.TempDir()
	bad := line(t, 2)
	bad[10] ^= 0xff

	segment(t, dir, 1, line(t, 1), bad)
	segment(t, dir, 2, line(t, 3))

	if _, _, err := replay(t, dir); !errors.Is(err, recovery.ErrCorruptSealed) {
		t.Fatalf("want ErrCorruptSealed, got %v", err)
	}
}

func TestReplayFailsOnTxGap(t *testing.T) {
	dir := t.TempDir()

	segment(t, dir, 1, line(t, 1), line(t, 3))

	if _, _, err := replay(t, dir); !errors.Is(err, recovery.ErrTxGap) {
		t.Fatalf("want ErrTxGap, got %v", err)
	}
}

func TestReplayRemovesTmpFiles(t *testing.T) {
	dir := t.TempDir()
	tmp := filepath.Join(dir, "segment.parquet.tmp")

	os.WriteFile(tmp, []byte("x"), 0o644)

	if _, _, err := replay(t, dir); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatal("tmp file not removed")
	}
}

func TestReplayFromPosition(t *testing.T) {
	dir := t.TempDir()
	first := line(t, 1)

	segment(t, dir, 1, first, line(t, 2))
	segment(t, dir, 2, line(t, 3))

	var txs []uint64

	res, err := recovery.Replay(recovery.Config{
		FS: fs.OS{}, Dir: dir, MaxLine: 1 << 20,
		From: recovery.Position{Seq: 1, Offset: int64(len(first)), Tx: 1},
	}, func(rec wal.Record) { txs = append(txs, rec.Tx) })
	if err != nil {
		t.Fatal(err)
	}

	if fmt.Sprint(txs) != "[2 3]" || res.LastTx != 3 {
		t.Fatalf("txs=%v res=%+v", txs, res)
	}
}
