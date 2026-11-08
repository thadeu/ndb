package bench

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestGatesPassAndFail(t *testing.T) {
	ceiling := gate{max: 10}
	floor := gate{min: 5, max: -1}

	if !ceiling.pass(10) || ceiling.pass(10.1) {
		t.Fatal("a max is inclusive")
	}

	if !floor.pass(5) || floor.pass(4.9) {
		t.Fatal("a min is inclusive")
	}
}

func TestVerifyPrintsOnlyWhatWasMeasured(t *testing.T) {
	measured = map[string]float64{"crash.missing": 2, "idle.fds": 2}

	var out bytes.Buffer

	if failed := verify(&out); failed != 1 {
		t.Fatalf("failed = %d, want 1", failed)
	}

	if !strings.Contains(out.String(), "| acked writes lost by a kill -9 | 2 | ≤ 0 | **FAIL** |") {
		t.Fatalf("no FAIL line:\n%s", out.String())
	}

	if strings.Contains(out.String(), "live write p99") {
		t.Fatalf("a check of a part that did not run:\n%s", out.String())
	}
}

func TestClockReadsPs(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"0:00.60":  600 * time.Millisecond,
		"00:00:03": 3 * time.Second,
		"1:02:03":  time.Hour + 2*time.Minute + 3*time.Second,
	} {
		if got := clock(in); got != want {
			t.Errorf("clock(%q) = %s, want %s", in, got, want)
		}
	}
}
