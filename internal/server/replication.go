package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/feed"
	"github.com/thadeu/ndb/internal/replication"
	"github.com/thadeu/ndb/internal/wal"
)

// Replication (docs/dev/replication.md). The owner side: the change stream
// (GET /_replication/stream), the snapshot of a collection for a resync
// (GET /_replication/snapshot) and the position of a forwarded write
// (Ndb-Position). The copy side: the gate that sends writes to the owner
// (and reads, until the copy caught up), promote and resync. GET
// /_replication shows both.

// defaultReplHeartbeat is how often the owner sends a heartbeat line.
const defaultReplHeartbeat = time.Second

// replication is the replication state of the server.
type replicationState struct {
	// copies are the databases of this daemon that are copies, by name
	// ("" is the default database).
	mu     sync.RWMutex
	copies map[string]*replication.Copy

	// gen is the catalog version: it goes up after each change of a
	// definition, so a copy can wait for it.
	gen atomic.Uint64

	// heartbeat is the time between two heartbeat lines.
	heartbeat time.Duration

	// peers are the copies that read a stream of this daemon now.
	peers   map[int]peer
	nextID  int
	peersMu sync.Mutex
}

// peer is one connected copy.
type peer struct {
	Database string    `json:"database"`
	Addr     string    `json:"addr"`
	Since    time.Time `json:"since"`
}

// SetReplicas makes the databases of copies (one per source) copies: their
// writes go to the owner. Call it before Serve.
func (s *Server) SetReplicas(copies []*replication.Copy) {
	s.repl.mu.Lock()
	defer s.repl.mu.Unlock()

	if s.repl.copies == nil {
		s.repl.copies = map[string]*replication.Copy{}
	}

	for _, c := range copies {
		name := c.Source().Local

		if name == engine.DefaultDatabase {
			name = ""
		}

		s.repl.copies[name] = c
	}
}

// SetReplicationHeartbeat sets the time between two heartbeat lines of a
// change stream (default 1 s). Call it before Serve.
func (s *Server) SetReplicationHeartbeat(d time.Duration) {
	s.repl.heartbeat = d
}

// copyOf is the running copy of the database name ("" = default), or nil.
func (s *Server) copyOf(name string) *replication.Copy {
	s.repl.mu.RLock()
	defer s.repl.mu.RUnlock()

	c := s.repl.copies[name]

	if c == nil || c.Promoted() {
		return nil
	}

	return c
}

// The ways the gate handles a request on a copy.
type gateAction int

const (
	gateLocal gateAction = iota
	gateForward
	gateForwardWait
	gateRefuse
)

// gateOf is what a copy does with a request: local (reads, the system
// routes of the daemon), to the owner (writes; reads until the copy caught
// up; watch and streams, that run on the owner only), or refused (a
// restore of the whole database).
func gateOf(method string, segs []string, readsLocal bool) gateAction {
	read := gateLocal

	if !readsLocal {
		read = gateForward
	}

	if strings.HasPrefix(segs[0], "_") {
		switch segs[0] {
		case "_blobs":
			if method == http.MethodPost {
				return gateForward
			}

			return read
		case "_collections":
			return read
		case "_restore":
			return gateRefuse
		}

		return gateLocal
	}

	action := ""

	if len(segs) > 1 {
		action = segs[1]
	}

	switch action {
	case "_watch", "_ack", "_streams", "_restore":
		return gateForward
	case "_compact", "_stats", "_pitr", "_quarantine":
		return gateLocal
	}

	if _, need := required(method, segs); need >= accessWrite {
		return gateForwardWait
	}

	return read
}

// replicate is the gate of a copy, and the position of a forwarded write
// on the owner.
func (s *Server) replicate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		segs, ok := segments(r.URL.EscapedPath())

		if !ok || len(segs) == 0 {
			next.ServeHTTP(w, r)

			return
		}

		c := s.copyOf(headerDatabase(r))

		if c == nil {
			if !strings.HasPrefix(segs[0], "_") {
				if _, need := required(r.Method, segs); need == accessAdmin || r.Header.Get(replication.ForwardedHeader) != "" {
					w = &positionWriter{ResponseWriter: w, s: s, r: r, segs: segs}
				}
			}

			next.ServeHTTP(w, r)

			return
		}

		switch gateOf(r.Method, segs, c.ReadsLocal()) {
		case gateLocal:
			next.ServeHTTP(w, r)
		case gateForward:
			c.Forward(w, r, false)
		case gateForwardWait:
			c.Forward(w, r, true)
		case gateRefuse:
			writeError(w, &apiError{status: http.StatusForbidden, code: "forbidden", msg: "this database is a copy: restore it on the owner"})
		}
	})
}

// positionWriter sets Ndb-Position on the answer of a write that a copy
// forwarded, and moves the catalog version after a change of a
// definition (for every write: a copy streams it).
type positionWriter struct {
	http.ResponseWriter

	s     *Server
	r     *http.Request
	segs  []string
	wrote bool
}

func (p *positionWriter) WriteHeader(status int) {
	if !p.wrote {
		p.wrote = true

		p.mark(status)
	}

	p.ResponseWriter.WriteHeader(status)
}

func (p *positionWriter) Write(b []byte) (int, error) {
	if !p.wrote {
		p.WriteHeader(http.StatusOK)
	}

	return p.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the real writer.
func (p *positionWriter) Unwrap() http.ResponseWriter {
	return p.ResponseWriter
}

func (p *positionWriter) mark(status int) {
	if status >= 300 {
		return
	}

	_, need := required(p.r.Method, p.segs)

	pos := map[string]uint64{}

	if need == accessAdmin {
		pos[replication.CatalogKey] = p.s.repl.gen.Add(1)
	}

	if p.r.Header.Get(replication.ForwardedHeader) == "" || need < accessWrite {
		return
	}

	root := p.s.db.Load()
	if root == nil {
		return
	}

	db, err := root.Database(headerDatabase(p.r), false)

	if headerDatabase(p.r) == "" {
		db, err = root, nil
	}

	if err != nil {
		return
	}

	for _, name := range db.Family(p.segs[0]) {
		pos[name] = db.LastTx(name)
	}

	p.Header().Set(replication.PositionHeader, replication.FormatPositions(pos))
}

// replicateAllows tells if a replicate token may call the route: the
// stream, the snapshot and the blobs (read only).
func replicateAllows(method string, segs []string) bool {
	if method != http.MethodGet || len(segs) != 2 {
		return false
	}

	switch segs[0] {
	case "_replication":
		return segs[1] == "stream" || segs[1] == "snapshot"
	case "_blobs":
		return true
	}

	return false
}

// replicationGet is GET /_replication/{stream|snapshot}.
func (s *Server) replicationGet(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	switch r.PathValue("op") {
	case "stream":
		s.replStream(w, r, db)
	case "snapshot":
		s.replSnapshot(w, r, db)
	default:
		unknownRoute(w)
	}
}

// replicationPost is POST /_replication/{promote|resync}, on the unix
// socket only, for the database of the request.
func (s *Server) replicationPost(w http.ResponseWriter, r *http.Request, _ *engine.DB) {
	if !isUnix(r) {
		writeError(w, &apiError{status: http.StatusForbidden, code: "forbidden", msg: "promote and resync are allowed on the unix socket only"})

		return
	}

	name := headerDatabase(r)
	c := s.copyOf(name)

	if name == "" {
		name = engine.DefaultDatabase
	}

	if c == nil {
		writeError(w, badRequest("the database %s is not a copy", name))

		return
	}

	switch r.PathValue("op") {
	case "promote":
		term, err := c.Promote()
		if err != nil {
			writeError(w, err)

			return
		}

		s.logInfo(w, r, "replication promoted", slog.String("database", name), slog.Uint64("term", term))

		writeJSON(w, http.StatusOK, map[string]any{"database": name, "role": replication.RoleOwner, "term": term})
	case "resync":
		if err := c.Resync(); err != nil {
			writeError(w, err)

			return
		}

		s.logInfo(w, r, "replication resync", slog.String("database", name))

		writeJSON(w, http.StatusOK, map[string]any{"database": name, "state": c.Status().State})
	default:
		unknownRoute(w)
	}
}

// replicationStatus is GET /_replication: the role of each database.
func (s *Server) replicationStatus(w http.ResponseWriter, _ *http.Request, root *engine.DB) {
	type dbStatus struct {
		Database string `json:"database"`
		Role     string `json:"role"`
		Term     uint64 `json:"term"`

		*replication.Status

		Copies []peer `json:"copies,omitempty"`
	}

	names := append([]string{engine.DefaultDatabase}, root.Databases()...)
	out := make([]dbStatus, 0, len(names))

	for _, name := range names {
		key := name

		if key == engine.DefaultDatabase {
			key = ""
		}

		if c := s.copyOf(key); c != nil {
			st := c.Status()

			out = append(out, dbStatus{Database: name, Role: replication.RoleCopy, Term: st.Term, Status: &st})

			continue
		}

		db := root

		if key != "" {
			var err error

			if db, err = root.Database(key, false); err != nil {
				continue
			}
		}

		st, _, _ := replication.LoadState(db.Dir())

		out = append(out, dbStatus{Database: name, Role: replication.RoleOwner, Term: st.Term, Copies: s.peersOf(name)})
	}

	writeJSON(w, http.StatusOK, map[string]any{"databases": out})
}

func (s *Server) peersOf(name string) []peer {
	s.repl.peersMu.Lock()
	defer s.repl.peersMu.Unlock()

	var out []peer

	for _, p := range s.repl.peers {
		if p.Database == name {
			out = append(out, p)
		}
	}

	slices.SortFunc(out, func(a, b peer) int { return a.Since.Compare(b.Since) })

	return out
}

func (s *Server) addPeer(p peer) func() {
	s.repl.peersMu.Lock()
	defer s.repl.peersMu.Unlock()

	if s.repl.peers == nil {
		s.repl.peers = map[int]peer{}
	}

	id := s.repl.nextID
	s.repl.nextID++
	s.repl.peers[id] = p

	return func() {
		s.repl.peersMu.Lock()
		defer s.repl.peersMu.Unlock()

		delete(s.repl.peers, id)
	}
}

// replStream is the change stream of the database of the request, after
// the positions of from ("users=12,posts=3"; a collection that is not
// there starts at its first record). A position whose WAL is gone is 410
// position_gone, with the collections in Ndb-Gone.
func (s *Server) replStream(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	from, err := replication.ParsePositions(r.URL.Query().Get("from"))
	if err != nil {
		writeError(w, badRequest("%v", err))

		return
	}

	st := &replStreamer{s: s, db: db, from: from, out: make(chan replication.Line, 256), pumps: map[string]context.CancelFunc{}}

	defs := db.EngineDefs()
	opened, gone, err := st.openAll(defs)

	if err != nil {
		writeError(w, err)

		return
	}

	if len(gone) > 0 {
		w.Header().Set(replication.GoneHeader, strings.Join(gone, ","))
		writeError(w, &apiError{status: http.StatusGone, code: "position_gone", msg: "the WAL of the position of " + strings.Join(gone, ", ") + " was deleted: take a snapshot (GET /_replication/snapshot)"})

		return
	}

	name := databaseOf(r).name

	if name == "" {
		name = engine.DefaultDatabase
	}

	defer s.addPeer(peer{Database: name, Addr: s.clientIP(r), Since: time.Now()})()

	key := db.Key()

	s.log.LogAttrs(r.Context(), slog.LevelInfo, "replication: a copy connected", slog.String("database", name), slog.String("addr", s.clientIP(r)))

	if key.Source == "made" {
		s.log.LogAttrs(r.Context(), slog.LevelWarn, "replication: the encryption key is kept in the data dir of this owner; if its disk is lost, the copies have the data and no key: set NDB_ENCRYPTION_KEY from a secret store", slog.String("database", name))
	}

	state, _, _ := replication.LoadState(db.Dir())
	hello := replication.Hello{Database: name, Term: state.Term, Key: key.Fingerprint(), Promoted: state.Promoted}

	longPoll(w)

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	defer context.AfterFunc(s.stopCtx, cancel)()

	st.ctx = ctx
	st.enc = json.NewEncoder(w)
	st.rc = http.NewResponseController(w)

	gen := s.repl.gen.Load()

	if st.send(replication.Line{Hello: &hello}) != nil || st.send(replication.Line{Catalog: &replication.Catalog{Gen: gen, Defs: defs}}) != nil || st.rc.Flush() != nil {
		return
	}

	for name, rs := range opened {
		st.start(name, rs, from[name])
	}

	st.loop(gen, defs)

	cancel()
	st.wg.Wait()
}

// replStreamer is one change stream to one copy.
type replStreamer struct {
	s    *Server
	db   *engine.DB
	from map[string]uint64
	ctx  context.Context
	enc  *json.Encoder
	rc   *http.ResponseController
	out  chan replication.Line
	wg   sync.WaitGroup

	pumps map[string]context.CancelFunc
}

// openAll opens the records of each collection after its position; gone
// lists the ones whose position is no longer in the WAL.
func (st *replStreamer) openAll(defs []catalog.Def) (map[string]*engine.RecordStream, []string, error) {
	opened := map[string]*engine.RecordStream{}

	var gone []string

	for _, d := range defs {
		rs, err := st.open(d.Name, st.from[d.Name])

		if errors.Is(err, feed.ErrHistoryTruncated) {
			gone = append(gone, d.Name)

			continue
		}

		if err != nil {
			for _, rs := range opened {
				rs.Close()
			}

			return nil, nil, err
		}

		opened[d.Name] = rs
	}

	if len(gone) > 0 {
		for _, rs := range opened {
			rs.Close()
		}
	}

	return opened, gone, nil
}

// open is OpenRecords, and history_truncated for a position after the last
// tx (a restore moved the collection back in time).
func (st *replStreamer) open(name string, from uint64) (*engine.RecordStream, error) {
	if from > st.db.LastTx(name) {
		return nil, feed.ErrHistoryTruncated
	}

	return st.db.OpenRecords(name, from)
}

func (st *replStreamer) send(l replication.Line) error {
	_ = st.rc.SetWriteDeadline(time.Now().Add(writeTimeout))

	return st.enc.Encode(l)
}

// loop writes the lines of the pumps, the heartbeats and the catalog
// changes until the client leaves or a line fails.
func (st *replStreamer) loop(gen uint64, defs []catalog.Def) {
	every := st.s.repl.heartbeat

	if every <= 0 {
		every = defaultReplHeartbeat
	}

	beat := time.NewTicker(every)
	defer beat.Stop()

	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()

	for {
		select {
		case <-st.ctx.Done():
			return
		case l := <-st.out:
			if st.send(l) != nil {
				return
			}

			if len(l.Gone) > 0 {
				_ = st.rc.Flush()

				return
			}

			if len(st.out) == 0 && st.rc.Flush() != nil {
				return
			}
		case <-beat.C:
			if !st.catalog(&gen, &defs, true) || !st.heartbeat(defs) {
				return
			}
		case <-poll.C:
			if !st.catalog(&gen, &defs, false) {
				return
			}
		}
	}
}

// catalog sends the definitions when the catalog version moved, or (with
// full) when they changed, and starts or stops the pumps of the
// collections that came or went.
func (st *replStreamer) catalog(gen *uint64, defs *[]catalog.Def, full bool) bool {
	now := st.s.repl.gen.Load()

	if now == *gen && !full {
		return true
	}

	next := st.db.EngineDefs()

	if now == *gen && sameDefs(*defs, next) {
		return true
	}

	*gen, *defs = now, next

	if st.send(replication.Line{Catalog: &replication.Catalog{Gen: now, Defs: next}}) != nil {
		return false
	}

	names := map[string]bool{}

	for _, d := range next {
		names[d.Name] = true

		if _, ok := st.pumps[d.Name]; ok {
			continue
		}

		rs, err := st.open(d.Name, 0)
		if err != nil {
			_ = st.send(replication.Line{Gone: []string{d.Name}})
			_ = st.rc.Flush()

			return false
		}

		st.start(d.Name, rs, 0)
	}

	for name, stop := range st.pumps {
		if !names[name] {
			stop()
			delete(st.pumps, name)
		}
	}

	return st.rc.Flush() == nil
}

func sameDefs(a, b []catalog.Def) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)

	return string(ja) == string(jb)
}

func (st *replStreamer) heartbeat(defs []catalog.Def) bool {
	last := make(map[string]uint64, len(defs))

	for _, d := range defs {
		last[d.Name] = st.db.LastTx(d.Name)
	}

	return st.send(replication.Line{Heartbeat: &replication.Heartbeat{TS: time.Now().UTC(), Last: last}}) == nil && st.rc.Flush() == nil
}

// start runs the pump of one collection.
func (st *replStreamer) start(name string, rs *engine.RecordStream, pos uint64) {
	ctx, cancel := context.WithCancel(st.ctx)

	st.pumps[name] = cancel

	st.wg.Add(1)

	go func() {
		defer st.wg.Done()

		st.pump(ctx, name, rs, pos)
	}()
}

// pump reads the records of one collection into out. When its stream ends
// (a truncate, a rename, a slow copy) it opens it again from the last
// position; a position that is gone ends the stream with a gone line.
func (st *replStreamer) pump(ctx context.Context, name string, rs *engine.RecordStream, pos uint64) {
	for {
		rec, err := rs.Next(ctx)

		if err == nil {
			pos = rec.Tx

			if !st.put(ctx, replication.Line{Record: recordLine(name, rec)}) {
				rs.Close()

				return
			}

			continue
		}

		rs.Close()

		if rs = st.reopen(ctx, name, pos); rs == nil {
			return
		}
	}
}

// reopen opens the records of name after pos again, or gives nil when the
// pump ends: ctx ended, the collection is gone, or its position is gone.
func (st *replStreamer) reopen(ctx context.Context, name string, pos uint64) *engine.RecordStream {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(20 * time.Millisecond):
		}

		rs, err := st.open(name, pos)

		switch {
		case err == nil:
			return rs
		case errors.Is(err, catalog.ErrUnknown):
			return nil
		case errors.Is(err, feed.ErrHistoryTruncated):
			st.put(ctx, replication.Line{Gone: []string{name}})

			return nil
		}
	}
}

func (st *replStreamer) put(ctx context.Context, l replication.Line) bool {
	select {
	case st.out <- l:
		return true
	case <-ctx.Done():
		return false
	}
}

func recordLine(name string, rec engine.Record) *replication.Record {
	out := &replication.Record{C: name, Tx: rec.Tx, TS: rec.TS.UTC()}

	for _, op := range rec.Ops {
		out.Ops = append(out.Ops, replication.Op{Op: string(op.Kind), ID: op.ID, V: op.V, Doc: op.Doc})
	}

	return out
}

// snapshotBatch is the count of documents in one record of a snapshot.
const snapshotBatch = 500

// replSnapshot is the live documents of one collection at one tx: its
// definition, then records of put ops, then an end line with the tx. The
// copy then reads the stream from that tx. It has no settings: the key
// never goes over the network.
func (s *Server) replSnapshot(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	name := r.URL.Query().Get("collection")

	def, ok := db.Definition(name)

	if !ok || def.Write == catalog.Direct {
		writeError(w, &apiError{status: http.StatusNotFound, code: "unknown_collection", msg: "no engine-mode collection " + name})

		return
	}

	longPoll(w)

	enc := json.NewEncoder(w)
	started := false
	docs := 0

	var (
		batch []replication.Op
		at    uint64
	)

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}

		err := enc.Encode(replication.Line{Record: &replication.Record{C: name, Ops: batch}})

		batch = batch[:0]

		return err
	}

	err := db.ReplicaSnapshot(r.Context(), name, func(tx uint64) error {
		at = tx
		started = true

		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)

		return enc.Encode(replication.Line{Catalog: &replication.Catalog{Defs: []catalog.Def{def}}})
	}, func(op wal.Op) error {
		docs++

		batch = append(batch, replication.Op{Op: string(op.Kind), ID: op.ID, V: op.V, Doc: op.Doc})

		if len(batch) < snapshotBatch {
			return nil
		}

		return flush()
	})

	if err == nil {
		err = flush()
	}

	if err == nil {
		err = enc.Encode(replication.Line{End: &replication.End{Tx: at, Docs: docs}})
	}

	if err == nil {
		return
	}

	if !started {
		writeError(w, err)

		return
	}

	panic(http.ErrAbortHandler)
}
