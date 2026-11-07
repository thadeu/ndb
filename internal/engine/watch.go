package engine

import (
	"bytes"
	"fmt"

	"github.com/thadeu/ndb/internal/feed"
	"github.com/thadeu/ndb/internal/query"
)

// source is the change feed of the collection, in either mode.
func (c *Collection) source() feed.Source {
	if c.eng != nil {
		return c.eng.Feed()
	}

	return c.mem.Feed()
}

// Watch opens the change stream of the collection for a consumer, after the
// position stored by its last ack. A consumer with no stored position starts
// at the beginning, or with from "now" at the current end; that end is stored
// at once, so a reconnect before the first ack does not skip changes.
func (db *DB) Watch(name, consumer, from string) (feed.Stream, error) {
	return db.WatchWhere(name, consumer, from, nil)
}

// WatchWhere is Watch with an optional where filter (the where of find, a JSON
// object; nil or empty means no filter). Only the events whose document
// matches are delivered, see filterStream for delete events. The stored
// position still moves over the events that are left out.
func (db *DB) WatchWhere(name, consumer, from string, where []byte) (feed.Stream, error) {
	if from != "" && from != "beginning" && from != "now" {
		return nil, fmt.Errorf("watch: from must be beginning or now, not %q", from)
	}

	col, err := db.Get(name)
	if err != nil {
		return nil, err
	}

	if err := col.enter(); err != nil {
		return nil, err
	}

	defer col.leave()

	var filter *query.Query

	if len(bytes.TrimSpace(where)) > 0 {
		filter, err = db.watchFilter(name, where)
		if err != nil {
			return nil, err
		}
	}

	src := col.source()
	start := ""

	err = db.cursors.Update(name, consumer, func(old string, ok bool) (string, error) {
		start = old

		if !ok && from == "now" {
			start = src.Now()
		}

		return start, nil
	})
	if err != nil {
		return nil, err
	}

	stream, err := src.Open(start)
	if err != nil || filter == nil {
		return stream, err
	}

	ack := func(pos string) error { return db.Ack(name, consumer, pos) }

	return newFilterStream(stream, filter, ack), nil
}

// watchFilter compiles where with the time fields of the collection.
func (db *DB) watchFilter(name string, where []byte) (*query.Query, error) {
	def, _ := db.cat.Get(name)

	body := append(append([]byte(`{"where":`), where...), '}')

	return query.Parse(body, query.Options{TimeField: def.Time, ExpiresField: def.ExpiresField})
}

// Ack stores the position of the consumer: the position of an event it has
// processed. An ack older than the stored position changes nothing.
func (db *DB) Ack(name, consumer, pos string) error {
	col, err := db.Get(name)
	if err != nil {
		return err
	}

	if err := col.enter(); err != nil {
		return err
	}

	defer col.leave()

	src := col.source()

	return db.cursors.Update(name, consumer, func(old string, _ bool) (string, error) {
		return src.Merge(old, pos)
	})
}
