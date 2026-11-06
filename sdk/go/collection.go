package ndb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// Coll is one collection of documents of type T.
type Coll[T any] struct {
	db     *DB
	name   string
	schema *schema
}

// Collection declares the collection name for T and returns it. The first
// call for a name sends ensure_collection, with the id, time and index tags
// of T. Later calls for the same name on the same DB send nothing.
func Collection[T any](ctx context.Context, db *DB, name string) (*Coll[T], error) {
	s, err := schemaOf(reflect.TypeFor[T]())
	if err != nil {
		return nil, err
	}

	if err := db.ensure(ctx, name, s); err != nil {
		return nil, err
	}

	return &Coll[T]{db: db, name: name, schema: s}, nil
}

// Insert adds documents. It fails with ErrDuplicateID when an id exists.
// Result is the id and version of an inserted document. An empty id field
// is not sent, so the daemon generates the id and Result carries it.
type Result struct {
	ID string `json:"id"`
	V  uint64 `json:"v"`
}

// Insert writes docs in one transaction (all or nothing).
func (c *Coll[T]) Insert(ctx context.Context, docs ...T) ([]Result, error) {
	return c.InsertWith(ctx, docs)
}

// InsertWith is Insert with options. ExpireIn applies to every document.
func (c *Coll[T]) InsertWith(ctx context.Context, docs []T, opts ...Option) ([]Result, error) {
	o := newOptions(opts)
	rows := make([]json.RawMessage, len(docs))

	for i, d := range docs {
		raw, err := c.schema.encode(d)
		if err != nil {
			return nil, err
		}

		rows[i] = raw
	}

	resp, err := c.post(ctx, "insert", insertBody{Insert: rows, ExpireIn: o.expireIn.seconds()})
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	var out struct {
		Results []Result `json:"results"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("ndb: insert response: %w", err)
	}

	return out.Results, nil
}

// Get returns the document with the id, or ErrNotFound.
func (c *Coll[T]) Get(ctx context.Context, id string) (T, error) {
	return c.Where(Eq(c.schema.idField(), id)).First(ctx)
}

// Ops are the write operators of an Update: $set, $unset, $inc, $push,
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

// Update is the atomic update by filter: the daemon matches the filter and
// writes the operators in one step, against the newest state, so concurrent
// updates never interleave. Nothing matched is not an error: Matched is 0.
//
// A filter with the id field finds one document by id; without it the
// update takes the first match. Many updates every match, and needs the id
// or an indexed field in the filter (ErrUnindexedMany). Upsert creates the
// document from the equality fields of the filter when nothing matches.
// A guard in the filter, such as Eq("_v", 7) or Gte("qty", 1), makes a
// conditional write: Matched is 0 when the guard fails.
func (c *Coll[T]) Update(ctx context.Context, filter Filter, ops Ops, opts ...Option) (UpdateResult[T], error) {
	o := newOptions(opts)

	body, err := updateBody(filter, ops)
	if err != nil {
		return UpdateResult[T]{}, err
	}

	q := url.Values{}

	o.addTo(q, "many", o.many)
	o.addTo(q, "upsert", o.upsert)

	if secs := o.expireIn.seconds(); secs != nil {
		q.Set("expire_in", strconv.FormatFloat(*secs, 'f', -1, 64))
	}

	resp, err := c.postQuery(ctx, "update", q, body)
	if err != nil {
		return UpdateResult[T]{}, err
	}

	defer resp.Body.Close()

	var out struct {
		Matched  int             `json:"matched"`
		Modified int             `json:"modified"`
		Doc      json.RawMessage `json:"doc"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return UpdateResult[T]{}, fmt.Errorf("ndb: update response: %w", err)
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

// Delete removes the document that the filter matches, or every match with
// Many (same rule as Update), and returns how many it removed.
func (c *Coll[T]) Delete(ctx context.Context, filter Filter, opts ...Option) (int, error) {
	o := newOptions(opts)

	body, err := json.Marshal(filter.doc)
	if err != nil {
		return 0, err
	}

	q := url.Values{}

	o.addTo(q, "many", o.many)

	resp, err := c.postQuery(ctx, "delete", q, body)
	if err != nil {
		return 0, err
	}

	defer resp.Body.Close()

	var out struct {
		Deleted int `json:"deleted"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, fmt.Errorf("ndb: delete response: %w", err)
	}

	return out.Deleted, nil
}

// updateBody merges the filter and the operators into the one body of the
// update call. An operator key without "$" is a mistake that the daemon would
// read as a filter key, so it is refused before anything is sent.
func updateBody(filter Filter, ops Ops) ([]byte, error) {
	body := make(map[string]any, len(filter.doc)+len(ops))

	for k, v := range filter.doc {
		body[k] = v
	}

	for k, v := range ops {
		if !strings.HasPrefix(k, "$") {
			return nil, fmt.Errorf("ndb: update operator %q must start with $ (for example \"$set\")", k)
		}

		body[k] = v
	}

	return json.Marshal(body)
}

// postQuery is post with a query string (the options of an update).
func (c *Coll[T]) postQuery(ctx context.Context, op string, q url.Values, body []byte) (*http.Response, error) {
	path := collPath(c.name) + "/" + op

	if len(q) > 0 {
		path += "?" + q.Encode()
	}

	return c.db.do(ctx, http.MethodPost, path, body)
}

// post sends one write op of the collection, with v as the JSON body.
func (c *Coll[T]) post(ctx context.Context, op string, v any) (*http.Response, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}

	return c.db.do(ctx, http.MethodPost, collPath(c.name)+"/"+op, body)
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
}

// addTo sets name=true in q when on.
func (o options) addTo(q url.Values, name string, on bool) {
	if on {
		q.Set(name, "true")
	}
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

// Many makes Update or Delete work on every match. The filter needs the id
// or an indexed field, or the call fails with ErrUnindexedMany.
func Many() Option {
	return func(o *options) {
		o.many = true
	}
}

// Upsert makes Update create the document when nothing matches.
func Upsert() Option {
	return func(o *options) {
		o.upsert = true
	}
}

// ExpireIn makes the daemon set the expires field of the written documents
// to now + d, in the same transaction (Insert and Update). It wins over a
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

	body, err := json.Marshal(ensureBody{ID: s.id, Time: s.time, ExpiresField: s.expires, Indexes: s.indexes, Unique: s.unique})
	if err != nil {
		return err
	}

	if err := db.putCollection(ctx, name, body); err != nil {
		return err
	}

	db.ensured[name] = true

	return nil
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
	ID           string   `json:"id,omitempty"`
	Time         string   `json:"time,omitempty"`
	ExpiresField string   `json:"expires_field,omitempty"`
	Indexes      []string `json:"indexes,omitempty"`
	Unique       []string `json:"unique,omitempty"`
}

type insertBody struct {
	Insert   []json.RawMessage `json:"insert"`
	ExpireIn *float64          `json:"expire_in,omitempty"`
}

type writeResult struct {
	ID string `json:"id"`
	V  uint64 `json:"v"`
}
