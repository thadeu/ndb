package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
)

type indexStat struct {
	Field string `json:"field"`
	State string `json:"state"`
}

// collectionStats has pointers where a source may not know the value: the
// HTTP API has no sizes, a stopped daemon has no row counts.
type collectionStats struct {
	Name         string      `json:"name"`
	Write        string      `json:"write,omitempty"`
	Rows         *int        `json:"rows,omitempty"`
	Quarantined  *int64      `json:"quarantined,omitempty"`
	Indexes      []indexStat `json:"indexes,omitempty"`
	Bytes        *int64      `json:"bytes,omitempty"`
	SegmentFiles *int        `json:"segment_files,omitempty"`
	WALFiles     *int        `json:"wal_files,omitempty"`
}

type statsReport struct {
	Source      string            `json:"source"`
	DataDir     string            `json:"data_dir,omitempty"`
	Collections []collectionStats `json:"collections"`
}

// runStats is "ndb stats". With -url, or a reachable socket from ndb.yml, or
// NDB_URL, it asks the daemon for rows, indexes and quarantine. Sizes and file
// counts come from the data dir when it is readable. With -data-dir, or when
// no daemon answers, it reads the data dir only.
func runStats(args []string) error {
	return stats(args, os.Stdout)
}

func stats(args []string, out io.Writer) error {
	fl := flag.NewFlagSet("ndb stats", flag.ContinueOnError)

	common := addDataDirFlags(fl)
	rawURL := fl.String("url", "", "running daemon: unix:///path/to.sock or tcp://host:port (default NDB_URL)")
	asJSON := fl.Bool("json", false, "machine output")

	if err := fl.Parse(args); err != nil {
		return err
	}

	if fl.NArg() != 0 {
		return errors.New("usage: ndb stats [-config f] [-data-dir d | -url u] [-json]")
	}

	if *rawURL != "" && flagSet(fl, "data-dir") {
		return errors.New("stats: use -url or -data-dir, not both")
	}

	cfg, err := common.load(fl)
	if err != nil {
		return err
	}

	rep, err := collectStats(fl, *rawURL, cfg.DataDir, cfg.Listen.Unix)
	if err != nil {
		return err
	}

	if *asJSON {
		return writeJSON(out, rep)
	}

	printStats(out, rep)

	return nil
}

func collectStats(fl *flag.FlagSet, rawURL, dataDir, cfgSock string) (statsReport, error) {
	if rawURL == "" && !flagSet(fl, "data-dir") {
		rawURL = os.Getenv("NDB_URL")
	}

	switch {
	case rawURL != "":
		c, err := newAPIClient(rawURL)
		if err != nil {
			return statsReport{}, err
		}

		return statsOnline(c, rawURL, dataDir)
	case flagSet(fl, "data-dir") || cfgSock == "":
		return statsOffline(dataDir)
	}

	rep, err := statsOnline(newUnixClient(cfgSock, os.Getenv("NDB_TOKEN")), "unix://"+cfgSock, dataDir)
	if err != nil {
		return statsOffline(dataDir)
	}

	return rep, nil
}

// statsOnline asks the daemon, then adds sizes from dataDir if it is there.
func statsOnline(c *apiClient, source, dataDir string) (statsReport, error) {
	rep := statsReport{Source: source}

	var list struct {
		Collections []struct {
			Name string `json:"name"`
		} `json:"collections"`
	}

	if err := c.get("/v1/c", &list); err != nil {
		return rep, err
	}

	for _, d := range list.Collections {
		var st struct {
			Write       string      `json:"write"`
			Rows        int         `json:"rows"`
			Quarantined int64       `json:"quarantined"`
			Indexes     []indexStat `json:"indexes"`
		}

		if err := c.get("/v1/c/"+url.PathEscape(d.Name)+"/stats", &st); err != nil {
			return rep, err
		}

		rep.Collections = append(rep.Collections, collectionStats{
			Name: d.Name, Write: st.Write, Rows: &st.Rows, Quarantined: &st.Quarantined, Indexes: st.Indexes,
		})
	}

	if u, err := scanUsage(dataDir); err == nil {
		rep.DataDir = dataDir
		mergeUsage(rep.Collections, u)
	}

	return rep, nil
}

func statsOffline(dataDir string) (statsReport, error) {
	rep := statsReport{Source: "data-dir", DataDir: dataDir}

	u, err := scanUsage(dataDir)
	if err != nil {
		return rep, fmt.Errorf("stats: %w", err)
	}

	for _, c := range u.Collections {
		rep.Collections = append(rep.Collections, collectionStats{Name: c.Name})
	}

	mergeUsage(rep.Collections, u)

	return rep, nil
}

func mergeUsage(cols []collectionStats, u usage) {
	byName := map[string]collectionUsage{}

	for _, c := range u.Collections {
		byName[c.Name] = c
	}

	for i := range cols {
		c, ok := byName[cols[i].Name]
		if !ok {
			continue
		}

		bytes, segs, wals := c.Total, c.SegmentFiles, c.WALFiles

		cols[i].Bytes, cols[i].SegmentFiles, cols[i].WALFiles = &bytes, &segs, &wals
	}
}

func printStats(out io.Writer, rep statsReport) {
	tw := newTable(out)

	fmt.Fprintln(tw, "COLLECTION\tMODE\tROWS\tSIZE\tSEGMENTS\tWAL\tQUARANTINED\tINDEXES")

	for _, c := range rep.Collections {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", c.Name, dash(c.Write),
			numOrDash(c.Rows), sizeOrDash(c.Bytes), numOrDash(c.SegmentFiles),
			numOrDash(c.WALFiles), numOrDash(c.Quarantined), indexList(c.Indexes))
	}

	tw.Flush()

	if rep.Source == "data-dir" {
		fmt.Fprintln(out, "\nrows, quarantine and indexes need a running daemon (-url or a reachable socket)")
	}
}

func dash(s string) string {
	if s == "" {
		return "-"
	}

	return s
}

func numOrDash[T int | int64](n *T) string {
	if n == nil {
		return "-"
	}

	return fmt.Sprint(*n)
}

func sizeOrDash(n *int64) string {
	if n == nil {
		return "-"
	}

	return humanBytes(*n)
}

func indexList(ix []indexStat) string {
	if len(ix) == 0 {
		return "-"
	}

	parts := make([]string, len(ix))

	for i, x := range ix {
		parts[i] = x.Field + "(" + x.State + ")"
	}

	return strings.Join(parts, ",")
}
