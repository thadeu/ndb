package engine

import (
	"fmt"

	"github.com/thadeu/ndb/internal/feed"
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
	if from != "" && from != "beginning" && from != "now" {
		return nil, fmt.Errorf("watch: from must be beginning or now, not %q", from)
	}

	col, err := db.Get(name)
	if err != nil {
		return nil, err
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

	return src.Open(start)
}

// Ack stores the position of the consumer: the position of an event it has
// processed. An ack older than the stored position changes nothing.
func (db *DB) Ack(name, consumer, pos string) error {
	col, err := db.Get(name)
	if err != nil {
		return err
	}

	src := col.source()

	return db.cursors.Update(name, consumer, func(old string, _ bool) (string, error) {
		return src.Merge(old, pos)
	})
}
