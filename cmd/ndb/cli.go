package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/thadeu/ndb/internal/config"
)

// subcommands maps the first argument to its command. Anything else, flags
// included, starts the daemon, so "ndb -data-dir d" keeps working.
var subcommands = map[string]func(args []string) error{
	"backup":  runBackup,
	"restore": runRestore,
	"stats":   runStats,
	"size":    runSize,
	"version": runVersion,
	"help":    runHelp,
}

// dataDirFlags are the flags every offline command shares.
type dataDirFlags struct {
	cfgPath *string
	dataDir *string
}

func addDataDirFlags(fl *flag.FlagSet) dataDirFlags {
	return dataDirFlags{
		cfgPath: fl.String("config", "ndb.yml", "optional config file (absent = defaults)"),
		dataDir: fl.String("data-dir", "", "data dir (overrides ndb.yml)"),
	}
}

// load reads ndb.yml and applies -data-dir when it was set.
func (d dataDirFlags) load(fl *flag.FlagSet) (config.Config, error) {
	cfg, err := config.Load(*d.cfgPath)
	if err != nil {
		return cfg, err
	}

	if flagSet(fl, "data-dir") {
		cfg.DataDir = *d.dataDir
	}

	return cfg, nil
}

func flagSet(fl *flag.FlagSet, name string) bool {
	found := false

	fl.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})

	return found
}

// parseInterspersed parses flags that may come before or after positional
// arguments, and returns the positional ones.
func parseInterspersed(fl *flag.FlagSet, args []string) ([]string, error) {
	var pos []string

	for {
		if err := fl.Parse(args); err != nil {
			return nil, err
		}

		if fl.NArg() == 0 {
			return pos, nil
		}

		pos = append(pos, fl.Arg(0))
		args = fl.Args()[1:]
	}
}

// newTable is an aligned table writer; call Flush when done.
func newTable(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
}

var byteUnits = []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}

// humanBytes prints n with binary units: 0 B, 1.5 KiB, 3.2 GiB.
func humanBytes(n int64) string {
	f := float64(n)
	i := 0

	for f >= 1024 && i < len(byteUnits)-1 {
		f /= 1024
		i++
	}

	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}

	return fmt.Sprintf("%.1f %s", f, byteUnits[i])
}

func runVersion(_ []string) error {
	fmt.Println("ndb", version)

	return nil
}

func runHelp(_ []string) error {
	fmt.Fprint(os.Stdout, helpText)

	return nil
}

var helpText = strings.TrimLeft(`
ndb: NDJSON query daemon

Usage:
  ndb [daemon flags]            run the daemon (same as "ndb serve")
  ndb serve [daemon flags]      run the daemon
  ndb backup <dest>             copy the data dir (daemon stopped or running)
  ndb restore <backup-dir>      put a backup in an empty data dir, after a check
  ndb stats                     rows, size, segments, WAL files, indexes, quarantine
  ndb size                      disk usage per collection and per part
  ndb version                   print the version (also: ndb -version)
  ndb help                      print this text

Common flags:
  -config FILE      ndb.yml (default ndb.yml; absent = defaults)
  -data-dir DIR     data dir (overrides ndb.yml)

Per command:
  restore  -force                   replace a data dir that is not empty
  stats    -url unix:///sock|tcp://host:port   ask a running daemon (NDB_URL, NDB_TOKEN)
  stats, size  -json                machine output

Run "ndb <command> -h" for the flags of one command.
`, "\n")
