package bench

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// datasetFile marks a dir whose calls are loaded (-data).
const datasetFile = "bench.json"

// dataset is the loaded calls that a run reads: loaded now (in the temp dir,
// or in -data the first time), or loaded by an earlier run (-data).
type dataset struct {
	dir   string
	found bool

	Calls     int    `json:"calls"`
	Partition string `json:"partition,omitempty"`

	// Day is the day of a day layout; Start and End the span of a month
	// layout (End is when the calls were loaded: "now" then).
	Day   string    `json:"day,omitempty"`
	Start time.Time `json:"start,omitzero"`
	End   time.Time `json:"end,omitzero"`
}

// openDataset reads the dataset of dir. A dataset of other calls is an
// error: its numbers would not compare.
func openDataset(dir string, calls int, partition string) *dataset {
	ds := newDataset(dir, calls, partition)

	if dir == "" {
		return ds
	}

	b, err := os.ReadFile(filepath.Join(dir, datasetFile))
	if errors.Is(err, os.ErrNotExist) {
		return ds
	}

	check(err)

	ds = &dataset{dir: dir}
	check(json.Unmarshal(b, ds))

	ds.Partition = cmp.Or(ds.Partition, "day")

	if ds.Calls != calls || ds.Partition != partition {
		check(fmt.Errorf("the dataset in %s has %d calls by %s: run with -calls %d -partition %s, or another -data", dir, ds.Calls, ds.Partition, ds.Calls, ds.Partition))
	}

	ds.found = true

	return ds
}

// newDataset is the dataset a load makes now: yesterday, or the last six
// months up to now.
func newDataset(dir string, calls int, partition string) *dataset {
	ds := &dataset{dir: dir, Calls: calls, Partition: partition}

	switch partition {
	case "day":
		ds.Day = yesterday().Format(time.DateOnly)
	case "month":
		ds.End = time.Now().UTC().Truncate(time.Second)
		ds.Start = monthOf(ds.End).AddDate(0, -5, 0)
	default:
		check(fmt.Errorf("-partition is day or month, not %q", partition))
	}

	return ds
}

func (ds *dataset) layout() layout {
	if ds.Partition == "month" {
		return monthLayout(ds.Start, ds.End)
	}

	t, err := time.Parse(time.DateOnly, ds.Day)
	check(err)

	return dayLayout(t)
}

// retention keeps the loaded calls for the whole run (seven days of a day
// layout) or, in a kept dataset, for ten years.
func (ds *dataset) retention() string {
	switch {
	case ds.dir != "":
		return "3650d"
	case ds.Partition == "month":
		return "365d"
	}

	return "7d"
}

// load loads the calls, or tells that a dataset has them. A kept dataset
// gets a retention of ten years, so the partition is not dropped.
func (ds *dataset) load(d *daemon) []string {
	if ds.found {
		return []string{fmt.Sprintf("| load | dataset %s: %d calls (%d docs) by %s, not loaded again |", ds.dir, ds.Calls, ds.Calls*7, ds.Partition)}
	}

	lines := load(d, ds.retention())

	if ds.dir == "" {
		return lines
	}

	b, err := json.Marshal(ds)
	check(err)
	check(os.WriteFile(filepath.Join(ds.dir, datasetFile), b, 0o644))

	return lines
}
