package ndb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
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
	rows := make([]json.RawMessage, len(docs))

	for i, d := range docs {
		raw, err := c.schema.encode(d)
		if err != nil {
			return nil, err
		}

		rows[i] = raw
	}

	resp, err := c.post(ctx, "insert", insertBody{Insert: rows})
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

// Update sets fields of the document with the id and returns its new
// version. Use IfVersion to fail with ErrVersionConflict on a stale write.
func (c *Coll[T]) Update(ctx context.Context, id string, set map[string]any, opts ...Option) (uint64, error) {
	o := newOptions(opts)
	args := updateArgs{ID: id, Set: set, Unset: o.unset, IfVersion: o.ifVersion}

	resp, err := c.post(ctx, "update", updateBody{Update: args})
	if err != nil {
		return 0, err
	}

	defer resp.Body.Close()

	var res writeResult

	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return 0, err
	}

	return res.V, nil
}

// Delete removes the document with the id. Only IfVersion applies.
func (c *Coll[T]) Delete(ctx context.Context, id string, opts ...Option) error {
	o := newOptions(opts)

	args := deleteArgs{ID: id, IfVersion: o.ifVersion}

	resp, err := c.post(ctx, "delete", deleteBody{Delete: args})
	if err != nil {
		return err
	}

	discard(resp)

	return nil
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

// Option changes an Update or a Delete.
type Option func(*options)

type options struct {
	ifVersion uint64
	unset     []string
}

// IfVersion makes the write fail with ErrVersionConflict unless the document
// is at version v. Zero means no check, the same as the daemon.
func IfVersion(v uint64) Option {
	return func(o *options) {
		o.ifVersion = v
	}
}

// Unset removes fields in an Update.
func Unset(fields ...string) Option {
	return func(o *options) {
		o.unset = append(o.unset, fields...)
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
	Insert []json.RawMessage `json:"insert"`
}

type updateBody struct {
	Update updateArgs `json:"update"`
}

type updateArgs struct {
	ID        string         `json:"id"`
	Set       map[string]any `json:"set,omitempty"`
	Unset     []string       `json:"unset,omitempty"`
	IfVersion uint64         `json:"if_version,omitempty"`
}

type deleteBody struct {
	Delete deleteArgs `json:"delete"`
}

type deleteArgs struct {
	ID        string `json:"id"`
	IfVersion uint64 `json:"if_version,omitempty"`
}

type writeResult struct {
	ID string `json:"id"`
	V  uint64 `json:"v"`
}
