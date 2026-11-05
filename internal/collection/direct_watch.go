package collection

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"

	"github.com/thadeu/ndb/internal/feed"
	"github.com/thadeu/ndb/internal/wal"
)

// directFeed is the change feed of a direct-mode collection.
//
// A direct memtable is rebuilt from the files at every start, in file order,
// so an arrival-order sequence would change between runs. The position is
// instead derived from the data: (file, offset of the line, ops of that line
// delivered). It is the same after a restart. The stored position of a
// consumer is a vector, one such pair per file; an ack moves only the file of
// its event, so an event is never skipped, at worst redelivered.
//
// The history is read again from the files. A file that was deleted or
// replaced loses its history for a consumer that was behind.
type directFeed struct {
	d       *Direct
	buf     int
	maxLine int

	hub feed.Hub[directEv]

	// ends is the last applied position per file; guarded by the hub lock.
	ends map[string]dpos
}

// dpos means: the first n ops of the line at off are delivered.
type dpos struct {
	Off int64 `json:"o"`
	N   int   `json:"n"`
}

func (p dpos) after(o dpos) bool {
	return p.Off > o.Off || (p.Off == o.Off && p.N > o.N)
}

type directEv struct {
	file string
	pos  dpos
	op   wal.Op
}

type dtoken struct {
	F string `json:"f"`
	dpos
}

func (t dtoken) encode() string {
	raw, _ := json.Marshal(t)

	return base64.RawURLEncoding.EncodeToString(raw)
}

func parseDtoken(s string) (dtoken, error) {
	var t dtoken

	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || json.Unmarshal(raw, &t) != nil || t.F == "" || t.N < 1 {
		return t, feed.ErrBadPos
	}

	return t, nil
}

func parseVector(s string) (map[string]dpos, error) {
	vec := map[string]dpos{}

	if s == "" {
		return vec, nil
	}

	if err := json.Unmarshal([]byte(s), &vec); err != nil {
		return nil, feed.ErrBadPos
	}

	return vec, nil
}

func encodeVector(vec map[string]dpos) string {
	raw, _ := json.Marshal(vec)

	return string(raw)
}

// publish runs in Apply, with the lock of the memtable held.
func (f *directFeed) publish(file string, off int64, ops []wal.Op) {
	for i, op := range ops {
		ev := directEv{file: file, pos: dpos{Off: off, N: i + 1}, op: op}

		f.hub.Publish(ev, func() { f.ends[file] = ev.pos })
	}
}

// Feed is the change feed of the collection.
func (d *Direct) Feed() feed.Source {
	return d.feed
}

func (f *directFeed) Now() string {
	var snap map[string]dpos

	f.hub.Subscribe(1, func() { snap = copyEnds(f.ends) }).Close()

	return encodeVector(snap)
}

func copyEnds(m map[string]dpos) map[string]dpos {
	out := make(map[string]dpos, len(m))

	for k, v := range m {
		out[k] = v
	}

	return out
}

func (f *directFeed) Merge(old, ack string) (string, error) {
	t, err := parseDtoken(ack)
	if err != nil {
		return "", err
	}

	vec, err := parseVector(old)
	if err != nil {
		return "", err
	}

	if !t.dpos.after(vec[t.F]) {
		return old, nil
	}

	vec[t.F] = t.dpos

	return encodeVector(vec), nil
}

// Open subscribes first and re-reads the files second, up to the end of
// each file at subscribe time. Later lines come from the subscription.
func (f *directFeed) Open(start string) (feed.Stream, error) {
	vec, err := parseVector(start)
	if err != nil {
		return nil, err
	}

	var snap map[string]dpos

	sub := f.hub.Subscribe(f.buf, func() { snap = copyEnds(f.ends) })

	files := make([]string, 0, len(snap))

	for name := range snap {
		files = append(files, name)
	}

	sort.Strings(files)

	return &directStream{f: f, sub: sub, vec: vec, snap: snap, files: files}, nil
}

type directStream struct {
	f    *directFeed
	sub  *feed.Sub[directEv]
	vec  map[string]dpos
	snap map[string]dpos

	files []string
	file  *os.File
	lr    *wal.LineReader
	queue []feed.Event
}

func (s *directStream) Close() {
	s.sub.Close()
	s.closeFile()
}

func (s *directStream) closeFile() {
	if s.file != nil {
		s.file.Close()

		s.file, s.lr = nil, nil
	}
}

func (s *directStream) Next(ctx context.Context) (feed.Event, error) {
	for len(s.queue) == 0 {
		if err := s.fill(ctx); err != nil {
			return feed.Event{}, err
		}
	}

	ev := s.queue[0]
	s.queue = s.queue[1:]

	return ev, nil
}

func (s *directStream) fill(ctx context.Context) error {
	if len(s.files) > 0 || s.lr != nil {
		return s.fillFromFile()
	}

	select {
	case ev, ok := <-s.sub.C:
		if !ok {
			return s.ended()
		}

		s.emit(ev.file, ev.pos, ev.op)

		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *directStream) ended() error {
	if s.sub.Lagged() {
		return feed.ErrLagged
	}

	return io.EOF
}

// emit queues the event unless the consumer already has it.
func (s *directStream) emit(file string, pos dpos, op wal.Op) {
	if !pos.after(s.vec[file]) {
		return
	}

	s.queue = append(s.queue, feed.Event{
		Pos: dtoken{F: file, dpos: pos}.encode(),
		Op:  string(op.Kind),
		ID:  op.ID,
		Doc: op.Doc,
	})
}

// fillFromFile reads one more line of the file being replayed. It moves to
// the next file at the snapshot end of this one.
func (s *directStream) fillFromFile() error {
	if s.lr == nil {
		if err := s.openNext(); err != nil {
			return err
		}

		return nil
	}

	name := s.files[0]
	line, off, err := s.lr.Next()

	if err != nil && !errors.Is(err, wal.ErrLineTooLarge) || off > s.snap[name].Off {
		s.closeFile()

		s.files = s.files[1:]

		return nil
	}

	if err != nil {
		return nil
	}

	ops, perr := s.f.d.parse(name, off, line)
	if perr != nil {
		return nil
	}

	for i, op := range ops {
		pos := dpos{Off: off, N: i + 1}

		if pos.after(s.snap[name]) {
			break
		}

		s.emit(name, pos, op)
	}

	return nil
}

// openNext opens the first file of the list at the offset of the consumer.
// A file that is gone is skipped.
func (s *directStream) openNext() error {
	name := s.files[0]

	f, err := os.Open(name)
	if err != nil {
		s.files = s.files[1:]

		if errors.Is(err, os.ErrNotExist) {
			return nil
		}

		return err
	}

	start := s.vec[name].Off

	s.file = f
	s.lr = wal.NewLineReader(io.NewSectionReader(f, start, 1<<62), start, s.f.maxLine)

	return nil
}

// Subscribers is the number of open watch streams.
func (d *Direct) Subscribers() int {
	return d.feed.hub.Len()
}
