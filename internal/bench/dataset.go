package bench

import (
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

	Calls int    `json:"calls"`
	Day   string `json:"day"`
}

// openDataset reads the dataset of dir. A dataset of other calls is an
// error: its numbers would not compare.
func openDataset(dir string, calls int) *dataset {
	ds := &dataset{dir: dir, Calls: calls, Day: yesterday().Format(time.DateOnly)}

	if dir == "" {
		return ds
	}

	b, err := os.ReadFile(filepath.Join(dir, datasetFile))
	if errors.Is(err, os.ErrNotExist) {
		return ds
	}

	check(err)
	check(json.Unmarshal(b, ds))

	if ds.Calls != calls {
		check(fmt.Errorf("the dataset in %s has %d calls: run with -calls %d, or another -data", dir, ds.Calls, ds.Calls))
	}

	ds.found = true

	return ds
}

func (ds *dataset) day() time.Time {
	t, err := time.Parse(time.DateOnly, ds.Day)
	check(err)

	return t
}

// load loads the calls, or tells that a dataset has them. A kept dataset
// gets a retention of ten years, so the partition is not dropped.
func (ds *dataset) load(d *daemon) []string {
	if ds.found {
		return []string{fmt.Sprintf("| load | dataset %s: %d calls (%d docs) of %s, not loaded again |", ds.dir, ds.Calls, ds.Calls*7, ds.Day)}
	}

	if ds.dir == "" {
		return load(d, "7d")
	}

	lines := load(d, "3650d")

	b, err := json.Marshal(ds)
	check(err)
	check(os.WriteFile(filepath.Join(ds.dir, datasetFile), b, 0o644))

	return lines
}
