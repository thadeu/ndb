package ndb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"time"
)

// Coll is one collection of documents of type T.
type Coll[T any] struct {
	db     *DB
	name   string
	schema *schema
}

// Collection returns the collection name for documents of type T. It sends
// nothing: the first call on the collection declares it (PUT /{coll}) with
// the time, expires, unique and index tags of T, once per name and DB.
//
// A name with a "*" at the end is a collection pattern ("logs-2026-*"): it
// reads every collection whose name starts with the prefix, declares
// nothing, and its writes fail with pattern_read_only. Each row carries
// "_coll", the name of its collection: map it with a field tagged
// `ndb:"_coll"`.
func Collection[T any](db *DB, name string) (*Coll[T], error) {
	s, err := schemaOf(reflect.TypeFor[T]())
	if err != nil {
		return nil, err
	}

	name = db.prefix + name

	db.use(name)

	return &Coll[T]{db: db, name: name, schema: s}, nil
}

// Name is the collection name (or pattern).
func (c *Coll[T]) Name() string {
	return c.name
}

// ready declares the collection on its first use.
func (c *Coll[T]) ready(ctx context.Context) error {
	if strings.Contains(c.name, "*") {
		return nil
	}

	return c.db.ensure(ctx, c.name, c.schema)
}

// Result is the _id and version of a written document. An empty _id field is
// not sent, so the daemon generates the id and Result carries it. Insert
// fails with ErrDuplicateID when an _id exists.
type Result struct {
	ID string `json:"_id"`
	V  uint64 `json:"_v"`
}

// Insert writes one document (POST /{coll}/_doc). With ExpireIn the
// document expires d from now.
func (c *Coll[T]) Insert(ctx context.Context, doc T, opts ...Option) (Result, error) {
	o := newOptions(opts)

	if o.expireIn > 0 {
		res, err := c.InsertMany(ctx, []T{doc}, opts...)
		if err != nil {
			return Result{}, err
		}

		return res[0], nil
	}

	raw, err := c.schema.encode(doc)
	if err != nil {
		return Result{}, err
	}

	var out Result

	return out, c.call(ctx, http.MethodPost, "_doc", raw, &out)
}

// InsertMany writes docs in one transaction, all or nothing
// (POST /{coll}/_bulk). ExpireIn applies to every document.
func (c *Coll[T]) InsertMany(ctx context.Context, docs []T, opts ...Option) ([]Result, error) {
	return c.bulk(ctx, docs, false, opts)
}

func (c *Coll[T]) bulk(ctx context.Context, docs []T, upsert bool, opts []Option) ([]Result, error) {
	o := newOptions(opts)
	rows := make([]json.RawMessage, len(docs))

	for i, d := range docs {
		raw, err := c.schema.encode(d)
		if err != nil {
			return nil, err
		}

		rows[i] = raw
	}

	body, err := json.Marshal(bulkBody{Docs: rows, ExpireIn: o.expireIn.seconds(), Upsert: upsert})
	if err != nil {
		return nil, err
	}

	var out struct {
		Results []Result `json:"results"`
	}

	return out.Results, c.call(ctx, http.MethodPost, "_bulk", body, &out)
}

// Upsert writes doc by its _id: it creates the document when the _id is
// free, and merges doc into the stored one when it exists (every field of T
// is set; fields of the stored document that T does not have stay), like
// doc_as_upsert in Elasticsearch. An empty _id is an insert. It takes
// ExpireIn.
func (c *Coll[T]) Upsert(ctx context.Context, doc T, opts ...Option) (Result, error) {
	res, err := c.UpsertMany(ctx, []T{doc}, opts...)
	if err != nil {
		return Result{}, err
	}

	return res[0], nil
}

// UpsertMany is Upsert for many documents in one transaction, all or nothing
// (POST /{coll}/_bulk with "upsert": true).
func (c *Coll[T]) UpsertMany(ctx context.Context, docs []T, opts ...Option) ([]Result, error) {
	return c.bulk(ctx, docs, true, opts)
}

// Get returns the document with this _id, or ErrNotFound.
func (c *Coll[T]) Get(ctx context.Context, id string) (T, error) {
	var raw json.RawMessage

	if err := c.call(ctx, http.MethodGet, docPath("_doc", id), nil, &raw); err != nil {
		var zero T

		return zero, err
	}

	return c.decode(raw)
}

// Ops are the write operators of an Update: $set, $setOnInsert, $unset, $inc, $push,
// $pull, $min and $max, each with a map of fields (dot paths allowed).
//
//	ndb.Ops{"$inc": {"qty": -1}, "$set": {"status": "done"}}
type Ops map[string]map[string]any

// UpdateResult is the answer of Update. Doc is the new document, with its
// version in the field tagged `version`. It is nil when nothing matched and
// for an Update with Many.
type UpdateResult[T any] struct {
	Matched  int
	Modified int
	Doc      *T
}

// Update applies ops to the document with this _id, in one atomic step
// (POST /{coll}/_update/{id}). Nothing matched is not an error: Matched is
// 0. Guard adds conditions on the same document, such as Guard(Eq("_v", 7)):
// Matched is 0 when a guard fails. Upsert creates the document with this
// _id. ExpireIn sets its expiry.
func (c *Coll[T]) Update(ctx context.Context, id string, ops Ops, opts ...Option) (UpdateResult[T], error) {
	o := newOptions(opts)

	return c.runUpdate(ctx, docPath("_update", id)+lockQuery(o.lock), Filter{doc: o.guard}, ops, o, false)
}

// UpdateBy is the atomic update by query (POST /{coll}/_update_by_query):
// the daemon matches the query and writes the operators in one step against
// the newest state, so concurrent updates never interleave.
//
// Without Many it updates the first match. Many updates every match and
// needs the id or an indexed field in the query (ErrUnindexedMany). Upsert
// creates the document from the equality fields of the query when nothing
// matches. A condition in the query, such as Gte("qty", 1), makes a
// conditional write.
func (c *Coll[T]) UpdateBy(ctx context.Context, query Filter, ops Ops, opts ...Option) (UpdateResult[T], error) {
	o := newOptions(opts)

	return c.runUpdate(ctx, "_update_by_query", query, ops, o, o.many)
}

func (c *Coll[T]) runUpdate(ctx context.Context, path string, query Filter, ops Ops, o options, many bool) (UpdateResult[T], error) {
	for k := range ops {
		if !strings.HasPrefix(k, "$") {
			return UpdateResult[T]{}, fmt.Errorf("ndb: update operator %q must start with $ (for example \"$set\")", k)
		}
	}

	body, err := json.Marshal(updateBody{Query: query.doc, Update: ops, Many: many, Upsert: o.upsert, ExpireIn: o.expireIn.seconds()})
	if err != nil {
		return UpdateResult[T]{}, err
	}

	var out struct {
		Matched  int             `json:"matched"`
		Modified int             `json:"modified"`
		Doc      json.RawMessage `json:"doc"`
	}

	if err := c.call(ctx, http.MethodPost, path, body, &out); err != nil {
		return UpdateResult[T]{}, err
	}

	res := UpdateResult[T]{Matched: out.Matched, Modified: out.Modified}

	if len(out.Doc) == 0 {
		return res, nil
	}

	doc, err := c.decode(out.Doc)
	if err != nil {
		return UpdateResult[T]{}, err
	}

	res.Doc = &doc

	return res, nil
}

// Delete removes the document with this _id and returns how many it
// removed (0 or 1).
func (c *Coll[T]) Delete(ctx context.Context, id string) (int, error) {
	var out struct {
		Deleted int `json:"deleted"`
	}

	return out.Deleted, c.call(ctx, http.MethodDelete, docPath("_doc", id), nil, &out)
}

// DeleteBy removes the first document that the query matches, or every
// match with Many (same rule as UpdateBy), and returns how many
// (POST /{coll}/_delete_by_query).
func (c *Coll[T]) DeleteBy(ctx context.Context, query Filter, opts ...Option) (int, error) {
	o := newOptions(opts)

	body, err := json.Marshal(deleteBody{Query: query.doc, Many: o.many})
	if err != nil {
		return 0, err
	}

	var out struct {
		Deleted int `json:"deleted"`
	}

	return out.Deleted, c.call(ctx, http.MethodPost, "_delete_by_query", body, &out)
}

// Truncate removes every document of the collection and keeps its
// definition and indexes. The cost does not depend on the number of
// documents. Watch consumers get one OpTruncate event. A direct-mode
// collection is ErrDirectCollection.
func (c *Coll[T]) Truncate(ctx context.Context) error {
	return c.send(ctx, http.MethodPost, "_truncate?confirm="+url.QueryEscape(c.name), nil, nil)
}

// Drop removes the collection: its documents, its definition and the
// positions of its watch consumers. The next call on c declares it again.
// A collection declared in ndb.yml comes back, empty, when the daemon
// restarts.
func (c *Coll[T]) Drop(ctx context.Context) error {
	err := c.send(ctx, http.MethodDelete, "?confirm="+url.QueryEscape(c.name), nil, nil)

	c.db.forget(c.name)

	return err
}

// Rename gives the collection the name to: its documents, indexes and watch
// positions move with it. With replace, an existing to goes aside first
// (with PITR on, it stays restorable for the window); without it, an
// existing to is ErrCollectionExists. c keeps the old name: open the
// collection again under to.
func (c *Coll[T]) Rename(ctx context.Context, to string, replace bool) error {
	body, err := json.Marshal(map[string]any{"to": to, "replace": replace})
	if err != nil {
		return err
	}

	err = c.send(ctx, http.MethodPost, "_rename", body, nil)

	c.db.forget(c.name)
	c.db.forget(to)

	return err
}

// Restore makes the collection to hold this collection as it was at at,
// from the PITR window of the daemon (1 day by default). The source is not
// touched; a dropped collection can be restored while it is in the window.
// Rename to over the source to put the copy in place. Errors:
// ErrOutOfWindow, ErrPITROff, ErrCollectionExists.
func (c *Coll[T]) Restore(ctx context.Context, at time.Time, to string) error {
	body, err := json.Marshal(map[string]any{"at": at.UTC().Format(time.RFC3339Nano), "to": to})
	if err != nil {
		return err
	}

	return c.send(ctx, http.MethodPost, "_restore", body, nil)
}

// PITRInfo is the PITR window of a collection: the oldest and newest
// instant a restore reaches, and the disk the window holds.
type PITRInfo struct {
	Collection string    `json:"collection"`
	Window     string    `json:"window"`
	Oldest     time.Time `json:"oldest"`
	Newest     time.Time `json:"newest"`
	Snapshots  int       `json:"snapshots"`
	Bytes      int64     `json:"bytes"`
	Dropped    bool      `json:"dropped"`
}

// PITR is the PITR window of the collection (GET /{coll}/_pitr).
func (c *Coll[T]) PITR(ctx context.Context) (PITRInfo, error) {
	var out PITRInfo

	return out, c.send(ctx, http.MethodGet, "_pitr", nil, &out)
}

// Stats is the state of the collection (GET /{coll}/_stats): rows, indexes,
// files and bytes.
func (c *Coll[T]) Stats(ctx context.Context) (map[string]any, error) {
	var out map[string]any

	return out, c.call(ctx, http.MethodGet, "_stats", nil, &out)
}

// Compact seals the memtable and merges every segment now
// (POST /{coll}/_compact). It answers the segments and bytes before and
// after.
func (c *Coll[T]) Compact(ctx context.Context) (map[string]any, error) {
	var out map[string]any

	return out, c.call(ctx, http.MethodPost, "_compact", nil, &out)
}

// EnsureIndex adds a secondary index on field and builds it in the
// background (PUT /{coll}/_indexes/{field}). It returns the indexed fields.
func (c *Coll[T]) EnsureIndex(ctx context.Context, field string) ([]string, error) {
	return c.index(ctx, http.MethodPut, field)
}

// DropIndex removes the index on field. It returns the indexed fields.
func (c *Coll[T]) DropIndex(ctx context.Context, field string) ([]string, error) {
	return c.index(ctx, http.MethodDelete, field)
}

func (c *Coll[T]) index(ctx context.Context, method, field string) ([]string, error) {
	var out struct {
		Indexes []string `json:"indexes"`
	}

	return out.Indexes, c.call(ctx, method, docPath("_indexes", field), nil, &out)
}

// call declares the collection on first use, then sends one request (see
// send).
func (c *Coll[T]) call(ctx context.Context, method, path string, body []byte, out any) error {
	if err := c.ready(ctx); err != nil {
		return err
	}

	return c.send(ctx, method, path, body, out)
}

// send sends one request to /{coll}/{path} and decodes the JSON answer into
// out (nil: discard). Truncate and drop use it alone: they never declare.
func (c *Coll[T]) send(ctx context.Context, method, path string, body []byte, out any) error {
	target := collPath(c.name)

	switch {
	case path == "":
	case strings.HasPrefix(path, "?"):
		target += path
	default:
		target += "/" + path
	}

	resp, err := c.db.do(ctx, method, target, body)
	if err != nil {
		return err
	}

	return decodeBody(resp, out)
}

// stream declares the collection on first use and sends one request whose
// answer the caller reads (rows of _search and _aggregate).
func (c *Coll[T]) stream(ctx context.Context, path string, body []byte) (*http.Response, error) {
	if err := c.ready(ctx); err != nil {
		return nil, err
	}

	return c.db.do(ctx, http.MethodPost, collPath(c.name)+"/"+path, body)
}

// docPath is "{action}/{id}", with the id escaped.
func docPath(action, id string) string {
	return action + "/" + url.PathEscape(id)
}

func (c *Coll[T]) decode(raw json.RawMessage) (T, error) {
	var v T

	err := c.schema.decode(raw, reflect.ValueOf(&v).Elem())

	return v, err
}

// Option changes a write: Many, Upsert and ExpireIn.
type Option func(*options)

type options struct {
	many     bool
	upsert   bool
	expireIn ttlSecs
	guard    map[string]any
	lock     string
}

// ttlSecs is a duration in seconds for the wire. Zero is omitted.
type ttlSecs float64

func (d ttlSecs) seconds() *float64 {
	if d <= 0 {
		return nil
	}

	f := float64(d)

	return &f
}

// Many makes UpdateBy or DeleteBy work on every match. The filter needs the id
// or an indexed field, or the call fails with ErrUnindexedMany.
func Many() Option {
	return func(o *options) {
		o.many = true
	}
}

// Guard adds conditions to Update by _id: the update matches only when the
// document also satisfies them.
func Guard(f Filter) Option {
	return func(o *options) {
		o.guard = f.doc
	}
}

// Upsert makes Update or UpdateBy create the document when nothing
// matches.
func Upsert() Option {
	return func(o *options) {
		o.upsert = true
	}
}

// ExpireIn makes the daemon set the expires field of the written documents
// to now + d, in the same transaction (Insert, InsertMany, Update,
// UpdateBy). It wins over a
// value of the field in the document or in the operators. The collection needs a field tagged
// `expires`, or the call fails with ErrTTLNotEnabled.
func ExpireIn(d time.Duration) Option {
	return func(o *options) {
		o.expireIn = ttlSecs(d.Seconds())
	}
}

func newOptions(opts []Option) options {
	var o options

	for _, opt := range opts {
		opt(&o)
	}

	return o
}

// ensure sends ensure_collection once per name for this DB.
func (db *DB) ensure(ctx context.Context, name string, s *schema) error {
	db.mu.Lock()

	defer db.mu.Unlock()

	if db.ensured[name] {
		return nil
	}

	body, err := json.Marshal(ensureBody{Time: s.time, ExpiresField: s.expires, Indexes: s.indexes, Unique: s.unique})
	if err != nil {
		return err
	}

	if err := db.putCollection(ctx, name, body); err != nil {
		return err
	}

	db.ensured[name] = true

	return nil
}

// forget makes the next call on name declare it again (after a drop).
func (db *DB) forget(name string) {
	db.mu.Lock()
	defer db.mu.Unlock()

	delete(db.ensured, name)
}

// putCollection sends ensure_collection with a catalog JSON body.
func (db *DB) putCollection(ctx context.Context, name string, catalog []byte) error {
	resp, err := db.do(ctx, http.MethodPut, collPath(name), catalog)
	if err != nil {
		return err
	}

	discard(resp)

	return nil
}

type ensureBody struct {
	Time         string   `json:"time,omitempty"`
	ExpiresField string   `json:"expires_field,omitempty"`
	Indexes      []string `json:"indexes,omitempty"`
	Unique       []string `json:"unique,omitempty"`
}

type bulkBody struct {
	Docs     []json.RawMessage `json:"docs"`
	ExpireIn *float64          `json:"expire_in,omitempty"`
	Upsert   bool              `json:"upsert,omitempty"`
}

type updateBody struct {
	Query    map[string]any `json:"query,omitempty"`
	Update   Ops            `json:"update"`
	Many     bool           `json:"many,omitempty"`
	Upsert   bool           `json:"upsert,omitempty"`
	ExpireIn *float64       `json:"expire_in,omitempty"`
}

type deleteBody struct {
	Query map[string]any `json:"query"`
	Many  bool           `json:"many,omitempty"`
}
