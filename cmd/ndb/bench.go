package main

import (
	"flag"
	"os"

	"github.com/thadeu/ndb/internal/bench"
)

// runBench is ndb bench: the ESL outbox scenario on a daemon of its own (this
// binary, in a temp dir). -stress-load measures the writes, -query the
// queries, -aggs the aggregations, -idle the cost of idle collections; with
// none of them it runs all, in about two and a half minutes. Each
// measurement is checked against its budget: a FAIL makes it exit 1. Copy the binary to a machine and run it there to compare
// machines or versions.
func runBench(args []string) error {
	o := bench.Defaults()

	fl := flag.NewFlagSet("ndb bench", flag.ContinueOnError)
	fl.DurationVar(&o.Live, "live", o.Live, "how long the live phase writes")
	fl.IntVar(&o.Rate, "rate", o.Rate, "docs per second of the live phase")
	fl.IntVar(&o.Calls, "calls", o.Calls, "calls of the load phase (7 docs each)")
	fl.StringVar(&o.Dir, "dir", "", "where the temp data dir goes (default: the system temp dir)")
	fl.BoolVar(&o.Keep, "keep", false, "keep the data dir")
	fl.StringVar(&o.Bin, "ndb", "", "the ndb binary of the daemon (default: this one)")
	fl.BoolVar(&o.Stress, "stress-load", false, "measure the writes: live writes with a consumer, kill -9, bulk load")
	fl.BoolVar(&o.Query, "query", false, "measure the queries (loads the calls first)")
	fl.BoolVar(&o.Aggs, "aggs", false, "measure the aggregations (loads the calls first)")
	fl.BoolVar(&o.Lookup, "lookup", false, "measure the lookups: belongs_to, has_many, has_one, 2 levels, $lookup (loads the calls first)")
	fl.BoolVar(&o.Idle, "idle", false, "measure idle collections: disk, CPU, RSS and files per collection at rest")
	fl.IntVar(&o.Collections, "collections", o.Collections, "collections of the idle part")
	fl.StringVar(&o.PITR, "pitr", "", "pitr of the daemon: off, 1d (default: the daemon default, 1d)")

	if err := fl.Parse(args); err != nil {
		return err
	}

	if o.Bin == "" {
		self, err := os.Executable()
		if err != nil {
			return err
		}

		o.Bin = self
	}

	o.Log = os.Stderr

	return bench.Run(o, os.Stdout)
}
