// Package backup writes and reads the backup file: one NDJSON stream with
// the extension .ndb (or .ndb.gz when compressed).
//
// Layout, one JSON object per line:
//
//	{"ndb_backup":1,"version":"...","created_at":"...","collections":["users"]}
//	{"_section":"catalog","name":"users","definition":{...}}
//	{"_section":"cursor","collection":"users","consumer":"mailer","pos":"e1.3"}
//	{"_section":"wal","collection":"users"}
//	<WAL lines of the collection, byte for byte, each with its _crc>
//	{"_section":"stream","collection":"jobs","data":"<base64>"}
//	{"_section":"blob","ref":"sha256:...","type":"...","size":N,"data":"<base64>"}
//	{"_section":"end","lines":N,"crc":"<crc32 hex>"}
//
// The end line counts the lines before it and carries the CRC-32 (IEEE) of
// every byte before it, so a cut or damaged file is found before a restore
// swaps anything.
//
// Segments and meta.bolt are derived from the WAL and are not saved. A
// direct-mode collection keeps its data in the app's own files, so only its
// catalog line is saved. Watch positions (e<tx>.<n>) name a tx, not a WAL
// file, so they stay valid when the restore writes the WAL of a collection as
// one file. The group store of a stream (streams.bolt: its groups and
// their pending entries) is saved whole after the WAL of the stream.
package backup

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/thadeu/ndb/internal/blob"
	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/cursors"
	"github.com/thadeu/ndb/internal/fs"
	"github.com/thadeu/ndb/internal/segment"
	"github.com/thadeu/ndb/internal/wal"
)

// FormatVersion is the value of "ndb_backup" in the header line.
const FormatVersion = 1

// blobChunk is the raw size of one blob line. A blob over it is split into
// lines with "part" and "parts", so neither the backup nor the restore holds
// a whole blob in memory.
const blobChunk = 1 << 20

// maxLine bounds one line read from disk or from a backup file.
const maxLine = 1 << 27

// ErrNotBackup: the input does not start with an ndb backup header.
var ErrNotBackup = errors.New("not an ndb backup file (the first line is not a backup header)")

var disk = fs.OS{}

// Mark is the end of the WAL prefix to save: the file and the byte offset in
// it. Files after Seq are left out.
type Mark struct {
	Seq    int
	Offset int64
}

// Source is what Write saves.
type Source struct {
	Defs    []catalog.Def
	Cursors []cursors.Entry

	// WAL emits the lines of one engine-mode collection, without the final
	// newline. The line is valid until emit returns.
	WAL func(name string, emit func(line []byte) error) error

	// Snapshots lists the engine collections whose WAL no longer starts at
	// tx 1. They are saved as their live documents at one tx, and the restore
	// starts them from there.
	Snapshots map[string]Snapshot

	// Blobs is where blob content is read; nil saves no blob.
	Blobs *blob.Store

	// Streams gives a consistent copy of the group store of a stream, or
	// nil when it has none.
	Streams func(name string) ([]byte, error)
}

// Snapshot is the state of one collection at tx Tx.
type Snapshot struct {
	Tx uint64

	// Lines emits one WAL put line per live document, in insertion order.
	Lines func(emit func(line []byte) error) error
}

// Options of a backup.
type Options struct {
	// Version is the daemon version written in the header.
	Version string

	// NoBlobs leaves the blob content out. Documents keep their references.
	NoBlobs bool

	// Streams gives a consistent copy of the group store of a stream while
	// the daemon holds it open. Nil reads the file (a stopped daemon).
	Streams func(name string) ([]byte, error)
}

type lineWriter struct {
	bw    *bufio.Writer
	crc   hash.Hash32
	lines int
}

func (lw *lineWriter) line(b []byte) error {
	if _, err := lw.bw.Write(b); err != nil {
		return err
	}

	if err := lw.bw.WriteByte('\n'); err != nil {
		return err
	}

	lw.crc.Write(b)
	lw.crc.Write([]byte{'\n'})

	lw.lines++

	return nil
}

func (lw *lineWriter) section(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}

	return lw.line(b)
}

type header struct {
	NdbBackup   int      `json:"ndb_backup"`
	Version     string   `json:"version"`
	CreatedAt   string   `json:"created_at"`
	Collections []string `json:"collections"`
}

type catalogLine struct {
	Section    string      `json:"_section"`
	Name       string      `json:"name"`
	Definition catalog.Def `json:"definition"`
}

type cursorLine struct {
	Section    string `json:"_section"`
	Collection string `json:"collection"`
	Consumer   string `json:"consumer"`
	Pos        string `json:"pos"`
}

type snapshotLine struct {
	Section    string `json:"_section"`
	Collection string `json:"collection"`
	Tx         uint64 `json:"tx"`
}

type walLine struct {
	Section    string `json:"_section"`
	Collection string `json:"collection"`
}

type blobLine struct {
	Section string `json:"_section"`
	Ref     string `json:"ref"`
	Type    string `json:"type,omitempty"`
	Size    int64  `json:"size"`
	Part    int    `json:"part,omitempty"`
	Parts   int    `json:"parts,omitempty"`
	Data    string `json:"data"`
}

type streamLine struct {
	Section    string `json:"_section"`
	Collection string `json:"collection"`
	Data       string `json:"data"`
}

// StreamFile is the group store of a stream in its collection dir.
const StreamFile = "streams.bolt"

type endLine struct {
	Section string `json:"_section"`
	Lines   int    `json:"lines"`
	CRC     string `json:"crc"`
}

// Write saves src to w as one backup file.
func Write(w io.Writer, src Source, opts Options) error {
	lw := &lineWriter{bw: bufio.NewWriterSize(w, 256<<10), crc: crc32.NewIEEE()}

	names := make([]string, 0, len(src.Defs))

	for _, d := range src.Defs {
		names = append(names, d.Name)
	}

	err := lw.section(header{
		NdbBackup:   FormatVersion,
		Version:     opts.Version,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
		Collections: names,
	})
	if err != nil {
		return err
	}

	for _, d := range src.Defs {
		if err := lw.section(catalogLine{Section: "catalog", Name: d.Name, Definition: d}); err != nil {
			return err
		}
	}

	for _, c := range src.Cursors {
		if err := lw.section(cursorLine{Section: "cursor", Collection: c.Collection, Consumer: c.Consumer, Pos: c.Pos}); err != nil {
			return err
		}
	}

	refs := map[string]blob.Ref{}

	for _, d := range src.Defs {
		if d.Write == catalog.Direct {
			continue
		}

		write := writeWAL

		if _, ok := src.Snapshots[d.Name]; ok {
			write = writeSnapshot
		}

		if err := write(lw, src, d.Name, refs); err != nil {
			return fmt.Errorf("collection %s: %w", d.Name, err)
		}

		if err := writeStream(lw, src, d); err != nil {
			return fmt.Errorf("stream %s: %w", d.Name, err)
		}
	}

	if src.Blobs != nil && !opts.NoBlobs {
		if err := writeBlobs(lw, src.Blobs, refs); err != nil {
			return err
		}
	}

	return finish(lw)
}

// writeStream saves the group store of a stream.
func writeStream(lw *lineWriter, src Source, d catalog.Def) error {
	if !d.Stream || src.Streams == nil {
		return nil
	}

	data, err := src.Streams(d.Name)
	if err != nil || data == nil {
		return err
	}

	return lw.section(streamLine{Section: "stream", Collection: d.Name, Data: base64.StdEncoding.EncodeToString(data)})
}

// writeSnapshot saves the state at one tx, then an empty wal section: no
// WAL line is after the snapshot tx.
func writeSnapshot(lw *lineWriter, src Source, name string, refs map[string]blob.Ref) error {
	snap := src.Snapshots[name]

	if err := lw.section(snapshotLine{Section: "snapshot", Collection: name, Tx: snap.Tx}); err != nil {
		return err
	}

	err := snap.Lines(func(line []byte) error {
		for _, ref := range blob.RefInfos(line) {
			refs[ref.Blob] = ref
		}

		return lw.line(line)
	})
	if err != nil {
		return err
	}

	return lw.section(walLine{Section: "wal", Collection: name})
}

func writeWAL(lw *lineWriter, src Source, name string, refs map[string]blob.Ref) error {
	if err := lw.section(walLine{Section: "wal", Collection: name}); err != nil {
		return err
	}

	return src.WAL(name, func(line []byte) error {
		for _, ref := range blob.RefInfos(line) {
			refs[ref.Blob] = ref
		}

		return lw.line(line)
	})
}

// writeBlobs saves every blob that a saved WAL line refers to. A blob that
// is gone (a collected one that only old, overwritten versions named) is
// skipped.
func writeBlobs(lw *lineWriter, store *blob.Store, refs map[string]blob.Ref) error {
	keys := make([]string, 0, len(refs))

	for k := range refs {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	for _, k := range keys {
		err := writeBlob(lw, store, refs[k])

		if errors.Is(err, blob.ErrNotFound) {
			continue
		}

		if err != nil {
			return fmt.Errorf("blob %s: %w", k, err)
		}
	}

	return nil
}

func writeBlob(lw *lineWriter, store *blob.Store, ref blob.Ref) error {
	r, err := store.Get(ref.Blob)
	if err != nil {
		return err
	}

	defer r.Close()

	size := r.Size()
	parts := max(1, int((size+blobChunk-1)/blobChunk))
	buf := make([]byte, blobChunk)

	for part := 1; part <= parts; part++ {
		n, err := io.ReadFull(r, buf)
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
			return err
		}

		line := blobLine{Section: "blob", Ref: ref.Blob, Type: ref.Type, Size: size, Data: base64.StdEncoding.EncodeToString(buf[:n])}

		if parts > 1 {
			line.Part, line.Parts = part, parts
		}

		if err := lw.section(line); err != nil {
			return err
		}
	}

	return nil
}

func finish(lw *lineWriter) error {
	sum := lw.crc.Sum32()

	end, err := json.Marshal(endLine{Section: "end", Lines: lw.lines, CRC: fmt.Sprintf("%08x", sum)})
	if err != nil {
		return err
	}

	if _, err := lw.bw.Write(append(end, '\n')); err != nil {
		return err
	}

	return lw.bw.Flush()
}

// ScanWAL emits the lines of the WAL files in dir, in order. With end set it
// stops at that mark (the saved prefix); without it, a cut or damaged tail of
// the last file is the end of the log, as in recovery. It checks each CRC and
// that tx numbers start at 1 and grow by one.
func ScanWAL(dir string, end *Mark, emit func(line []byte) error) error {
	seqs, err := walSeqs(dir)
	if err != nil {
		return err
	}

	var lastTx uint64

	for i, seq := range seqs {
		if end != nil && seq > end.Seq {
			break
		}

		limit := int64(-1)

		if end != nil && seq == end.Seq {
			limit = end.Offset
		}

		isLast := i == len(seqs)-1 || (end != nil && seq == end.Seq)
		tolerateTail := end == nil && isLast

		path := filepath.Join(dir, fmt.Sprintf("%06d.ndjson", seq))

		lastTx, err = scanFile(path, limit, tolerateTail, lastTx, emit)
		if err != nil {
			return err
		}
	}

	return nil
}

func walSeqs(dir string) ([]int, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	var seqs []int

	for _, e := range entries {
		var seq int

		if _, err := fmt.Sscanf(e.Name(), "%06d.ndjson", &seq); err == nil {
			seqs = append(seqs, seq)
		}
	}

	sort.Ints(seqs)

	return seqs, nil
}

func scanFile(path string, limit int64, tolerateTail bool, lastTx uint64, emit func([]byte) error) (uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return lastTx, err
	}

	defer f.Close()

	var in io.Reader = f

	if limit >= 0 {
		in = io.LimitReader(f, limit)
	}

	lr := wal.NewLineReader(in, 0, maxLine)

	for {
		line, off, err := lr.Next()

		if errors.Is(err, io.EOF) {
			return lastTx, nil
		}

		var rec wal.Record

		if err == nil {
			rec, err = wal.Decode(line)
		}

		if err != nil && tolerateTail {
			return lastTx, nil
		}

		if err != nil {
			return lastTx, fmt.Errorf("%s offset %d: %w", path, off, err)
		}

		if rec.Tx != lastTx+1 {
			return lastTx, fmt.Errorf("%s offset %d: tx %d after %d; the WAL must be whole to be saved (wal_retention removed older files?)", path, off, rec.Tx, lastTx)
		}

		lastTx = rec.Tx

		if err := emit(line); err != nil {
			return lastTx, err
		}
	}
}

// FirstTx is the tx of the first WAL record in dir; ok is false when there
// is none.
func FirstTx(dir string) (tx uint64, ok bool, err error) {
	seqs, err := walSeqs(dir)
	if err != nil {
		return 0, false, err
	}

	for _, seq := range seqs {
		f, err := os.Open(filepath.Join(dir, fmt.Sprintf("%06d.ndjson", seq)))
		if err != nil {
			return 0, false, err
		}

		line, _, err := wal.NewLineReader(f, 0, maxLine).Next()
		f.Close()

		if err != nil {
			continue
		}

		rec, err := wal.Decode(line)
		if err != nil {
			return 0, false, fmt.Errorf("%s: %w", dir, err)
		}

		return rec.Tx, true, nil
	}

	return 0, false, nil
}

// Extract reads a backup file from r (plain or gzip, found by the magic
// bytes) into stage, a new dir laid out as a data dir. It checks the header,
// every WAL line CRC and the end line. A cut or damaged file gives an error;
// the caller removes stage.
func Extract(r io.Reader, stage string) error {
	in, err := openInput(r)
	if err != nil {
		return err
	}

	x := &extractor{stage: stage, crc: crc32.NewIEEE(), seen: map[string]bool{}, defs: map[string]catalog.Def{}}

	defer x.close()

	return x.run(wal.NewLineReader(in, 0, maxLine))
}

func openInput(r io.Reader) (io.Reader, error) {
	br := bufio.NewReaderSize(r, 64<<10)

	magic, _ := br.Peek(2)

	if len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		return gzip.NewReader(br)
	}

	return br, nil
}

type extractor struct {
	stage string
	crc   hash.Hash32
	lines int

	names map[string]bool
	seen  map[string]bool

	cat   *catalog.Catalog
	cur   *cursors.Store
	blobs *blob.Store

	walFile *os.File
	walOut  *bufio.Writer
	inWAL   bool
	lastTx  uint64

	defs map[string]catalog.Def
	snap *snapState

	pending *pendingBlob
}

// snapState is a snapshot section being read: its rows wait in memory until
// the wal section after it, then become the first segment of the collection.
type snapState struct {
	name string
	tx   uint64
	rows []segment.Row
	ids  map[string]bool
}

type pendingBlob struct {
	ref   string
	typ   string
	size  int64
	parts int
	next  int
	tmp   *os.File
}

func (x *extractor) close() {
	x.endWAL()

	if x.cat != nil {
		x.cat.Close()
	}

	if x.cur != nil {
		x.cur.Close()
	}

	if x.pending != nil {
		x.pending.tmp.Close()
		os.Remove(x.pending.tmp.Name())
	}
}

func (x *extractor) run(lr *wal.LineReader) error {
	if err := disk.MkdirAll(x.stage); err != nil {
		return err
	}

	first, err := x.read(lr)
	if err != nil {
		return err
	}

	x.count(first)

	if err := x.header(first); err != nil {
		return err
	}

	for {
		line, err := x.read(lr)
		if err != nil {
			return err
		}

		sec, ok := sectionOf(line)

		if ok && sec == "end" {
			return x.end(lr, line)
		}

		x.count(line)

		if !ok {
			if err := x.record(line); err != nil {
				return err
			}

			continue
		}

		if err := x.section(sec, line); err != nil {
			return err
		}
	}
}

// read returns the next line. A cut input is an error: the end line is the
// only valid way to finish.
func (x *extractor) read(lr *wal.LineReader) ([]byte, error) {
	line, off, err := lr.Next()

	if errors.Is(err, io.EOF) {
		return nil, errors.New("the backup is cut: the end line is missing")
	}

	if errors.Is(err, wal.ErrPartialLine) {
		return nil, fmt.Errorf("the backup is cut in the middle of line %d (offset %d)", x.lines+1, off)
	}

	if err != nil {
		return nil, fmt.Errorf("line %d: %w", x.lines+1, err)
	}

	return line, nil
}

// count feeds a line to the running CRC and the line counter.
func (x *extractor) count(line []byte) {
	x.crc.Write(line)
	x.crc.Write([]byte{'\n'})

	x.lines++
}

func sectionOf(line []byte) (string, bool) {
	rest, ok := bytes.CutPrefix(line, []byte(`{"_section":"`))
	if !ok {
		return "", false
	}

	name, _, ok := bytes.Cut(rest, []byte(`"`))

	return string(name), ok
}

func (x *extractor) header(line []byte) error {
	var h header

	if err := json.Unmarshal(line, &h); err != nil || h.NdbBackup == 0 {
		return ErrNotBackup
	}

	if h.NdbBackup != FormatVersion {
		return fmt.Errorf("backup format %d is not supported (this ndb reads %d)", h.NdbBackup, FormatVersion)
	}

	x.names = map[string]bool{}

	for _, n := range h.Collections {
		if !safeName(n) {
			return fmt.Errorf("header: bad collection name %q", n)
		}

		x.names[n] = true
	}

	var err error

	if x.cat, err = catalog.Open(filepath.Join(x.stage, "catalog.bolt"), catalog.Options{}); err != nil {
		return err
	}

	if x.cur, err = cursors.Open(filepath.Join(x.stage, "cursors.bolt")); err != nil {
		return err
	}

	x.blobs, err = blob.Open(disk, filepath.Join(x.stage, "blobs"))

	return err
}

func safeName(n string) bool {
	return n != "" && n != "." && n != ".." && filepath.Base(n) == n && filepath.ToSlash(n) == n
}

func (x *extractor) section(sec string, line []byte) error {
	x.endWAL()

	if x.snap != nil && sec != "wal" {
		return fmt.Errorf("line %d: a snapshot must be followed by its wal section", x.lines)
	}

	switch sec {
	case "catalog":
		return x.catalogLine(line)
	case "cursor":
		return x.cursorLine(line)
	case "snapshot":
		return x.snapshotStart(line)
	case "wal":
		return x.walStart(line)
	case "blob":
		return x.blobLine(line)
	case "stream":
		return x.streamStore(line)
	}

	return fmt.Errorf("line %d: unknown section %q", x.lines, sec)
}

func (x *extractor) catalogLine(line []byte) error {
	var c catalogLine

	if err := json.Unmarshal(line, &c); err != nil {
		return fmt.Errorf("line %d: %w", x.lines, err)
	}

	if !x.names[c.Name] || c.Definition.Name != c.Name {
		return fmt.Errorf("line %d: catalog entry %q is not in the header", x.lines, c.Name)
	}

	x.defs[c.Name] = c.Definition

	return x.cat.Put(c.Definition)
}

// streamStore writes the group store of a stream into its dir.
func (x *extractor) streamStore(line []byte) error {
	var sl streamLine

	if err := json.Unmarshal(line, &sl); err != nil {
		return fmt.Errorf("line %d: %w", x.lines, err)
	}

	if !x.names[sl.Collection] {
		return fmt.Errorf("line %d: stream section of %q is not in the header", x.lines, sl.Collection)
	}

	data, err := base64.StdEncoding.DecodeString(sl.Data)
	if err != nil {
		return fmt.Errorf("line %d: %w", x.lines, err)
	}

	dir := filepath.Join(x.stage, "collections", sl.Collection)

	if err := disk.MkdirAll(dir); err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(dir, StreamFile), data, 0o644)
}

func (x *extractor) cursorLine(line []byte) error {
	var c cursorLine

	if err := json.Unmarshal(line, &c); err != nil {
		return fmt.Errorf("line %d: %w", x.lines, err)
	}

	return x.cur.Update(c.Collection, c.Consumer, func(string, bool) (string, error) { return c.Pos, nil })
}

func (x *extractor) walStart(line []byte) error {
	var w walLine

	if err := json.Unmarshal(line, &w); err != nil {
		return fmt.Errorf("line %d: %w", x.lines, err)
	}

	if !x.names[w.Collection] || x.seen[w.Collection] {
		return fmt.Errorf("line %d: wal section of %q is unknown or repeated", x.lines, w.Collection)
	}

	x.seen[w.Collection] = true

	dir := filepath.Join(x.stage, "collections", w.Collection)
	base := uint64(0)

	if x.snap != nil {
		if x.snap.name != w.Collection {
			return fmt.Errorf("line %d: the wal section is not the one of the snapshot", x.lines)
		}

		if err := collection.WriteSnapshot(disk, dir, x.defs[w.Collection], x.snap.tx, x.snap.rows); err != nil {
			return err
		}

		base, x.snap = x.snap.tx, nil
	}

	if err := disk.MkdirAll(dir); err != nil {
		return err
	}

	f, err := os.Create(filepath.Join(dir, "000000.ndjson"))
	if err != nil {
		return err
	}

	x.walFile = f
	x.walOut = bufio.NewWriterSize(x.walFile, 256<<10)
	x.inWAL = true
	x.lastTx = base

	return nil
}

func (x *extractor) snapshotStart(line []byte) error {
	var sl snapshotLine

	if err := json.Unmarshal(line, &sl); err != nil {
		return fmt.Errorf("line %d: %w", x.lines, err)
	}

	if !x.names[sl.Collection] || x.seen[sl.Collection] || sl.Tx == 0 {
		return fmt.Errorf("line %d: snapshot of %q is unknown, repeated or at tx 0", x.lines, sl.Collection)
	}

	x.snap = &snapState{name: sl.Collection, tx: sl.Tx, ids: map[string]bool{}}

	return nil
}

// record handles a line that is not a section: a document of the snapshot,
// or a WAL record.
func (x *extractor) record(line []byte) error {
	if x.snap == nil {
		return x.walRecord(line)
	}

	rec, err := wal.Decode(line)
	if err != nil {
		return fmt.Errorf("line %d: %w", x.lines, err)
	}

	if len(rec.Ops) != 1 || rec.Ops[0].Kind != wal.Put || rec.Tx > x.snap.tx || x.snap.ids[rec.Ops[0].ID] {
		return fmt.Errorf("line %d: not a document of the snapshot", x.lines)
	}

	op := rec.Ops[0]

	x.snap.ids[op.ID] = true
	x.snap.rows = append(x.snap.rows, segment.Row{
		ID:  op.ID,
		V:   op.V,
		Seq: uint64(len(x.snap.rows) + 1),
		Tx:  rec.Tx,
		Raw: append([]byte(nil), op.Doc...),
	})

	return nil
}

func (x *extractor) endWAL() {
	if !x.inWAL {
		return
	}

	x.inWAL = false

	x.walOut.Flush()
	x.walFile.Sync()
	x.walFile.Close()
}

func (x *extractor) walRecord(line []byte) error {
	if !x.inWAL {
		return fmt.Errorf("line %d: a WAL line outside a wal section", x.lines)
	}

	rec, err := wal.Decode(line)
	if err != nil {
		return fmt.Errorf("line %d: %w", x.lines, err)
	}

	if rec.Tx != x.lastTx+1 {
		return fmt.Errorf("line %d: tx %d after %d", x.lines, rec.Tx, x.lastTx)
	}

	x.lastTx = rec.Tx

	if _, err := x.walOut.Write(line); err != nil {
		return err
	}

	return x.walOut.WriteByte('\n')
}

func (x *extractor) blobLine(line []byte) error {
	var b blobLine

	if err := json.Unmarshal(line, &b); err != nil {
		return fmt.Errorf("line %d: %w", x.lines, err)
	}

	data, err := base64.StdEncoding.DecodeString(b.Data)
	if err != nil {
		return fmt.Errorf("line %d: blob data: %w", x.lines, err)
	}

	if b.Parts <= 1 {
		return x.storeBlob(b, bytes.NewReader(data))
	}

	return x.blobPart(b, data)
}

func (x *extractor) blobPart(b blobLine, data []byte) error {
	p := x.pending

	if b.Part == 1 {
		tmp, err := os.CreateTemp(x.stage, ".blob-part-*")
		if err != nil {
			return err
		}

		p = &pendingBlob{ref: b.Ref, typ: b.Type, size: b.Size, parts: b.Parts, next: 1, tmp: tmp}
		x.pending = p
	}

	if p == nil || p.ref != b.Ref || p.next != b.Part || p.parts != b.Parts {
		return fmt.Errorf("line %d: blob part %d of %s is out of order", x.lines, b.Part, b.Ref)
	}

	if _, err := p.tmp.Write(data); err != nil {
		return err
	}

	p.next++

	if b.Part < b.Parts {
		return nil
	}

	x.pending = nil

	defer os.Remove(p.tmp.Name())
	defer p.tmp.Close()

	if _, err := p.tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}

	return x.storeBlob(blobLine{Ref: p.ref, Type: p.typ, Size: p.size}, p.tmp)
}

// storeBlob puts the content in the stage blob store and checks that it has
// the hash and the size that the line names.
func (x *extractor) storeBlob(b blobLine, r io.Reader) error {
	got, err := x.blobs.Put(r, b.Type, 0)
	if err != nil {
		return err
	}

	if got.Blob != b.Ref || got.Size != b.Size {
		return fmt.Errorf("line %d: blob content does not match %s", x.lines, b.Ref)
	}

	return nil
}

// end checks the end line against what was read, and that nothing follows.
func (x *extractor) end(lr *wal.LineReader, line []byte) error {
	var e endLine

	if err := json.Unmarshal(line, &e); err != nil {
		return fmt.Errorf("end line: %w", err)
	}

	if x.pending != nil || x.snap != nil {
		return errors.New("the backup ends inside a split blob or a snapshot")
	}

	if e.Lines != x.lines {
		return fmt.Errorf("the backup is damaged: the end line counts %d lines, %d were read", e.Lines, x.lines)
	}

	if want := fmt.Sprintf("%08x", x.crc.Sum32()); e.CRC != want {
		return fmt.Errorf("the backup is damaged: crc is %s, the end line says %s", want, e.CRC)
	}

	if _, _, err := lr.Next(); !errors.Is(err, io.EOF) {
		return errors.New("the backup has data after the end line")
	}

	return nil
}
