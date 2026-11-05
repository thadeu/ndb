package engine

import (
	"context"
	"encoding/json"
	"iter"
	"time"

	"github.com/thadeu/ndb/internal/blob"
)

// scanPage is how many documents one page of the live-document scan reads.
const scanPage = 1000

// Blobs is the blob store of the data dir.
func (db *DB) Blobs() *blob.Store {
	return db.blobs
}

// GCBlobs deletes the blobs that no live document refers to and that are
// older than grace (blob.DefaultGrace is 24 h). It scans the live documents
// of every collection. The grace period covers a blob whose document is not
// written yet, and a document that changed after its collection was scanned.
func (db *DB) GCBlobs(ctx context.Context, grace time.Duration) (blob.GCResult, error) {
	live, err := db.liveBlobs(ctx)
	if err != nil {
		return blob.GCResult{}, err
	}

	return db.blobs.GC(live, grace)
}

// compacted is the OnCompact hook of every engine collection. A compaction
// is when old document versions stop existing, so it starts one blob GC in
// the background. The GC scans all collections, so the documents the hook
// gives are not used, and a GC that is running already is not doubled.
func (db *DB) compacted(iter.Seq[[]byte]) {
	if !db.gcRunning.CompareAndSwap(false, true) {
		return
	}

	db.gcWait.Add(1)

	go func() {
		defer db.gcWait.Done()
		defer db.gcRunning.Store(false)

		res, err := db.GCBlobs(db.ctx, blob.DefaultGrace)
		if err != nil {
			if db.ctx.Err() == nil {
				db.log.Error("blob gc failed", "err", err)
			}

			return
		}

		db.log.Debug("blob gc", "result", res)
	}()
}

// liveBlobs reads every live document and collects its blob hashes.
func (db *DB) liveBlobs(ctx context.Context) (iter.Seq[string], error) {
	db.mu.RLock()

	cols := make([]*Collection, 0, len(db.cols))

	for _, col := range db.cols {
		cols = append(cols, col)
	}

	db.mu.RUnlock()

	var hashes []string

	for _, col := range cols {
		err := col.eachDoc(ctx, func(doc []byte) {
			hashes = append(hashes, blob.Refs(doc)...)
		})
		if err != nil {
			return nil, err
		}
	}

	return func(yield func(string) bool) {
		for _, h := range hashes {
			if !yield(h) {
				return
			}
		}
	}, nil
}

// eachDoc calls fn for every live document, page by page.
func (c *Collection) eachDoc(ctx context.Context, fn func(doc []byte)) error {
	after := ""

	for {
		body, err := json.Marshal(map[string]any{"limit": scanPage, "after": after})
		if err != nil {
			return err
		}

		page, err := c.Find(ctx, body)
		if err != nil {
			return err
		}

		for _, row := range page.Rows {
			fn(row)
		}

		if page.Next == "" {
			return nil
		}

		after = page.Next
	}
}
