package collection

import (
	"context"
	"encoding/json"
)

// SetBuildGate makes the background index build call g before each chunk.
// Set it before EnsureIndex.
func SetBuildGate(c *Collection, g func()) {
	c.rd.gate = g
}

// MemDocs counts the memtable documents that still hold their bytes.
func MemDocs(c *Collection) int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	n := 0

	for _, e := range c.durable.docs {
		if e.doc != nil {
			n++
		}
	}

	return n
}

// SegmentRows is the number of rows in the live segments.
func SegmentRows(c *Collection) int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	n := 0

	for _, r := range c.seal.readers {
		n += r.Len()
	}

	return n
}

// HoldSegments takes a reference on every live segment, as a running query
// does, and returns the release.
func HoldSegments(c *Collection) func() {
	c.mu.RLock()
	held := c.holdSegments()
	c.mu.RUnlock()

	return func() { releaseAll(held) }
}

// SetCompactHook calls fn after each step of a compaction.
func SetCompactHook(c *Collection, fn func(step string)) {
	c.seal.hook = fn
}

// CompactRange merges the segments lo..hi-1.
func (c *Collection) CompactRange(lo, hi int) error {
	return c.compactRange(lo, hi)
}

// UpdateID is the old update by id, on top of the update by filter: the
// change is $set and $unset on the id, and a non-zero ifVersion becomes a
// "_v" condition. A document that does not match is not_found.
func (c *Collection) UpdateID(ctx context.Context, id string, ch Change, ifVersion uint64) (uint64, error) {
	body := map[string]any{c.def.ID: id}

	if len(ch.Set) > 0 {
		body["$set"] = ch.Set
	}

	if len(ch.Unset) > 0 {
		unset := map[string]any{}

		for _, p := range ch.Unset {
			unset[p] = true
		}

		body["$unset"] = unset
	}

	if ifVersion != 0 {
		body["_v"] = ifVersion
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}

	res, err := c.Update(ctx, raw, UpdateOpts{ExpireIn: ch.ExpireIn})
	if err != nil {
		return 0, err
	}

	if res.Matched == 0 {
		return 0, errf("not_found", "%s", id)
	}

	var doc struct {
		V uint64 `json:"_v"`
	}

	return doc.V, json.Unmarshal(res.Doc, &doc)
}

// DeleteID deletes the document with the id. Nothing deleted is not_found.
func (c *Collection) DeleteID(ctx context.Context, id string) error {
	raw, _ := json.Marshal(map[string]any{c.def.ID: id})

	n, err := c.Delete(ctx, raw, false)
	if err != nil {
		return err
	}

	if n == 0 {
		return errf("not_found", "%s", id)
	}

	return nil
}
