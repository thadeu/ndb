package ndb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// Group starts, for Group.Create and Group.SetID.
const (
	// StartNew: the group reads only the entries appended after it ($).
	StartNew = "$"

	// StartAll: the group reads every entry of the stream (0).
	StartAll = "0"
)

// Stream is one stream of entries of type T: an append-only log with
// consumer groups, with the behavior of Redis Streams (see
// docs/dev/streams.md). T is a struct or map[string]any; the entry ID and
// time are in Entry, not in T.
type Stream[T any] struct {
	db     *DB
	name   string
	schema *schema
}

// StreamOf returns the stream name for entries of type T. It sends nothing:
// the first append, or the first group create, makes the stream.
func StreamOf[T any](db *DB, name string) (*Stream[T], error) {
	st := &Stream[T]{db: db, name: db.prefix + name}

	// A struct maps its fields with the ndb tags; a map or another type
	// goes through encoding/json.
	if t := reflect.TypeFor[T](); t.Kind() == reflect.Struct {
		s, err := schemaOf(t)

		if err != nil {
			return nil, err
		}

		st.schema = s
	}

	return st, nil
}

// Name is the stream name.
func (s *Stream[T]) Name() string {
	return s.name
}

// Entry is one entry of a stream.
type Entry[T any] struct {
	// ID is "<ms>-<seq>".
	ID string

	// Time is the ms of the ID.
	Time time.Time

	Doc T

	// Deleted: the entry was pending, but it is no longer in the stream (a
	// trim or a delete). Doc is the zero value.
	Deleted bool

	raw json.RawMessage
}

// Raw is the entry as the daemon sent it, with _id and _ts.
func (e Entry[T]) Raw() json.RawMessage {
	return e.raw
}

// IDTime is the time of an entry ID: its ms part. A bad ID gives the zero
// time.
func IDTime(id string) time.Time {
	ms, _, ok := strings.Cut(id, "-")

	if !ok {
		return time.Time{}
	}

	n, err := strconv.ParseInt(ms, 10, 64)

	if err != nil {
		return time.Time{}
	}

	return time.UnixMilli(n).UTC()
}

// Create makes the stream, or checks that name is one. Append and
// Group.Create make it too; Create is for a stream with options.
func (s *Stream[T]) Create(ctx context.Context) error {
	return s.call(ctx, http.MethodPut, "", map[string]any{"stream": true}, nil)
}

// AppendOption changes an Append.
type AppendOption func(*appendBody)

// WithEntryID sets the ID of the entry: "<ms>-<seq>" or "<ms>-*". The
// default is "*": the daemon makes it from its clock.
func WithEntryID(id string) AppendOption {
	return func(b *appendBody) { b.ID = id }
}

// WithTrim trims the stream after the append, as Trim does.
func WithTrim(t Trim) AppendOption {
	return func(b *appendBody) { b.Trim = &t }
}

// NoCreate makes the append fail with ErrUnknownCollection when the stream
// does not exist (NOMKSTREAM).
func NoCreate() AppendOption {
	return func(b *appendBody) { b.NoCreate = true }
}

type appendBody struct {
	Doc      json.RawMessage   `json:"doc,omitempty"`
	Docs     []json.RawMessage `json:"docs,omitempty"`
	ID       string            `json:"id,omitempty"`
	NoCreate bool              `json:"no_create,omitempty"`
	Trim     *Trim             `json:"trim,omitempty"`
}

// Append adds one entry and returns its ID (XADD). With the default
// durability, the entry is on disk before Append returns.
func (s *Stream[T]) Append(ctx context.Context, doc T, opts ...AppendOption) (string, error) {
	raw, err := s.encode(doc)

	if err != nil {
		return "", err
	}

	body := appendBody{Doc: raw}

	for _, opt := range opts {
		opt(&body)
	}

	var out struct {
		ID string `json:"_id"`
	}

	err = s.call(ctx, http.MethodPost, "_streams/append", body, &out)

	return out.ID, err
}

// AppendMany adds the entries in order, with one fsync for all, and returns
// their IDs. WithEntryID applies to the first entry.
func (s *Stream[T]) AppendMany(ctx context.Context, docs []T, opts ...AppendOption) ([]string, error) {
	body := appendBody{Docs: make([]json.RawMessage, 0, len(docs))}

	for _, doc := range docs {
		raw, err := s.encode(doc)

		if err != nil {
			return nil, err
		}

		body.Docs = append(body.Docs, raw)
	}

	for _, opt := range opts {
		opt(&body)
	}

	var out struct {
		IDs []string `json:"ids"`
	}

	err := s.call(ctx, http.MethodPost, "_streams/append", body, &out)

	return out.IDs, err
}

// Trim says which entries a trim deletes: MaxLen or MinID.
type Trim struct {
	// MaxLen keeps the newest MaxLen entries.
	MaxLen *int64 `json:"max_len,omitempty"`

	// MinID deletes the entries with an ID lower than MinID.
	MinID string `json:"min_id,omitempty"`

	// Approx deletes only whole blocks of 1 024 entries (~ in Redis).
	Approx bool `json:"approx,omitempty"`

	// Limit: with Approx, delete at most Limit entries.
	Limit int `json:"limit,omitempty"`
}

// MaxLen is a Trim that keeps the newest n entries, in blocks of 1 024 when
// approx is true.
func MaxLen(n int64, approx bool) Trim {
	return Trim{MaxLen: &n, Approx: approx}
}

// MinID is a Trim that deletes the entries with an ID lower than id.
func MinID(id string, approx bool) Trim {
	return Trim{MinID: id, Approx: approx}
}

// Trim deletes old entries and returns how many it deleted (XTRIM).
func (s *Stream[T]) Trim(ctx context.Context, t Trim) (int, error) {
	var out struct {
		Deleted int `json:"deleted"`
	}

	err := s.call(ctx, http.MethodPost, "_streams/trim", t, &out)

	return out.Deleted, err
}

// StreamInfo is the state of a stream (XINFO STREAM).
type StreamInfo struct {
	Length       int    `json:"length"`
	FirstID      string `json:"first_id"`
	LastID       string `json:"last_id"`
	EntriesAdded int64  `json:"entries_added"`
	MaxDeletedID string `json:"max_deleted_id"`
	Groups       int    `json:"groups"`
}

// Info returns the state of the stream.
func (s *Stream[T]) Info(ctx context.Context) (StreamInfo, error) {
	var out StreamInfo

	err := s.call(ctx, http.MethodGet, "_streams", nil, &out)

	return out, err
}

// GroupInfo is the state of a group (XINFO GROUPS). Lag is nil when it
// cannot be known.
type GroupInfo struct {
	Name            string `json:"name"`
	Consumers       int    `json:"consumers"`
	Pending         int    `json:"pending"`
	LastDeliveredID string `json:"last_delivered_id"`
	EntriesRead     *int64 `json:"entries_read"`
	Lag             *int64 `json:"lag"`
}

// Groups returns the groups of the stream.
func (s *Stream[T]) Groups(ctx context.Context) ([]GroupInfo, error) {
	var out []GroupInfo

	err := s.call(ctx, http.MethodGet, "_streams/groups", nil, &out)

	return out, err
}

// Range returns up to count entries with an ID from start to end, both
// included, in ID order (XRANGE). "-" and "+" are the lowest and the
// highest ID; "(id" leaves that ID out. count 0 means the default of
// _search.
func (s *Stream[T]) Range(ctx context.Context, start, end string, count int) ([]Entry[T], error) {
	return s.rangeOf(ctx, start, end, count, 1)
}

// RevRange is Range from end down to start (XREVRANGE).
func (s *Stream[T]) RevRange(ctx context.Context, end, start string, count int) ([]Entry[T], error) {
	return s.rangeOf(ctx, start, end, count, -1)
}

func (s *Stream[T]) rangeOf(ctx context.Context, start, end string, count, order int) ([]Entry[T], error) {
	lowOp, low := rangeBound(start, "$gte", "$gt")
	highOp, high := rangeBound(end, "$lte", "$lt")

	body := map[string]any{
		"query": map[string]any{"_id": map[string]any{lowOp: low, highOp: high}},
		"sort":  map[string]any{"_id": order},
	}

	if count > 0 {
		body["limit"] = count
	}

	return s.rows(ctx, "_search", body)
}

// rangeBound gives the operator and the ID of one end of a range: "(id"
// leaves the ID out, as in Redis.
func rangeBound(id, included, excluded string) (string, string) {
	if rest, ok := strings.CutPrefix(id, "("); ok {
		return excluded, rest
	}

	return included, id
}

// Tail says what Stream.Tail gives.
type Tail struct {
	// After gives the entries with an ID greater than this one. The default
	// is "0", the start of the stream. StartNew ("$") is the last ID when
	// the call starts: only later entries come.
	After string `json:"after,omitempty"`

	// Count is the most entries to give: default 10, at most 10 000.
	Count int `json:"count,omitempty"`

	// Block waits up to this long for a new entry when there is none.
	Block time.Duration `json:"-"`

	// Fields gives only these fields (and _id).
	Fields []string `json:"fields,omitempty"`
}

type tailBody struct {
	Tail
	BlockMs int64 `json:"block_ms,omitempty"`
}

// Tail gives the entries after t.After, in ID order, without a group
// (XREAD). It changes no group and no pending list. An empty answer is an
// empty slice. To read on, send the ID of the last entry as After; do not
// send StartNew again, or an entry appended between two calls is lost.
func (s *Stream[T]) Tail(ctx context.Context, t Tail) ([]Entry[T], error) {
	body := tailBody{Tail: t, BlockMs: t.Block.Milliseconds()}

	return s.rows(ctx, "_streams/read", body)
}

// Delete removes one entry by ID (XDEL) and returns 1, or 0 when it is not
// there.
func (s *Stream[T]) Delete(ctx context.Context, id string) (int, error) {
	var out struct {
		Deleted int `json:"deleted"`
	}

	err := s.call(ctx, http.MethodDelete, docPath("_doc", id), nil, &out)

	return out.Deleted, err
}

// Group returns the consumer group name of the stream. It sends nothing.
func (s *Stream[T]) Group(name string) *Group[T] {
	return &Group[T]{s: s, name: name}
}

// call sends one request to /{stream}/{path} with a JSON body (nil: none).
func (s *Stream[T]) call(ctx context.Context, method, path string, body, out any) error {
	var raw []byte

	if body != nil {
		var err error

		if raw, err = json.Marshal(body); err != nil {
			return err
		}
	}

	target := collPath(s.name)

	if path != "" {
		target += "/" + path
	}

	resp, err := s.db.do(ctx, method, target, raw)

	if err != nil {
		return err
	}

	return decodeBody(resp, out)
}

// rows sends one request whose answer is NDJSON entries.
func (s *Stream[T]) rows(ctx context.Context, path string, body any) ([]Entry[T], error) {
	raw, err := json.Marshal(body)

	if err != nil {
		return nil, err
	}

	resp, err := s.db.do(ctx, http.MethodPost, collPath(s.name)+"/"+path, raw)

	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	out := []Entry[T]{}

	for line, err := range ndjsonRows(resp.Body) {
		if err != nil {
			return nil, err
		}

		e, err := s.entry(line)

		if err != nil {
			return nil, err
		}

		out = append(out, e)
	}

	return out, nil
}

// entry decodes one entry: its _id and _deleted, then T.
func (s *Stream[T]) entry(raw json.RawMessage) (Entry[T], error) {
	var meta struct {
		ID      string `json:"_id"`
		Deleted bool   `json:"_deleted"`
	}

	if err := json.Unmarshal(raw, &meta); err != nil {
		return Entry[T]{}, fmt.Errorf("ndb: stream entry: %w", err)
	}

	e := Entry[T]{ID: meta.ID, Time: IDTime(meta.ID), Deleted: meta.Deleted, raw: raw}

	if meta.Deleted {
		return e, nil
	}

	if err := s.decode(raw, &e.Doc); err != nil {
		return Entry[T]{}, err
	}

	return e, nil
}

func (s *Stream[T]) encode(doc T) (json.RawMessage, error) {
	if s.schema == nil {
		return json.Marshal(doc)
	}

	return s.schema.encode(doc)
}

func (s *Stream[T]) decode(raw json.RawMessage, dst *T) error {
	if s.schema == nil {
		return json.Unmarshal(raw, dst)
	}

	return s.schema.decode(raw, reflect.ValueOf(dst).Elem())
}

// Group is one consumer group of a stream. Each entry goes to one consumer
// of the group and stays pending until it is acked.
type Group[T any] struct {
	s    *Stream[T]
	name string
}

// Name is the group name.
func (g *Group[T]) Name() string {
	return g.name
}

func (g *Group[T]) path(parts ...string) string {
	p := "_streams/groups/" + url.PathEscape(g.name)

	for _, part := range parts {
		p += "/" + part
	}

	return p
}

// GroupOption changes a Group.Create.
type GroupOption func(*groupBody)

// GroupNoCreate makes Group.Create fail with ErrUnknownCollection when the
// stream does not exist (XGROUP CREATE without MKSTREAM).
func GroupNoCreate() GroupOption {
	return func(b *groupBody) { b.NoCreate = true }
}

type groupBody struct {
	Start    string `json:"start"`
	NoCreate bool   `json:"no_create,omitempty"`
}

// Create makes the group (XGROUP CREATE ... MKSTREAM). start is StartNew,
// StartAll or an ID. A group that exists: ErrGroupExists.
func (g *Group[T]) Create(ctx context.Context, start string, opts ...GroupOption) error {
	body := groupBody{Start: start}

	for _, opt := range opts {
		opt(&body)
	}

	return g.s.call(ctx, http.MethodPut, g.path(), body, nil)
}

// Ensure makes the group, or does nothing when it exists.
func (g *Group[T]) Ensure(ctx context.Context, start string) error {
	err := g.Create(ctx, start)

	if errors.Is(err, ErrGroupExists) {
		return nil
	}

	return err
}

// SetID moves the last delivered ID of the group (XGROUP SETID).
func (g *Group[T]) SetID(ctx context.Context, id string) error {
	return g.s.call(ctx, http.MethodPost, g.path("setid"), map[string]any{"id": id}, nil)
}

// Destroy deletes the group and its pending entries (XGROUP DESTROY).
func (g *Group[T]) Destroy(ctx context.Context) error {
	return g.s.call(ctx, http.MethodDelete, g.path(), nil, nil)
}

// CreateConsumer makes a consumer and reports whether it was new. A read
// makes the consumer too.
func (g *Group[T]) CreateConsumer(ctx context.Context, consumer string) (bool, error) {
	var out struct {
		Created bool `json:"created"`
	}

	err := g.s.call(ctx, http.MethodPut, g.path("consumers", url.PathEscape(consumer)), nil, &out)

	return out.Created, err
}

// DeleteConsumer deletes a consumer and its pending entries, and returns how
// many it held (XGROUP DELCONSUMER).
func (g *Group[T]) DeleteConsumer(ctx context.Context, consumer string) (int, error) {
	var out struct {
		Pending int `json:"pending"`
	}

	err := g.s.call(ctx, http.MethodDelete, g.path("consumers", url.PathEscape(consumer)), nil, &out)

	return out.Pending, err
}

// ConsumerInfo is the state of a consumer (XINFO CONSUMERS).
type ConsumerInfo struct {
	Name       string `json:"name"`
	Pending    int    `json:"pending"`
	IdleMs     int64  `json:"idle_ms"`
	InactiveMs int64  `json:"inactive_ms"`
}

// Consumers returns the consumers of the group.
func (g *Group[T]) Consumers(ctx context.Context) ([]ConsumerInfo, error) {
	var out []ConsumerInfo

	err := g.s.call(ctx, http.MethodGet, g.path("consumers"), nil, &out)

	return out, err
}

// Read says what Group.Read gives.
type Read struct {
	// Count is the most entries to give: default 10, at most 10 000.
	Count int `json:"count,omitempty"`

	// Block waits up to this long for a new entry when there is none.
	Block time.Duration `json:"-"`

	// Pending gives the consumer's own pending entries, with an ID greater
	// than After, instead of new ones.
	Pending bool   `json:"pending,omitempty"`
	After   string `json:"after,omitempty"`

	// NoAck does not add the entries to the pending list: at most once.
	NoAck bool `json:"no_ack,omitempty"`

	// Fields gives only these fields (and _id).
	Fields []string `json:"fields,omitempty"`
}

type readBody struct {
	Consumer string `json:"consumer"`
	Read
	BlockMs int64 `json:"block_ms,omitempty"`
}

// Read gives entries to consumer (XREADGROUP): new ones by default, which
// become pending for consumer, or its pending ones with r.Pending. An empty
// answer is an empty slice.
func (g *Group[T]) Read(ctx context.Context, consumer string, r Read) ([]Entry[T], error) {
	body := readBody{Consumer: consumer, Read: r, BlockMs: r.Block.Milliseconds()}

	return g.s.rows(ctx, g.path("read"), body)
}

// Ack ends the work on the entries and returns how many were pending
// (XACK). An ID that is not pending counts 0.
func (g *Group[T]) Ack(ctx context.Context, ids ...string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}

	var out struct {
		Acked int `json:"acked"`
	}

	err := g.s.call(ctx, http.MethodPost, g.path("ack"), map[string]any{"ids": ids}, &out)

	return out.Acked, err
}

// PendingSummary is the pending list in short (XPENDING).
type PendingSummary struct {
	Count     int     `json:"count"`
	Lowest    *string `json:"lowest"`
	Highest   *string `json:"highest"`
	Consumers []struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	} `json:"consumers"`
}

// Pending returns the pending list in short.
func (g *Group[T]) Pending(ctx context.Context) (PendingSummary, error) {
	var out PendingSummary

	err := g.s.call(ctx, http.MethodPost, g.path("pending"), map[string]any{}, &out)

	return out, err
}

// PendingQuery says which pending entries PendingList gives.
type PendingQuery struct {
	// Start and End: the ID range; default "-" and "+".
	Start string `json:"start"`
	End   string `json:"end"`

	// Count is the most entries to give; default 100.
	Count int `json:"count"`

	// Consumer gives only the entries of this consumer.
	Consumer string `json:"consumer,omitempty"`

	// Idle gives only the entries idle for at least this long.
	Idle time.Duration `json:"-"`
}

type pendingBody struct {
	PendingQuery
	IdleMs int64 `json:"idle_ms,omitempty"`
}

// PendingEntry is one pending entry.
type PendingEntry struct {
	ID         string `json:"_id"`
	Consumer   string `json:"consumer"`
	IdleMs     int64  `json:"idle_ms"`
	Deliveries int64  `json:"deliveries"`
}

// PendingList returns pending entries, in ID order (XPENDING with a range).
func (g *Group[T]) PendingList(ctx context.Context, q PendingQuery) ([]PendingEntry, error) {
	if q.Start == "" {
		q.Start = "-"
	}

	if q.End == "" {
		q.End = "+"
	}

	if q.Count <= 0 {
		q.Count = 100
	}

	var out []PendingEntry

	err := g.s.call(ctx, http.MethodPost, g.path("pending"), pendingBody{PendingQuery: q, IdleMs: q.Idle.Milliseconds()}, &out)

	return out, err
}

// Claimed is the answer of Claim and AutoClaim.
type Claimed[T any] struct {
	// Cursor is where the next AutoClaim starts; "0-0" when the scan is
	// complete. Empty for Claim.
	Cursor string

	Entries []Entry[T]

	// Deleted: pending IDs whose entry is no longer in the stream. They
	// left the pending list.
	Deleted []string
}

type claimBody struct {
	Consumer  string   `json:"consumer"`
	MinIdleMs int64    `json:"min_idle_ms"`
	IDs       []string `json:"ids,omitempty"`
	Cursor    string   `json:"cursor,omitempty"`
	Count     int      `json:"count,omitempty"`
	JustIDs   bool     `json:"just_ids,omitempty"`
}

// Claim moves the entries idle for at least minIdle to consumer (XCLAIM).
// Each one gets one more delivery.
func (g *Group[T]) Claim(ctx context.Context, consumer string, minIdle time.Duration, ids ...string) (Claimed[T], error) {
	return g.claim(ctx, claimBody{Consumer: consumer, MinIdleMs: minIdle.Milliseconds(), IDs: ids})
}

// AutoClaim scans the pending list from cursor ("0-0" at the start) and
// moves up to count entries idle for at least minIdle to consumer
// (XAUTOCLAIM).
func (g *Group[T]) AutoClaim(ctx context.Context, consumer string, minIdle time.Duration, cursor string, count int) (Claimed[T], error) {
	if cursor == "" {
		cursor = "0-0"
	}

	return g.claim(ctx, claimBody{Consumer: consumer, MinIdleMs: minIdle.Milliseconds(), Cursor: cursor, Count: count})
}

func (g *Group[T]) claim(ctx context.Context, body claimBody) (Claimed[T], error) {
	var out struct {
		Cursor  *string           `json:"cursor"`
		Entries []json.RawMessage `json:"entries"`
		Deleted []string          `json:"deleted"`
	}

	if err := g.s.call(ctx, http.MethodPost, g.path("claim"), body, &out); err != nil {
		return Claimed[T]{}, err
	}

	c := Claimed[T]{Deleted: out.Deleted, Entries: make([]Entry[T], 0, len(out.Entries))}

	if out.Cursor != nil {
		c.Cursor = *out.Cursor
	}

	for _, raw := range out.Entries {
		e, err := g.s.entry(raw)

		if err != nil {
			return Claimed[T]{}, err
		}

		c.Entries = append(c.Entries, e)
	}

	return c, nil
}

// ClaimedIDs is the answer of ClaimIDs and AutoClaimIDs.
type ClaimedIDs struct {
	// Cursor is where the next AutoClaimIDs starts; "0-0" when the scan is
	// complete. Empty for ClaimIDs.
	Cursor string

	IDs []string

	// Deleted: pending IDs whose entry is no longer in the stream. They
	// left the pending list.
	Deleted []string
}

// ClaimIDs is Claim that gives only the IDs (XCLAIM ... JUSTID). The
// delivery count of the entries does not change.
func (g *Group[T]) ClaimIDs(ctx context.Context, consumer string, minIdle time.Duration, ids ...string) (ClaimedIDs, error) {
	return g.claimIDs(ctx, claimBody{Consumer: consumer, MinIdleMs: minIdle.Milliseconds(), IDs: ids, JustIDs: true})
}

// AutoClaimIDs is AutoClaim that gives only the IDs (XAUTOCLAIM ...
// JUSTID). The delivery count of the entries does not change.
func (g *Group[T]) AutoClaimIDs(ctx context.Context, consumer string, minIdle time.Duration, cursor string, count int) (ClaimedIDs, error) {
	if cursor == "" {
		cursor = "0-0"
	}

	return g.claimIDs(ctx, claimBody{Consumer: consumer, MinIdleMs: minIdle.Milliseconds(), Cursor: cursor, Count: count, JustIDs: true})
}

func (g *Group[T]) claimIDs(ctx context.Context, body claimBody) (ClaimedIDs, error) {
	var out struct {
		Cursor  *string  `json:"cursor"`
		Entries []string `json:"entries"`
		Deleted []string `json:"deleted"`
	}

	if err := g.s.call(ctx, http.MethodPost, g.path("claim"), body, &out); err != nil {
		return ClaimedIDs{}, err
	}

	c := ClaimedIDs{IDs: out.Entries, Deleted: out.Deleted}

	if out.Cursor != nil {
		c.Cursor = *out.Cursor
	}

	return c, nil
}
