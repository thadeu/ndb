package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/thadeu/ndb/internal/config"
)

// subcommands maps the first argument to its command. Anything else, flags
// included, starts the daemon, so "ndb -data-dir d" keeps working.
var subcommands = map[string]func(args []string) error{
	"backup":  runBackup,
	"restore": runRestore,
	"stats":   runStats,
	"size":    runSize,

	"collections": runCollections,
	"compact":     runCompact,
	"expire":      runExpire,
	"persist":     runPersist,
	"ttl":         runTTL,
	"quarantine":  runQuarantine,
	"query":       runQuery,
	"version":     runVersion,
	"help":        runHelp,
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
  ndb collections [name]        the catalog, or the definition of one collection
  ndb compact <collection>      seal and merge the segments now (engine mode)
  ndb expire <coll> <id> <dur>  the document expires <dur> from now (30m, 1h, 3600)
  ndb persist <coll> <id>       remove the expiry of a document
  ndb ttl <coll> <id>           time left of a document (-1 = never expires)
  ndb quarantine <collection>   bad lines of a direct collection (file, offset, reason)
  ndb query <collection> ['<where-json>']
                                find rows; -aggregate '<pipeline-json>' aggregates
  ndb version                   print the version (also: ndb -version)
  ndb help                      print this text

Common flags:
  -config FILE      ndb.yml (default ndb.yml; absent = defaults)
  -data-dir DIR     data dir (overrides ndb.yml)

Talking to the daemon (stats, size, collections, compact, expire, persist, ttl, quarantine, query):
  -url unix:///sock|tcp://host:port   which daemon (default NDB_URL, then the
                                      unix socket of ndb.yml); NDB_TOKEN is the bearer token
  -json                               machine output (query: NDJSON rows)
  stats and size read the data dir when no daemon answers, or with -data-dir.
  The other commands need a daemon.

Per command:
  restore     -force                  replace a data dir that is not empty
  quarantine  -limit N                last N bad lines (default 20, at most 1000)
  query       -sort JSON -fields a,b -limit N -aggregate JSON -ndjson

Tables fit the terminal width (COLUMNS when there is no tty size), use ASCII
borders when the locale is not UTF-8 or TERM=dumb, and no color with NO_COLOR.
Piped output is not cut.

Run "ndb <command> -h" for the flags of one command.
`, "\n")
