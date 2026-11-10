package streams

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/query"
)

// Service runs the streams of the daemon. One stream is one engine
// collection with "stream": true and one group store. Every change of a
// stream holds its lock: the ID generator and the group state change in one
// order.
type Service struct {
	db  func() *engine.DB
	now func() time.Time

	mu      sync.Mutex
	streams map[string]*stream

	// held are the names that a truncate, a drop or a rename holds (Hold):
	// an open of one waits until the channel is closed.
	held map[string]chan struct{}

	// shut is set by Close: no store opens after it.
	shut bool
}

type stream struct {
	mu   sync.Mutex
	name string
	col  *engine.Collection
	st   *bolt.DB
	now  func() time.Time

	// closed is set when the stream leaves the map (Hold, Close, or a new
	// collection under the name). A caller that finds it set opens again.
	closed bool

	last       ID
	added      int64
	maxDeleted ID

	// wake is closed by an append; a blocked read waits on it.
	wake chan struct{}

	// The appends that wait for the next insert (group commit): while one
	// insert runs its fsync, the next appends queue, and one of them takes
	// the whole queue into one insert.
	qmu      sync.Mutex
	queue    []*appendReq
	flushing bool
}

// appendReq is one append in the queue of a stream.
type appendReq struct {
	docs  []map[string]any
	idArg string
	ids   []ID
	err   error
	done  chan struct{}
}

func New(db func() *engine.DB) *Service {
	return &Service{db: db, now: time.Now, streams: map[string]*stream{}, held: map[string]chan struct{}{}}
}

// errClosed tells that the stream closed while the caller used it: the
// caller opens it again. It does not go out of the package.
var errClosed = errors.New("streams: the stream closed, open it again")

// errShut is the answer after Close.
var errShut = errors.New("streams: the daemon is closing")

// Errors of the streams contract.
func errNotStream(name string) error {
	return fmt.Errorf("%w: %s is a collection, not a stream", catalog.ErrNotStream, name)
}

func errTooSmall(id, last ID) error {
	return query.NewError("stream_id_too_small", "%s is not greater than the last ID %s", id, last)
}

// errExhausted: the last ID is the highest ID, so no entry can come after
// it (Redis: "The stream has exhausted the last possible ID").
func errExhausted() error {
	return query.NewError("stream_id_too_small", "the stream has exhausted the last possible ID %s", Max)
}

func errGroup(g string) error {
	return query.NewError("unknown_group", "no group %q", g)
}

func errGroupExists(g string) error {
	return query.NewError("group_exists", "group %q exists", g)
}

func badRequest(format string, args ...any) error {
	return query.NewError("invalid_request", format, args...)
}

// open gives the stream, made when create is true and it does not exist.
// The caller holds nothing; the stream comes locked. It waits while a
// truncate, a drop or a rename holds the name.
func (s *Service) open(ctx context.Context, name string, create bool) (*stream, error) {
	for {
		st, err := s.openOnce(ctx, name, create)

		if !errors.Is(err, errClosed) {
			return st, err
		}
	}
}

// openOnce is one try of open: errClosed when the name is held or the
// stream closed before it got the lock.
func (s *Service) openOnce(ctx context.Context, name string, create bool) (*stream, error) {
	if err := s.waitHeld(ctx, name); err != nil {
		return nil, err
	}

	db := s.db()

	def, ok := db.Definition(name)

	switch {
	case ok && !def.Stream:
		return nil, errNotStream(name)
	case !ok && !create:
		return nil, fmt.Errorf("%w: %s", catalog.ErrUnknown, name)
	case !ok:
		var err error

		if def, err = db.Ensure(catalog.Def{Name: name, Stream: true}); err != nil {
			return nil, err
		}
	}

	col, err := db.Get(name)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()

	if s.shut {
		s.mu.Unlock()

		return nil, errShut
	}

	if s.held[name] != nil {
		s.mu.Unlock()

		return nil, errClosed
	}

	st := s.streams[name]

	if st != nil && st.col != col {
		st.mu.Lock()
		st.shutDown()
		st.mu.Unlock()

		st = nil
	}

	if st == nil {
		st = &stream{name: name, col: col, now: s.now, wake: make(chan struct{})}
		s.streams[name] = st
	}

	s.mu.Unlock()

	st.mu.Lock()

	if st.closed {
		st.mu.Unlock()

		return nil, errClosed
	}

	if st.st == nil {
		if err := st.load(ctx, def.Durability == catalog.Relaxed); err != nil {
			st.mu.Unlock()

			return nil, err
		}
	}

	return st, nil
}

// waitHeld waits while a truncate, a drop or a rename holds name.
func (s *Service) waitHeld(ctx context.Context, name string) error {
	s.mu.Lock()
	ch := s.held[name]
	s.mu.Unlock()

	if ch == nil {
		return nil
	}

	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// load opens the group store and finds the last ID: the highest of the one
// stored and the one in the data; entries_added is the stored checkpoint
// plus the entries after it.
func (st *stream) load(ctx context.Context, relaxed bool) error {
	db, err := openStore(filepath.Join(st.col.Dir(), StoreFile), relaxed)
	if err != nil {
		return err
	}

	var cpLast ID

	var cpAdded int64

	err = db.View(func(tx *bolt.Tx) error {
		m := tx.Bucket(bMeta)
		st.last, st.maxDeleted = getID(m, "last"), getID(m, "max_deleted")
		cpLast, cpAdded = getID(m, "cp_last"), getInt(m, "cp_added", 0)

		return nil
	})
	if err != nil {
		db.Close()

		return err
	}

	st.st = db

	if top, ok, err := st.edge(ctx, -1); err != nil {
		return err
	} else if ok && st.last.Less(top) {
		st.last = top
	}

	after, err := st.count(ctx, map[string]any{"_id": map[string]any{"$gt": cpLast.Key()}})
	if err != nil {
		return err
	}

	st.added = cpAdded + int64(after)

	return nil
}

// checkpoint stores the last ID and entries_added, in a group store
// transaction.
func (st *stream) checkpoint(tx *bolt.Tx) error {
	m := tx.Bucket(bMeta)

	if err := putID(m, "last", st.last); err != nil {
		return err
	}

	if err := putID(m, "max_deleted", st.maxDeleted); err != nil {
		return err
	}

	if err := putID(m, "cp_last", st.last); err != nil {
		return err
	}

	return putInt(m, "cp_added", st.added)
}

func (st *stream) close() {
	if st.st != nil {
		_ = st.st.Update(st.checkpoint)
		_ = st.st.Close()
		st.st = nil
	}
}

// shutDown closes the store and marks the stream closed; the caller holds
// st.mu. A blocked read wakes and opens the stream again.
func (st *stream) shutDown() {
	st.close()
	st.closed = true

	close(st.wake)
	st.wake = make(chan struct{})
}

// Hold closes the streams of names and keeps them closed until release: an
// open of one of them waits. A truncate, a drop and a rename hold their
// names while the engine changes the collection dir, so no request opens
// the old group store in that time. With reset, release deletes the group
// store first: a truncated stream has no groups.
func (s *Service) Hold(names ...string) (release func(reset bool)) {
	names = slices.Clone(names)
	slices.Sort(names)
	names = slices.Compact(names)

	s.mu.Lock()

	var open []*stream

	// The names are held in sorted order, so two holds never wait on each
	// other.
	for _, name := range names {
		for s.held[name] != nil {
			ch := s.held[name]

			s.mu.Unlock()
			<-ch
			s.mu.Lock()
		}

		s.held[name] = make(chan struct{})

		if st := s.streams[name]; st != nil {
			open = append(open, st)

			delete(s.streams, name)
		}
	}

	s.mu.Unlock()

	for _, st := range open {
		st.mu.Lock()
		st.shutDown()
		st.mu.Unlock()
	}

	return func(reset bool) {
		if reset {
			s.removeStores(names)
		}

		s.mu.Lock()

		for _, name := range names {
			close(s.held[name])
			delete(s.held, name)
		}

		s.mu.Unlock()
	}
}

// removeStores deletes the group store of each name, if it has one.
func (s *Service) removeStores(names []string) {
	db := s.db()

	for _, name := range names {
		col, err := db.Get(name)

		if err != nil {
			continue
		}

		_ = os.Remove(filepath.Join(col.Dir(), StoreFile))
	}
}

// Close closes every store. No store opens after it.
func (s *Service) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.shut = true

	for name, st := range s.streams {
		st.mu.Lock()
		st.shutDown()
		st.mu.Unlock()

		delete(s.streams, name)
	}
}

// ---- engine reads

func (st *stream) find(ctx context.Context, body map[string]any) ([]json.RawMessage, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	page, err := st.col.Find(ctx, raw)

	return page.Rows, err
}

func (st *stream) count(ctx context.Context, where map[string]any) (int, error) {
	body := map[string]any{"count": true, "limit": 1, "fields": []string{"_id"}}

	if where != nil {
		body["query"] = where
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}

	page, err := st.col.Find(ctx, raw)
	if err != nil || page.Total == nil {
		return 0, err
	}

	return *page.Total, nil
}

// edge is the lowest (dir 1) or highest (dir -1) ID of the data.
func (st *stream) edge(ctx context.Context, dir int) (ID, bool, error) {
	rows, err := st.find(ctx, map[string]any{"sort": map[string]any{"_id": dir}, "limit": 1, "fields": []string{"_id"}})
	if err != nil || len(rows) == 0 {
		return ID{}, false, err
	}

	var r struct {
		ID string `json:"_id"`
	}

	if err := json.Unmarshal(rows[0], &r); err != nil {
		return ID{}, false, err
	}

	id, ok := FromKey(r.ID)

	return id, ok, nil
}

// entries gives the rows of keys, by key; a key with no row is not there.
func (st *stream) entries(ctx context.Context, keys []string, fields []string) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}

	if len(keys) == 0 {
		return out, nil
	}

	body := map[string]any{"query": map[string]any{"_id": map[string]any{"$in": keys}}, "limit": len(keys)}

	if len(fields) > 0 {
		body["fields"] = withID(fields)
	}

	rows, err := st.find(ctx, body)
	if err != nil {
		return nil, err
	}

	for _, r := range rows {
		var head struct {
			ID string `json:"_id"`
		}

		if json.Unmarshal(r, &head) == nil {
			out[head.ID] = r
		}
	}

	return out, nil
}

func withID(fields []string) []string {
	if slices.Contains(fields, "_id") {
		return fields
	}

	return append([]string{"_id"}, fields...)
}

// ---- append and trim

// AppendOptions are the keys of an append.
type AppendOptions struct {
	ID       string
	NoCreate bool
	Trim     *TrimOptions
}

// Append adds docs in order and gives their IDs. One insert, so one fsync,
// covers all of them.
func (s *Service) Append(ctx context.Context, name string, docs []map[string]any, o AppendOptions) ([]ID, error) {
	if len(docs) == 0 {
		return nil, badRequest("an append needs doc or docs")
	}

	if len(docs) > 1 && o.ID != "" && o.ID != "*" {
		return nil, badRequest("id is for one doc; docs take * IDs")
	}

	for _, d := range docs {
		if _, ok := d["_id"]; ok {
			return nil, query.NewError("invalid_doc", "a stream entry cannot set _id: use id")
		}

		if _, ok := d[catalog.StreamTime]; ok {
			return nil, query.NewError("invalid_doc", "a stream entry cannot set %s", catalog.StreamTime)
		}
	}

	var ids []ID

	// A stream that a truncate, a drop or a rename closes before the
	// insert gives errClosed: the append opens it again.
	for {
		st, err := s.open(ctx, name, !o.NoCreate)

		if err != nil {
			return nil, err
		}

		st.mu.Unlock()

		ids, err = s.commit(ctx, st, &appendReq{docs: docs, idArg: o.ID, done: make(chan struct{})})

		if errors.Is(err, errClosed) {
			continue
		}

		if err != nil {
			return nil, err
		}

		break
	}

	if o.Trim == nil {
		return ids, nil
	}

	_, err := s.Trim(ctx, name, *o.Trim)

	return ids, err
}

// commit queues req and waits for its insert. The first append of an
// empty queue leads: it inserts the queue in rounds, one insert (one fsync)
// per round, until the queue is empty. IDs are made in queue order under
// the stream lock, so the insert order is the ID order: a group read never
// sees an ID before a lower one is written.
func (s *Service) commit(ctx context.Context, st *stream, req *appendReq) ([]ID, error) {
	st.qmu.Lock()
	st.queue = append(st.queue, req)

	if st.flushing {
		st.qmu.Unlock()

		<-req.done

		return req.ids, req.err
	}

	st.flushing = true
	st.qmu.Unlock()

	// The insert of a round is for every append in it: a caller that goes
	// away does not cancel the others.
	flushCtx := context.WithoutCancel(ctx)

	for {
		st.qmu.Lock()

		batch := st.queue
		st.queue = nil

		if len(batch) == 0 {
			st.flushing = false
			st.qmu.Unlock()

			break
		}

		st.qmu.Unlock()

		st.mu.Lock()

		if st.closed {
			for _, r := range batch {
				r.err = errClosed
			}
		} else {
			s.insertRound(flushCtx, st, batch)
		}

		st.mu.Unlock()

		for _, r := range batch {
			close(r.done)
		}
	}

	return req.ids, req.err
}

// insertRound gives the IDs of a round and inserts its documents at once.
// An append with a bad ID fails alone; a failed insert fails the round.
func (s *Service) insertRound(ctx context.Context, st *stream, batch []*appendReq) {
	last := st.last

	var (
		raws []json.RawMessage
		ok   []*appendReq
	)

	for _, r := range batch {
		ids, docs, next, err := s.prepare(r, last)
		if err != nil {
			r.err = err

			continue
		}

		r.ids, last = ids, next
		raws = append(raws, docs...)
		ok = append(ok, r)
	}

	if len(raws) == 0 {
		return
	}

	plain := make([][]byte, len(raws))

	for i, raw := range raws {
		plain[i] = raw
	}

	if _, err := st.col.Insert(ctx, plain...); err != nil {
		for _, r := range ok {
			r.ids, r.err = nil, err
		}

		return
	}

	st.last = last
	st.added += int64(len(raws))

	close(st.wake)
	st.wake = make(chan struct{})
}

// prepare gives the IDs and the JSON of the documents of one append after
// last, and the last ID it used.
func (s *Service) prepare(r *appendReq, last ID) ([]ID, []json.RawMessage, ID, error) {
	ids := make([]ID, len(r.docs))
	raws := make([]json.RawMessage, len(r.docs))

	for i, d := range r.docs {
		id, err := s.nextID(last, r.idArg)
		if err != nil {
			return nil, nil, last, err
		}

		ids[i], last = id, id

		d["_id"] = id.Key()
		d[catalog.StreamTime] = time.UnixMilli(int64(id.Ms)).UTC().Format(time.RFC3339Nano)

		if raws[i], err = json.Marshal(d); err != nil {
			return nil, nil, last, err
		}
	}

	return ids, raws, last, nil
}

// nextID is the ID of the next entry after last for the id argument of an
// append: "*" or "", "<ms>-*" or "<ms>-<seq>".
func (s *Service) nextID(last ID, arg string) (ID, error) {
	switch {
	case arg == "" || arg == "*":
		ms := uint64(s.now().UnixMilli())

		switch {
		case ms > last.Ms:
			return ID{ms, 0}, nil
		case last == Max:
			return ID{}, errExhausted()
		}

		return last.Next(), nil
	case len(arg) > 2 && arg[len(arg)-2:] == "-*":
		id, err := Parse(arg[:len(arg)-2], 0)
		if err != nil {
			return ID{}, invalidID(arg)
		}

		switch {
		case last == Max:
			return ID{}, errExhausted()
		case id.Ms > last.Ms:
			return ID{id.Ms, 0}, nil
		case id.Ms == last.Ms && last.Seq == Max.Seq:
			return ID{}, errTooSmall(ID{id.Ms, Max.Seq}, last)
		case id.Ms == last.Ms && !last.IsZero():
			return last.Next(), nil
		case id.Ms == last.Ms:
			return ID{id.Ms, 1}, nil
		}

		return ID{}, errTooSmall(id, last)
	}

	id, err := Parse(arg, 0)
	if err != nil {
		return ID{}, err
	}

	if id.IsZero() {
		return ID{}, query.NewError("invalid_stream_id", "0-0 is not a valid entry ID")
	}

	if !last.Less(id) {
		return ID{}, errTooSmall(id, last)
	}

	return id, nil
}

// TrimOptions are the keys of a trim.
type TrimOptions struct {
	MaxLen *int64 `json:"max_len"`
	MinID  string `json:"min_id"`
	Approx bool   `json:"approx"`
	Limit  int    `json:"limit"`
}

// ApproxBlock is the unit of an approximate trim: a row group of a segment.
// Approx deletes only whole blocks of the oldest entries, so a stream trimmed
// on every append (MAXLEN ~ in Redis) deletes once per block, not once per
// append. It keeps more entries than asked, never fewer.
const ApproxBlock = 1024

// Trim deletes the oldest entries, by count or by ID; Limit caps the
// deletes of an approximate trim, as LIMIT in Redis (it needs approx).
func (s *Service) Trim(ctx context.Context, name string, o TrimOptions) (int, error) {
	switch {
	case (o.MaxLen == nil) == (o.MinID == ""):
		return 0, badRequest("trim takes max_len or min_id")
	case o.MaxLen != nil && *o.MaxLen < 0:
		return 0, badRequest("max_len must be >= 0")
	case o.Limit < 0:
		return 0, badRequest("limit must be >= 0")
	case o.Limit > 0 && !o.Approx:
		return 0, badRequest("limit needs approx: true (LIMIT needs ~ in Redis)")
	}

	st, err := s.open(ctx, name, false)
	if err != nil {
		return 0, err
	}

	defer st.mu.Unlock()

	drop, err := st.toDrop(ctx, o)

	if err != nil {
		return 0, err
	}

	if o.Approx {
		drop -= drop % ApproxBlock
	}

	if o.Limit > 0 {
		drop = min(drop, int64(o.Limit))
	}

	if drop <= 0 {
		return 0, nil
	}

	rows, err := st.find(ctx, map[string]any{"sort": map[string]any{"_id": 1}, "limit": drop, "fields": []string{"_id"}})

	if err != nil || len(rows) == 0 {
		return 0, err
	}

	var r struct {
		ID string `json:"_id"`
	}

	_ = json.Unmarshal(rows[len(rows)-1], &r)
	cut, _ := FromKey(r.ID)

	return st.deleteUpTo(ctx, cut)
}

// toDrop is the number of the oldest entries that a trim deletes before
// approx and limit: the entries over max_len, or the entries below min_id.
func (st *stream) toDrop(ctx context.Context, o TrimOptions) (int64, error) {
	if o.MaxLen != nil {
		n, err := st.count(ctx, nil)

		return int64(n) - *o.MaxLen, err
	}

	min, err := Parse(o.MinID, 0)

	if err != nil {
		return 0, err
	}

	n, err := st.count(ctx, map[string]any{"_id": map[string]any{"$lt": min.Key()}})

	return int64(n), err
}

// deleteUpTo deletes every entry up to cut, in batches of IDs (a delete by
// query takes the id, not a range of it). A trim does not change the max
// deleted ID: as in Redis, only a delete of one entry (XDEL) does.
func (st *stream) deleteUpTo(ctx context.Context, cut ID) (int, error) {
	total := 0

	for {
		rows, err := st.find(ctx, map[string]any{
			"query":  map[string]any{"_id": map[string]any{"$lte": cut.Key()}},
			"sort":   map[string]any{"_id": 1},
			"limit":  MaxCount,
			"fields": []string{"_id"},
		})
		if err != nil || len(rows) == 0 {
			if total > 0 {
				err = errors.Join(err, st.st.Update(st.checkpoint))
			}

			return total, err
		}

		keys := make([]string, len(rows))

		for i, r := range rows {
			var h struct {
				ID string `json:"_id"`
			}

			_ = json.Unmarshal(r, &h)
			keys[i] = h.ID
		}

		filter, err := json.Marshal(map[string]any{"_id": map[string]any{"$in": keys}})
		if err != nil {
			return total, err
		}

		n, err := st.col.Delete(ctx, filter, true)

		total += n

		if err != nil {
			return total, err
		}
	}
}

// Deleted records that an entry was deleted by DELETE /{s}/_doc/{id}.
func (s *Service) Deleted(ctx context.Context, name, key string) {
	id, ok := FromKey(key)

	if !ok {
		return
	}

	st, err := s.open(ctx, name, false)
	if err != nil {
		return
	}

	defer st.mu.Unlock()

	if st.maxDeleted.Less(id) {
		st.maxDeleted = id

		_ = st.st.Update(st.checkpoint)
	}
}

// Info is GET /{s}/_streams.
type Info struct {
	Length       int    `json:"length"`
	FirstID      string `json:"first_id"`
	LastID       string `json:"last_id"`
	EntriesAdded int64  `json:"entries_added"`
	MaxDeletedID string `json:"max_deleted_id"`
	Groups       int    `json:"groups"`
}

func (s *Service) Info(ctx context.Context, name string) (Info, error) {
	st, err := s.open(ctx, name, false)
	if err != nil {
		return Info{}, err
	}

	defer st.mu.Unlock()

	n, err := st.count(ctx, nil)
	if err != nil {
		return Info{}, err
	}

	first, _, err := st.edge(ctx, 1)
	if err != nil {
		return Info{}, err
	}

	info := Info{Length: n, FirstID: first.String(), LastID: st.last.String(), EntriesAdded: st.added, MaxDeletedID: st.maxDeleted.String()}

	info.Groups, err = st.groupCount()

	return info, err
}

func (st *stream) groupCount() (int, error) {
	n := 0

	err := st.st.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bGroups).ForEachBucket(func([]byte) error {
			n++

			return nil
		})
	})

	return n, err
}

// ---- groups

// GroupHead is the answer of a group create or setid.
type GroupHead struct {
	Group           string `json:"group"`
	LastDeliveredID string `json:"last_delivered_id"`
}

// start reads the start of a group: "$", "0" or an ID, and its
// entries_read: the given one, else -1 (unknown), as in Redis 7. The lag
// of a group with an unknown entries_read is an estimate (lagOf).
func (st *stream) start(arg string, entriesRead *int64) (ID, int64, error) {
	read := int64(-1)

	if entriesRead != nil {
		read = *entriesRead
	}

	switch arg {
	case "", "$":
		return st.last, read, nil
	case "0", "0-0", "-":
		return ID{}, read, nil
	}

	id, err := Parse(arg, 0)

	if err != nil {
		return ID{}, 0, err
	}

	return id, read, nil
}

func (s *Service) CreateGroup(ctx context.Context, name, g, startArg string, entriesRead *int64, noCreate bool) (GroupHead, error) {
	st, err := s.open(ctx, name, !noCreate)
	if err != nil {
		return GroupHead{}, err
	}

	defer st.mu.Unlock()

	start, read, err := st.start(startArg, entriesRead)
	if err != nil {
		return GroupHead{}, err
	}

	err = st.st.Update(func(tx *bolt.Tx) error {
		groups := tx.Bucket(bGroups)

		if groups.Bucket([]byte(g)) != nil {
			return errGroupExists(g)
		}

		b, err := groups.CreateBucket([]byte(g))
		if err != nil {
			return err
		}

		for _, sub := range [][]byte{bPEL, bConsumers} {
			if _, err := b.CreateBucket(sub); err != nil {
				return err
			}
		}

		if err := putID(b, "last", start); err != nil {
			return err
		}

		if err := putInt(b, "read", read); err != nil {
			return err
		}

		return st.checkpoint(tx)
	})

	return GroupHead{g, start.String()}, err
}

func (s *Service) SetID(ctx context.Context, name, g, idArg string, entriesRead *int64) (GroupHead, error) {
	st, err := s.open(ctx, name, false)
	if err != nil {
		return GroupHead{}, err
	}

	defer st.mu.Unlock()

	start, read, err := st.start(idArg, entriesRead)
	if err != nil {
		return GroupHead{}, err
	}

	err = st.st.Update(func(tx *bolt.Tx) error {
		b, err := groupBucket(tx, g)
		if err != nil {
			return errGroup(g)
		}

		if err := putID(b, "last", start); err != nil {
			return err
		}

		return putInt(b, "read", read)
	})

	return GroupHead{g, start.String()}, err
}

func (s *Service) DestroyGroup(ctx context.Context, name, g string) error {
	st, err := s.open(ctx, name, false)
	if err != nil {
		return err
	}

	defer st.mu.Unlock()

	return st.st.Update(func(tx *bolt.Tx) error {
		if err := tx.Bucket(bGroups).DeleteBucket([]byte(g)); err != nil {
			return errGroup(g)
		}

		return nil
	})
}

func (s *Service) CreateConsumer(ctx context.Context, name, g, c string) (bool, error) {
	st, err := s.open(ctx, name, false)
	if err != nil {
		return false, err
	}

	defer st.mu.Unlock()

	created := false

	err = st.st.Update(func(tx *bolt.Tx) error {
		b, err := groupBucket(tx, g)
		if err != nil {
			return errGroup(g)
		}

		cons := b.Bucket(bConsumers)

		if _, ok := getConsumer(cons, c); ok {
			return nil
		}

		created = true

		return putConsumer(cons, c, consumer{Seen: s.now().UnixMilli()})
	})

	return created, err
}

// DeleteConsumer deletes c and its pending entries, and gives their number.
func (s *Service) DeleteConsumer(ctx context.Context, name, g, c string) (int, error) {
	st, err := s.open(ctx, name, false)
	if err != nil {
		return 0, err
	}

	defer st.mu.Unlock()

	n := 0

	err = st.st.Update(func(tx *bolt.Tx) error {
		b, err := groupBucket(tx, g)
		if err != nil {
			return errGroup(g)
		}

		pel := b.Bucket(bPEL)

		var keys [][]byte

		_ = pel.ForEach(func(k, v []byte) error {
			if p, ok := getPending(pel, k); ok && p.Consumer == c {
				keys = append(keys, slices.Clone(k))
			}

			return nil
		})

		for _, k := range keys {
			if err := pel.Delete(k); err != nil {
				return err
			}
		}

		n = len(keys)

		return b.Bucket(bConsumers).Delete([]byte(c))
	})

	return n, err
}

// GroupInfo is one row of GET /{s}/_streams/groups.
type GroupInfo struct {
	Name            string `json:"name"`
	Consumers       int    `json:"consumers"`
	Pending         int    `json:"pending"`
	LastDeliveredID string `json:"last_delivered_id"`
	EntriesRead     *int64 `json:"entries_read"`
	Lag             *int64 `json:"lag"`
}

func (s *Service) Groups(ctx context.Context, name string) ([]GroupInfo, error) {
	st, err := s.open(ctx, name, false)
	if err != nil {
		return nil, err
	}

	defer st.mu.Unlock()

	out := []GroupInfo{}
	sh := st.shapeOf(ctx)

	err = st.st.View(func(tx *bolt.Tx) error {
		groups := tx.Bucket(bGroups)

		return groups.ForEachBucket(func(k []byte) error {
			b := groups.Bucket(k)
			head := readGroup(b)
			info := GroupInfo{Name: string(k), LastDeliveredID: head.Last.String(), Pending: b.Bucket(bPEL).Stats().KeyN}

			info.Consumers = b.Bucket(bConsumers).Stats().KeyN

			if head.Read >= 0 {
				read := head.Read
				info.EntriesRead = &read
			}

			info.Lag = st.lagOf(sh, head)

			out = append(out, info)

			return nil
		})
	})

	if err == nil {
		err = sh.err
	}

	return out, err
}

// ---- entries_read and lag, as in Redis 7 (t_stream.c)

// shape is the length and the first ID of the data, read once and only
// when an estimate needs them.
type shape struct {
	st    *stream
	ctx   context.Context
	known bool
	err   error

	length int64
	first  ID
}

func (st *stream) shapeOf(ctx context.Context) *shape {
	return &shape{st: st, ctx: ctx}
}

func (sh *shape) load() {
	if sh.known {
		return
	}

	sh.known = true

	n, err := sh.st.count(sh.ctx, nil)

	if err != nil {
		sh.err = err

		return
	}

	first, _, err := sh.st.edge(sh.ctx, 1)

	if err != nil {
		sh.err = err

		return
	}

	sh.length, sh.first = int64(n), first
}

// hasTombstones is streamRangeHasTombstones from start to the end: an
// entry deleted one by one (XDEL) at or after start, while the stream has
// entries after it.
func (st *stream) hasTombstones(sh *shape, start ID) bool {
	if st.maxDeleted.IsZero() {
		return false
	}

	sh.load()

	if sh.length == 0 || st.maxDeleted.Less(sh.first) {
		return false
	}

	return !st.maxDeleted.Less(start)
}

// estimate is streamEstimateDistanceFromFirstEverEntry: the entries_read
// of a group whose last delivered ID is id, or -1 when it cannot be known.
func (st *stream) estimate(sh *shape, id ID) int64 {
	if st.added == 0 {
		return 0
	}

	sh.load()

	if sh.length == 0 && !st.last.Less(id) {
		return st.added
	}

	switch {
	case id == st.last:
		return st.added
	case st.last.Less(id):
		return -1
	}

	if st.maxDeleted.IsZero() || st.maxDeleted.Less(sh.first) {
		switch {
		case id.Less(sh.first):
			return st.added - sh.length
		case id == sh.first:
			return st.added - sh.length + 1
		}
	}

	return -1
}

// lagOf is the lag of XINFO GROUPS: exact from entries_read when no
// deleted entry is after the group, else from the estimate; nil when it
// cannot be known.
func (st *stream) lagOf(sh *shape, head group) *int64 {
	var lag int64

	switch {
	case st.added == 0:
		lag = 0
	case head.Read >= 0 && !st.hasTombstones(sh, head.Last):
		lag = st.added - head.Read
	default:
		read := st.estimate(sh, head.Last)

		if read < 0 {
			return nil
		}

		lag = st.added - read
	}

	return &lag
}

// delivered is the entries_read of a group after it gets id, as XREADGROUP
// counts it: one more while the count is valid and no deleted entry is
// ahead, else the estimate.
func (st *stream) delivered(sh *shape, read int64, id ID) int64 {
	if read >= 0 && !st.hasTombstones(sh, id) {
		return read + 1
	}

	if st.added > 0 {
		return st.estimate(sh, id)
	}

	return read
}

// ConsumerInfo is one row of GET /{s}/_streams/groups/{g}/consumers.
type ConsumerInfo struct {
	Name       string `json:"name"`
	Pending    int    `json:"pending"`
	IdleMs     int64  `json:"idle_ms"`
	InactiveMs int64  `json:"inactive_ms"`
}

func (s *Service) Consumers(ctx context.Context, name, g string) ([]ConsumerInfo, error) {
	st, err := s.open(ctx, name, false)
	if err != nil {
		return nil, err
	}

	defer st.mu.Unlock()

	now := s.now().UnixMilli()
	out := []ConsumerInfo{}

	err = st.st.View(func(tx *bolt.Tx) error {
		b, err := groupBucket(tx, g)
		if err != nil {
			return errGroup(g)
		}

		counts := map[string]int{}
		pel := b.Bucket(bPEL)

		_ = pel.ForEach(func(k, _ []byte) error {
			if p, ok := getPending(pel, k); ok {
				counts[p.Consumer]++
			}

			return nil
		})

		cons := b.Bucket(bConsumers)

		return cons.ForEach(func(k, _ []byte) error {
			c, _ := getConsumer(cons, string(k))
			info := ConsumerInfo{Name: string(k), Pending: counts[string(k)], IdleMs: now - c.Seen, InactiveMs: -1}

			if c.Active > 0 {
				info.InactiveMs = now - c.Active
			}

			out = append(out, info)

			return nil
		})
	})

	return out, err
}

// ---- read, ack, pending, claim

// ReadOptions are the keys of a group read.
type ReadOptions struct {
	Consumer string   `json:"consumer"`
	Count    int      `json:"count"`
	BlockMs  int64    `json:"block_ms"`
	Pending  bool     `json:"pending"`
	After    string   `json:"after"`
	NoAck    bool     `json:"no_ack"`
	Fields   []string `json:"fields"`
}

// MaxCount is the most entries of one read or claim.
const MaxCount = 10_000

func (o *ReadOptions) check() error {
	switch {
	case o.Consumer == "":
		return badRequest("read needs a consumer")
	case o.Count < 0 || o.Count > MaxCount:
		return badRequest("count is 1 to %d", MaxCount)
	case o.BlockMs < 0:
		return badRequest("block_ms must be >= 0")
	case o.Pending && o.BlockMs > 0:
		return badRequest("block_ms is for new entries, not pending")
	}

	if o.Count == 0 {
		o.Count = 10
	}

	return nil
}

// Read is XREADGROUP: new entries (each goes into the PEL of the consumer)
// or, with Pending, the entries of its PEL. The rows have padded _id.
func (s *Service) Read(ctx context.Context, name, g string, o ReadOptions) ([]json.RawMessage, error) {
	if err := o.check(); err != nil {
		return nil, err
	}

	deadline := s.now().Add(time.Duration(o.BlockMs) * time.Millisecond)

	for {
		st, err := s.open(ctx, name, false)
		if err != nil {
			return nil, err
		}

		var rows []json.RawMessage

		if o.Pending {
			rows, err = s.readPending(ctx, st, g, o)
		} else {
			rows, err = s.readNew(ctx, st, g, o)
		}

		wake := st.wake

		st.mu.Unlock()

		if err != nil || len(rows) > 0 || o.BlockMs == 0 {
			return rows, err
		}

		wait := time.Until(deadline)

		if wait <= 0 {
			return rows, nil
		}

		timer := time.NewTimer(wait)

		select {
		case <-wake:
			timer.Stop()
		case <-timer.C:
			return rows, nil
		case <-ctx.Done():
			timer.Stop()

			return nil, ctx.Err()
		}
	}
}

// TailOptions are the keys of a read without a group.
type TailOptions struct {
	After   string   `json:"after"`
	Count   int      `json:"count"`
	BlockMs int64    `json:"block_ms"`
	Fields  []string `json:"fields"`
}

func (o *TailOptions) check() error {
	switch {
	case o.Count < 0 || o.Count > MaxCount:
		return badRequest("count is 1 to %d", MaxCount)
	case o.BlockMs < 0:
		return badRequest("block_ms must be >= 0")
	}

	if o.Count == 0 {
		o.Count = 10
	}

	return nil
}

// Tail is XREAD: the entries after an ID, with no group and no PEL. After
// is an ID ("0" by default) or "$", the last ID when the call starts. With
// BlockMs and no entry, it waits for an append.
func (s *Service) Tail(ctx context.Context, name string, o TailOptions) ([]json.RawMessage, error) {
	if err := o.check(); err != nil {
		return nil, err
	}

	deadline := s.now().Add(time.Duration(o.BlockMs) * time.Millisecond)

	var (
		after ID
		known bool
	)

	for {
		st, err := s.open(ctx, name, false)

		// XREAD BLOCK on a missing key waits for it, as for an entry. The
		// stream is made by an append that does not go through a wake of
		// this stream, so the wait polls.
		if errors.Is(err, catalog.ErrUnknown) && o.BlockMs > 0 {
			if !known {
				after, known, err = s.startOfMissing(o.After)

				if err != nil {
					return nil, err
				}
			}

			if done, err := waitPoll(ctx, deadline); done {
				return nil, err
			}

			continue
		}

		if err != nil {
			return nil, err
		}

		if !known {
			after, _, err = st.start(tailStart(o.After), nil)
			known = err == nil
		}

		var rows []json.RawMessage

		if err == nil {
			body := map[string]any{
				"query": map[string]any{"_id": map[string]any{"$gt": after.Key()}},
				"sort":  map[string]any{"_id": 1},
				"limit": o.Count,
			}

			if len(o.Fields) > 0 {
				body["fields"] = withID(o.Fields)
			}

			rows, err = st.find(ctx, body)
		}

		wake := st.wake

		st.mu.Unlock()

		if err != nil || len(rows) > 0 || o.BlockMs == 0 {
			return rows, err
		}

		wait := time.Until(deadline)

		if wait <= 0 {
			return rows, nil
		}

		timer := time.NewTimer(wait)

		select {
		case <-wake:
			timer.Stop()
		case <-timer.C:
			return rows, nil
		case <-ctx.Done():
			timer.Stop()

			return nil, ctx.Err()
		}
	}
}

// missingPoll is how often a blocked tail on a missing stream looks for it.
const missingPoll = 50 * time.Millisecond

// startOfMissing is the start of a tail on a stream that does not exist:
// every ID, "$" too, is after 0-0 (the last ID of a missing key in Redis).
func (s *Service) startOfMissing(after string) (ID, bool, error) {
	switch tailStart(after) {
	case "$", "0", "0-0", "-":
		return ID{}, true, nil
	}

	id, err := Parse(after, 0)

	return id, err == nil, err
}

// waitPoll waits one poll, or until the deadline or the end of ctx. done
// tells that the tail ends: at the deadline (no error, no rows) or with
// the error of ctx.
func waitPoll(ctx context.Context, deadline time.Time) (bool, error) {
	wait := time.Until(deadline)

	if wait <= 0 {
		return true, nil
	}

	timer := time.NewTimer(min(wait, missingPoll))

	defer timer.Stop()

	select {
	case <-timer.C:
		return false, nil
	case <-ctx.Done():
		return true, ctx.Err()
	}
}

// tailStart is the start of a tail: no value is the start of the stream,
// not "$" as for a group.
func tailStart(after string) string {
	if after == "" {
		return "0"
	}

	return after
}

func (s *Service) readNew(ctx context.Context, st *stream, g string, o ReadOptions) ([]json.RawMessage, error) {
	var head group

	err := st.st.View(func(tx *bolt.Tx) error {
		b, err := groupBucket(tx, g)
		if err != nil {
			return errGroup(g)
		}

		head = readGroup(b)

		return nil
	})
	if err != nil {
		return nil, err
	}

	body := map[string]any{
		"query": map[string]any{"_id": map[string]any{"$gt": head.Last.Key()}},
		"sort":  map[string]any{"_id": 1},
		"limit": o.Count,
	}

	if len(o.Fields) > 0 {
		body["fields"] = withID(o.Fields)
	}

	rows, err := st.find(ctx, body)
	if err != nil {
		return nil, err
	}

	keys := make([]string, 0, len(rows))

	for _, r := range rows {
		var h struct {
			ID string `json:"_id"`
		}

		_ = json.Unmarshal(r, &h)
		keys = append(keys, h.ID)
	}

	now := s.now().UnixMilli()

	err = st.st.Update(func(tx *bolt.Tx) error {
		b, err := groupBucket(tx, g)
		if err != nil {
			return errGroup(g)
		}

		cons := b.Bucket(bConsumers)
		c, _ := getConsumer(cons, o.Consumer)
		c.Seen = now

		if len(keys) == 0 {
			return putConsumer(cons, o.Consumer, c)
		}

		c.Active = now

		if err := putConsumer(cons, o.Consumer, c); err != nil {
			return err
		}

		if !o.NoAck {
			pel := b.Bucket(bPEL)

			for _, k := range keys {
				if err := putPending(pel, k, pending{Consumer: o.Consumer, Delivered: now, Deliveries: 1}); err != nil {
					return err
				}
			}
		}

		read := head.Read
		sh := st.shapeOf(ctx)

		var last ID

		for _, k := range keys {
			last, _ = FromKey(k)
			read = st.delivered(sh, read, last)
		}

		if sh.err != nil {
			return sh.err
		}

		if err := putID(b, "last", last); err != nil {
			return err
		}

		if err := putInt(b, "read", read); err != nil {
			return err
		}

		return st.checkpoint(tx)
	})

	return rows, err
}

func (s *Service) readPending(ctx context.Context, st *stream, g string, o ReadOptions) ([]json.RawMessage, error) {
	after := ID{}

	if o.After != "" {
		var err error

		if after, err = Parse(o.After, 0); err != nil {
			return nil, err
		}
	}

	var keys []string

	now := s.now().UnixMilli()

	err := st.st.Update(func(tx *bolt.Tx) error {
		b, err := groupBucket(tx, g)
		if err != nil {
			return errGroup(g)
		}

		pel := b.Bucket(bPEL)
		cur := pel.Cursor()

		// No ID is after the highest one: Next would wrap to 0-0.
		k, _ := cur.Seek([]byte(after.Next().Key()))

		if after == Max {
			k = nil
		}

		for ; k != nil && len(keys) < o.Count; k, _ = cur.Next() {
			if p, ok := getPending(pel, k); ok && p.Consumer == o.Consumer {
				keys = append(keys, string(k))
			}
		}

		cons := b.Bucket(bConsumers)
		c, _ := getConsumer(cons, o.Consumer)
		c.Seen = now

		return putConsumer(cons, o.Consumer, c)
	})
	if err != nil {
		return nil, err
	}

	found, err := st.entries(ctx, keys, o.Fields)
	if err != nil {
		return nil, err
	}

	rows := make([]json.RawMessage, 0, len(keys))

	for _, k := range keys {
		if r, ok := found[k]; ok {
			rows = append(rows, r)

			continue
		}

		rows = append(rows, json.RawMessage(`{"_id":"`+k+`","_deleted":true}`))
	}

	if err := s.redeliver(st, g, o.Consumer, keys, found, now); err != nil {
		return nil, err
	}

	return rows, nil
}

// redeliver counts a pending read as a delivery, as Redis does: each entry
// given again gets delivery count + 1 and a new delivery time. A deleted
// entry does not change.
func (s *Service) redeliver(st *stream, g, consumer string, keys []string, found map[string]json.RawMessage, now int64) error {
	if len(found) == 0 {
		return nil
	}

	return st.st.Update(func(tx *bolt.Tx) error {
		b, err := groupBucket(tx, g)
		if err != nil {
			return errGroup(g)
		}

		pel := b.Bucket(bPEL)

		for _, k := range keys {
			if _, ok := found[k]; !ok {
				continue
			}

			p, ok := getPending(pel, []byte(k))

			if !ok || p.Consumer != consumer {
				continue
			}

			p.Deliveries++
			p.Delivered = now

			if err := putPending(pel, k, p); err != nil {
				return err
			}
		}

		return nil
	})
}

// Ack removes the IDs from the PEL of g and gives how many were pending.
func (s *Service) Ack(ctx context.Context, name, g string, ids []string) (int, error) {
	keys, err := keysOf(ids)
	if err != nil {
		return 0, err
	}

	// A truncate, a drop or a rename can close the store between the
	// unlock and the batch: the ack opens the stream again.
	for {
		st, err := s.open(ctx, name, false)

		if err != nil {
			return 0, err
		}

		// An ack changes only the PEL: it does not need the stream lock, so
		// it runs while a read holds it, and bbolt joins the acks that come
		// at once into one transaction (one fsync).
		store := st.st

		st.mu.Unlock()

		n, err := ackIn(store, g, keys)

		if errors.Is(err, bolt.ErrDatabaseNotOpen) {
			continue
		}

		return n, err
	}
}

// ackIn removes keys from the PEL of g in store.
func ackIn(store *bolt.DB, g string, keys []string) (int, error) {
	n := 0

	err := store.Batch(func(tx *bolt.Tx) error {
		n = 0

		b, err := groupBucket(tx, g)
		if err != nil {
			return errGroup(g)
		}

		pel := b.Bucket(bPEL)

		for _, k := range keys {
			if pel.Get([]byte(k)) == nil {
				continue
			}

			if err := pel.Delete([]byte(k)); err != nil {
				return err
			}

			n++
		}

		return nil
	})

	return n, err
}

func keysOf(ids []string) ([]string, error) {
	keys := make([]string, len(ids))

	for i, raw := range ids {
		id, err := Parse(raw, 0)
		if err != nil {
			return nil, err
		}

		keys[i] = id.Key()
	}

	return keys, nil
}

// PendingOptions are the keys of XPENDING. Count 0 is the summary.
type PendingOptions struct {
	Start    string `json:"start"`
	End      string `json:"end"`
	Count    int    `json:"count"`
	Consumer string `json:"consumer"`
	IdleMs   int64  `json:"idle_ms"`
}

// PendingSummary is XPENDING with no range.
type PendingSummary struct {
	Count     int             `json:"count"`
	Lowest    *string         `json:"lowest"`
	Highest   *string         `json:"highest"`
	Consumers []ConsumerCount `json:"consumers"`
}

type ConsumerCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// PendingEntry is one row of XPENDING with a range.
type PendingEntry struct {
	ID         string `json:"_id"`
	Consumer   string `json:"consumer"`
	IdleMs     int64  `json:"idle_ms"`
	Deliveries int64  `json:"deliveries"`
}

// Pending gives a PendingSummary (Count 0) or []PendingEntry.
func (s *Service) Pending(ctx context.Context, name, g string, o PendingOptions) (any, error) {
	st, err := s.open(ctx, name, false)
	if err != nil {
		return nil, err
	}

	defer st.mu.Unlock()

	if o.Count < 0 || o.Count > MaxCount {
		return nil, badRequest("count is 1 to %d", MaxCount)
	}

	if o.Count == 0 {
		return st.pendingSummary(g)
	}

	start, err := Parse(orDefault(o.Start, "-"), 0)
	if err != nil {
		return nil, err
	}

	end, err := Parse(orDefault(o.End, "+"), Max.Seq)
	if err != nil {
		return nil, err
	}

	now := s.now().UnixMilli()
	out := []PendingEntry{}

	err = st.st.View(func(tx *bolt.Tx) error {
		b, err := groupBucket(tx, g)
		if err != nil {
			return errGroup(g)
		}

		pel := b.Bucket(bPEL)
		cur := pel.Cursor()

		for k, _ := cur.Seek([]byte(start.Key())); k != nil && len(out) < o.Count; k, _ = cur.Next() {
			id, _ := FromKey(string(k))

			if end.Less(id) {
				break
			}

			p, ok := getPending(pel, k)
			idle := now - p.Delivered

			if !ok || o.Consumer != "" && p.Consumer != o.Consumer || idle < o.IdleMs {
				continue
			}

			out = append(out, PendingEntry{ID: id.String(), Consumer: p.Consumer, IdleMs: idle, Deliveries: p.Deliveries})
		}

		return nil
	})

	return out, err
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}

	return s
}

func (st *stream) pendingSummary(g string) (PendingSummary, error) {
	sum := PendingSummary{Consumers: []ConsumerCount{}}

	err := st.st.View(func(tx *bolt.Tx) error {
		b, err := groupBucket(tx, g)
		if err != nil {
			return errGroup(g)
		}

		pel := b.Bucket(bPEL)
		counts := map[string]int{}

		_ = pel.ForEach(func(k, _ []byte) error {
			id, _ := FromKey(string(k))
			text := id.String()

			if sum.Lowest == nil {
				sum.Lowest = &text
			}

			sum.Highest = &text
			sum.Count++

			if p, ok := getPending(pel, k); ok {
				counts[p.Consumer]++
			}

			return nil
		})

		for name, n := range counts {
			sum.Consumers = append(sum.Consumers, ConsumerCount{name, n})
		}

		slices.SortFunc(sum.Consumers, func(a, b ConsumerCount) int {
			if a.Name < b.Name {
				return -1
			}

			return 1
		})

		return nil
	})

	return sum, err
}

// ClaimOptions are the keys of XCLAIM (IDs) and XAUTOCLAIM (Cursor).
type ClaimOptions struct {
	Consumer   string   `json:"consumer"`
	MinIdleMs  int64    `json:"min_idle_ms"`
	IDs        []string `json:"ids"`
	Cursor     string   `json:"cursor"`
	Count      int      `json:"count"`
	JustIDs    bool     `json:"just_ids"`
	IdleMs     *int64   `json:"idle_ms"`
	RetryCount *int64   `json:"retry_count"`
	Force      bool     `json:"force"`
}

// ClaimResult is the answer of a claim. Cursor is set only with a scan.
type ClaimResult struct {
	Cursor  *string           `json:"cursor,omitempty"`
	Entries []json.RawMessage `json:"entries"`
	Deleted []string          `json:"deleted"`
}

func (s *Service) Claim(ctx context.Context, name, g string, o ClaimOptions) (ClaimResult, error) {
	switch {
	case o.Consumer == "":
		return ClaimResult{}, badRequest("claim needs a consumer")
	case (o.IDs == nil) == (o.Cursor == ""):
		return ClaimResult{}, badRequest("claim takes ids or a cursor")
	case o.Count < 0 || o.Count > MaxCount:
		return ClaimResult{}, badRequest("count is 1 to %d", MaxCount)
	}

	if o.Count == 0 {
		o.Count = 100
	}

	st, err := s.open(ctx, name, false)
	if err != nil {
		return ClaimResult{}, err
	}

	defer st.mu.Unlock()

	candidates, next, err := st.claimCandidates(g, o)
	if err != nil {
		return ClaimResult{}, err
	}

	found, err := st.entries(ctx, candidates, nil)
	if err != nil {
		return ClaimResult{}, err
	}

	res := ClaimResult{Entries: []json.RawMessage{}, Deleted: []string{}}
	now := s.now().UnixMilli()

	err = st.st.Update(func(tx *bolt.Tx) error {
		b, err := groupBucket(tx, g)
		if err != nil {
			return errGroup(g)
		}

		pel := b.Bucket(bPEL)

		for _, k := range candidates {
			id, _ := FromKey(k)
			row, exists := found[k]

			p, pend := getPending(pel, []byte(k))

			if !exists {
				if pend {
					res.Deleted = append(res.Deleted, id.String())

					if err := pel.Delete([]byte(k)); err != nil {
						return err
					}
				}

				continue
			}

			if !pend {
				if !o.Force {
					continue
				}

				p = pending{Delivered: now}
			}

			if now-p.Delivered < o.MinIdleMs && pend {
				continue
			}

			p.Consumer = o.Consumer
			p.Delivered = now

			if o.IdleMs != nil {
				p.Delivered = now - *o.IdleMs
			}

			if !o.JustIDs {
				p.Deliveries++
			}

			if o.RetryCount != nil {
				p.Deliveries = *o.RetryCount
			}

			if err := putPending(pel, k, p); err != nil {
				return err
			}

			if o.JustIDs {
				row, _ = json.Marshal(id.String())
			}

			res.Entries = append(res.Entries, row)
		}

		cons := b.Bucket(bConsumers)
		c, _ := getConsumer(cons, o.Consumer)
		c.Seen = now

		return putConsumer(cons, o.Consumer, c)
	})

	if o.Cursor != "" {
		text := next.String()
		res.Cursor = &text
	}

	return res, err
}

// claimCandidates gives the keys to look at: the IDs, or the PEL from the
// cursor (count of them), and the next cursor (0-0 at the end).
func (st *stream) claimCandidates(g string, o ClaimOptions) ([]string, ID, error) {
	if o.IDs != nil {
		keys, err := keysOf(o.IDs)

		return keys, ID{}, err
	}

	from, err := Parse(o.Cursor, 0)
	if err != nil {
		return nil, ID{}, err
	}

	var (
		keys []string
		next ID
	)

	now := st.now().UnixMilli()

	err = st.st.View(func(tx *bolt.Tx) error {
		b, err := groupBucket(tx, g)
		if err != nil {
			return errGroup(g)
		}

		pel := b.Bucket(bPEL)
		cur := pel.Cursor()

		k, _ := cur.Seek([]byte(from.Key()))

		for ; k != nil && len(keys) < o.Count; k, _ = cur.Next() {
			if p, ok := getPending(pel, k); ok && now-p.Delivered >= o.MinIdleMs {
				keys = append(keys, string(k))
			}
		}

		if k != nil {
			next, _ = FromKey(string(k))
		}

		return nil
	})

	return keys, next, err
}

// ---- metrics

// StreamMetrics are the series of one stream that /_metrics prints.
type StreamMetrics struct {
	Name   string
	Added  int64
	Groups []GroupInfo
}

// Metrics gives the series of the streams used since the start (the open
// ones). It reads the counters and the group stores; it counts the entries
// only when a lag needs the estimate of Redis (lagOf).
func (s *Service) Metrics() []StreamMetrics {
	s.mu.Lock()

	open := make([]*stream, 0, len(s.streams))

	for _, st := range s.streams {
		open = append(open, st)
	}

	s.mu.Unlock()

	out := make([]StreamMetrics, 0, len(open))

	for _, st := range open {
		st.mu.Lock()

		if st.st != nil {
			m := StreamMetrics{Name: st.name, Added: st.added}
			sh := st.shapeOf(context.Background())

			_ = st.st.View(func(tx *bolt.Tx) error {
				groups := tx.Bucket(bGroups)

				return groups.ForEachBucket(func(k []byte) error {
					b := groups.Bucket(k)
					head := readGroup(b)
					info := GroupInfo{Name: string(k), Pending: b.Bucket(bPEL).Stats().KeyN}

					info.Lag = st.lagOf(sh, head)

					m.Groups = append(m.Groups, info)

					return nil
				})
			})

			out = append(out, m)
		}

		st.mu.Unlock()
	}

	slices.SortFunc(out, func(a, b StreamMetrics) int { return strings.Compare(a.Name, b.Name) })

	return out
}

// Snapshot is a consistent copy of the group store of a stream, for a
// backup: from a read transaction of the open store, or the file when the
// stream is not open. Nil when the stream has no store.
func (s *Service) Snapshot(name string) ([]byte, error) {
	s.mu.Lock()
	st := s.streams[name]
	s.mu.Unlock()

	if st != nil {
		st.mu.Lock()
		defer st.mu.Unlock()

		if st.st != nil {
			var buf bytes.Buffer

			err := st.st.View(func(tx *bolt.Tx) error {
				_, err := tx.WriteTo(&buf)

				return err
			})

			return buf.Bytes(), err
		}
	}

	db := s.db()

	col, err := db.Get(name)
	if err != nil {
		return nil, nil
	}

	data, err := os.ReadFile(filepath.Join(col.Dir(), StoreFile))

	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}

	return data, err
}
