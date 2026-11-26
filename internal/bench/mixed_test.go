package bench

import (
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMixedBulkWritesBatchDocs(t *testing.T) {
	body := mixedBulk("h", 2000)

	if n := strings.Count(body, `"_id"`); n != mixedBatch {
		t.Fatalf("%d docs, want %d", n, mixedBatch)
	}

	if !strings.HasPrefix(body, `{"docs":[{"_id":"h002000","group":"h0000"`) || !strings.HasSuffix(body, "}]}") {
		t.Fatalf("body = %.80s…", body)
	}
}

func TestUsIsTheQuantileInMicroseconds(t *testing.T) {
	lats := []time.Duration{10 * time.Microsecond, 20 * time.Microsecond, 30 * time.Microsecond}

	if got := us(lats, .5); got != 20 {
		t.Fatalf("p50 = %v, want 20", got)
	}

	if got := us(nil, .5); got != 0 {
		t.Fatalf("empty = %v, want 0", got)
	}
}

// The mixed part on a real daemon, for a short time.
func TestMixedPartRecordsTheReadLines(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and starts a daemon")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "ndb")

	if out, err := exec.Command("go", "build", "-o", path, "../../cmd/ndb").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}

	old := mixedFor
	mixedFor = 200 * time.Millisecond
	bin, prog, measured = &path, newProgress(io.Discard, mixedPlan()), map[string]float64{}

	t.Cleanup(func() { mixedFor = old })

	lines := mixedPart(dir)

	if len(lines) != 3 || !strings.HasPrefix(lines[0], "| GET by _id |") {
		t.Fatalf("lines = %q", lines)
	}

	for _, m := range []string{"get.id_us", "search.small_us", "mixed.get_p99_us", "mixed.search_p99_us"} {
		if measured[m] <= 0 {
			t.Errorf("%s = %v, want > 0", m, measured[m])
		}
	}
}
