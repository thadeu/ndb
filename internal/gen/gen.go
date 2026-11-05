// Package gen writes deterministic NDJSON test data.
package gen

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"strings"
	"time"
)

// Options control the shape of the data. Ratios are in [0, 1].
type Options struct {
	Seed  uint64
	Bytes int64

	TypeConflicts float64
	Updates       float64
	Deletes       float64
	LargeValues   float64
	LargeSize     int
}

var (
	statuses = []string{"answered", "failed", "busy", "no_answer", "abandoned"}
	cities   = []string{"SP", "RJ", "BH", "POA", "REC", "CWB"}
	tags     = []string{"vip", "new", "retry", "outbound", "inbound"}
	epoch    = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
)

// Write emits lines until at least opts.Bytes bytes are written.
func Write(w io.Writer, opts Options) error {
	rng := rand.New(rand.NewPCG(opts.Seed, opts.Seed^0x9e3779b97f4a7c15))
	bw := bufio.NewWriter(w)

	var written int64

	next := 0

	for written < opts.Bytes {
		line, err := json.Marshal(doc(rng, opts, &next))
		if err != nil {
			return err
		}

		line = append(line, '\n')

		if _, err := bw.Write(line); err != nil {
			return err
		}

		written += int64(len(line))
	}

	return bw.Flush()
}

func doc(rng *rand.Rand, opts Options, next *int) map[string]any {
	id := pickID(rng, opts, next)

	if *next > 1 && rng.Float64() < opts.Deletes {
		return map[string]any{"id": id, "_deleted": true}
	}

	ts := epoch.Add(time.Duration(*next) * time.Second)

	d := map[string]any{
		"id":       id,
		"ts":       ts.Format(time.RFC3339),
		"status":   statuses[rng.IntN(len(statuses))],
		"duration": rng.IntN(600),
		"score":    rng.Float64() * 10,
		"address":  map[string]any{"city": cities[rng.IntN(len(cities))], "zip": fmt.Sprintf("%05d", rng.IntN(99999))},
		"tags":     []string{tags[rng.IntN(len(tags))], tags[rng.IntN(len(tags))]},
	}

	age := 18 + rng.IntN(60)

	if rng.Float64() < opts.TypeConflicts {
		d["age"] = fmt.Sprint(age)
	} else {
		d["age"] = age
	}

	if rng.IntN(10) == 0 {
		delete(d, "score")
	}

	if rng.Float64() < opts.LargeValues {
		d["payload"] = strings.Repeat("x", opts.LargeSize)
	}

	return d
}

// pickID returns a new id, or an old one when the line is an update.
func pickID(rng *rand.Rand, opts Options, next *int) string {
	if *next > 1 && rng.Float64() < opts.Updates {
		return fmt.Sprintf("c%d", rng.IntN(*next))
	}

	id := fmt.Sprintf("c%d", *next)
	*next++

	return id
}
