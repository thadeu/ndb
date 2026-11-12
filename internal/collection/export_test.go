package collection

import (
	"context"
	"encoding/json"
	bolt "go.etcd.io/bbolt"
	"os"
	"path/filepath"
	"strings"
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

// UpdateByID is the old update by id, on top of the update by filter: the
// change is $set and $unset on the id, and a non-zero ifVersion becomes a
// "_v" condition. A document that does not match is not_found.
func (c *Collection) UpdateByID(ctx context.Context, id string, ch Change, ifVersion uint64) (uint64, error) {
	body := map[string]any{idField: id}

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

// DeleteByID deletes the document with the id. Nothing deleted is not_found.
func (c *Collection) DeleteByID(ctx context.Context, id string) error {
	raw, _ := json.Marshal(map[string]any{idField: id})

	n, err := c.Delete(ctx, raw, false)
	if err != nil {
		return err
	}

	if n == 0 {
		return errf("not_found", "%s", id)
	}

	return nil
}

// MakeLegacy turns the data under root into the format of a version before
// M16: no key files, no live bits, checkpoint format 0.
func MakeLegacy(root string) error {
	entries, err := os.ReadDir(filepath.Join(root, segmentDir))
	if err != nil {
		return err
	}

	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".parquet") {
			if err := os.Remove(filepath.Join(root, segmentDir, e.Name())); err != nil {
				return err
			}
		}
	}

	db, err := bolt.Open(filepath.Join(root, metaFile), 0o644, nil)
	if err != nil {
		return err
	}

	defer db.Close()

	cp, err := readCheckpoint(db)
	if err != nil {
		return err
	}

	cp.Format = 0

	return db.Update(func(tx *bolt.Tx) error {
		if err := tx.DeleteBucket(bucketLiveBits); err != nil {
			return err
		}

		return putCheckpoint(tx, cp)
	})
}

// Scheduled tells whether the seal and the compaction jobs of c have a
// deadline (or run now).
func Scheduled(c *Collection) (seal, compact bool) {
	sched.mu.Lock()
	defer sched.mu.Unlock()

	queued := func(j *job) bool { return j.idx >= 0 || j.busy }

	return queued(c.seal.sealJob), queued(c.seal.compactJob)
}

// SetResealGate makes the reseal job call g before each page. Set it before
// SetEncrypted.
func SetResealGate(c *Collection, g func()) {
	c.resealGate = g
}

// ResealFile is the file that keeps the fields of a reseal job.
const ResealFile = resealFile
