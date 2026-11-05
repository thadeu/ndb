// ndbgen writes deterministic NDJSON test data.
//
//	ndbgen -size 1GB -seed 42 -out data.ndjson
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/thadeu/ndb/internal/gen"
)

func main() {
	size := flag.String("size", "100MB", "output size (KB, MB, GB)")
	out := flag.String("out", "-", "output file, - for stdout")

	var opts gen.Options

	flag.Uint64Var(&opts.Seed, "seed", 1, "random seed")
	flag.Float64Var(&opts.TypeConflicts, "conflicts", 0.01, "ratio of lines with age as a string")
	flag.Float64Var(&opts.Updates, "updates", 0.05, "ratio of lines that repeat an id")
	flag.Float64Var(&opts.Deletes, "deletes", 0.01, "ratio of _deleted lines")
	flag.Float64Var(&opts.LargeValues, "large", 0, "ratio of lines with a large payload")
	flag.IntVar(&opts.LargeSize, "large-size", 64*1024, "payload size in bytes")
	flag.Parse()

	n, err := parseSize(*size)
	if err != nil {
		fail(err)
	}

	opts.Bytes = n

	var w io.Writer = os.Stdout

	if *out != "-" {
		f, err := os.Create(*out)
		if err != nil {
			fail(err)
		}

		defer f.Close()

		w = f
	}

	if err := gen.Write(w, opts); err != nil {
		fail(err)
	}
}

func parseSize(s string) (int64, error) {
	units := map[string]int64{"GB": 1 << 30, "MB": 1 << 20, "KB": 1 << 10}
	s = strings.ToUpper(strings.TrimSpace(s))

	for suffix, mult := range units {
		if num, ok := strings.CutSuffix(s, suffix); ok {
			n, err := strconv.ParseFloat(num, 64)

			return int64(n * float64(mult)), err
		}
	}

	return strconv.ParseInt(s, 10, 64)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "ndbgen:", err)
	os.Exit(1)
}
