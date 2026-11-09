package collection_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/collection"
)

// Close during a compaction stops it: a shutdown does not wait for the
// merge of big segments.
func TestCloseStopsACompaction(t *testing.T) {
	c := openSealed(t, t.TempDir(), collection.Config{CompactMin: 2})

	for s := range 8 {
		for b := s * 25_000; b < (s+1)*25_000; b += 1000 {
			bulk(t, c, rangeDocs(b, b+1000, func(i int) string {
				return fmt.Sprintf(`{"_id":"d%07d","status":"s%d","n":%d,"pad":"%064d"}`, i, i%7, i, i)
			}))
		}

		mustSeal(t, c)
	}

	started := time.Now()
	done := make(chan time.Duration, 1)

	go func() {
		_ = c.Compact()
		done <- time.Since(started)
	}()

	time.Sleep(150 * time.Millisecond)

	closing := time.Now()

	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	took := time.Since(closing)
	full := <-done

	t.Logf("Close took %s; Compact returned after %s", took, full)

	if took > 2*time.Second {
		t.Fatalf("Close waited %s for the compaction", took)
	}
}
