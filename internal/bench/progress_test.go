package bench

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// Each step logs a start and an end line; the ETA adds the steps left and
// takes the real time of a step done as its estimate.
func TestProgressLogsStepsWithTheETA(t *testing.T) {
	var out bytes.Buffer

	p := newProgress(&out, []planned{{"one", time.Minute}, {"two", 2 * time.Minute}})

	p.step("one", nil)
	p.end("found it")

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")

	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got:\n%s", out.String())
	}

	if !strings.Contains(lines[0], "▶ one · ETA 3m0s") {
		t.Fatalf("start line: %s", lines[0])
	}

	if !strings.Contains(lines[1], "✓ one in 0s: found it · ETA 2m0s") {
		t.Fatalf("end line: %s", lines[1])
	}

	p.step("two", nil)
	p.end("")

	if !strings.HasSuffix(strings.TrimSpace(out.String()), "· ETA 0s") {
		t.Fatalf("the last step leaves no ETA:\n%s", out.String())
	}
}

func TestProgressStepEndsTheOneBefore(t *testing.T) {
	var out bytes.Buffer

	p := newProgress(&out, []planned{{"a", time.Second}, {"b", time.Second}})

	p.step("a", nil)
	p.step("b", nil)

	if !strings.Contains(out.String(), "✓ a in") {
		t.Fatalf("a was not ended:\n%s", out.String())
	}
}
