package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/thadeu/ndb/internal/diskusage"
	"github.com/thadeu/ndb/internal/tui"
)

type indexStat struct {
	Field string `json:"field"`
	State string `json:"state"`
	Done  int    `json:"done,omitempty"`
	Total int    `json:"total,omitempty"`
}

type partBytes struct {
	WAL      int64 `json:"wal"`
	Segments int64 `json:"segments"`
	Meta     int64 `json:"meta"`
}

// collectionStats has pointers where a source may not know the value: a
// stopped daemon has no row counts.
type collectionStats struct {
	Name        string      `json:"name"`
	Write       string      `json:"write,omitempty"`
	Rows        *int        `json:"rows,omitempty"`
	Quarantined *int64      `json:"quarantined,omitempty"`
	Indexes     []indexStat `json:"indexes,omitempty"`
	Segments    int         `json:"segments"`
	WALFiles    int         `json:"wal_files"`
	Bytes       partBytes   `json:"bytes"`
	Total       int64       `json:"total"`
}

type statsTotals struct {
	Collections int    `json:"collections"`
	Rows        *int   `json:"rows,omitempty"`
	Quarantined *int64 `json:"quarantined,omitempty"`
	Segments    int    `json:"segments"`
	WALFiles    int    `json:"wal_files"`
	Bytes       int64  `json:"bytes"`
}

type statsReport struct {
	Source        string            `json:"source"`
	DataDir       string            `json:"data_dir,omitempty"`
	Version       string            `json:"version,omitempty"`
	UptimeSeconds *float64          `json:"uptime_seconds,omitempty"`
	Collections   []collectionStats `json:"collections"`
	Totals        statsTotals       `json:"totals"`
	BlobsBytes    int64             `json:"blobs_bytes"`
	CursorsBytes  int64             `json:"cursors_bytes"`
	SharedBytes   int64             `json:"shared_bytes"`
}

// runStats is "ndb stats". It asks the daemon (GET /v1/stats). With -data-dir,
// or when no daemon answers, it reads the data dir: sizes and file counts
// only, because rows, indexes and quarantine live in the daemon.
func runStats(args []string) error {
	return stats(args, os.Stdout)
}

func stats(args []string, out io.Writer) error {
	fl := flag.NewFlagSet("ndb stats", flag.ContinueOnError)

	cf := addConnFlags(fl)

	if err := fl.Parse(args); err != nil {
		return err
	}

	if fl.NArg() != 0 {
		return errors.New("usage: ndb stats [-config f] [-data-dir d | -url u] [-json]")
	}

	t, err := cf.resolve(fl)
	if err != nil {
		return fmt.Errorf("stats: %w", err)
	}

	rep, err := collectStats(t)
	if err != nil {
		return err
	}

	if *cf.asJSON {
		return writeJSON(out, rep)
	}

	printStats(out, rep)

	return nil
}

func collectStats(t target) (statsReport, error) {
	if t.client == nil {
		return statsOffline(t.cfg.DataDir)
	}

	rep := statsReport{Source: t.source}

	if err := t.client.get("/v1/stats", &rep); err != nil {
		return rep, err
	}

	rep.Source = t.source

	return rep, nil
}

func statsOffline(dataDir string) (statsReport, error) {
	rep := statsReport{Source: "data-dir", DataDir: dataDir, Collections: []collectionStats{}}

	u, err := diskusage.Scan(dataDir)
	if err != nil {
		return rep, fmt.Errorf("stats: %w", err)
	}

	for _, c := range u.Collections {
		rep.Collections = append(rep.Collections, collectionStats{
			Name:     c.Name,
			Segments: c.SegmentFiles,
			WALFiles: c.WALFiles,
			Bytes:    partBytes{WAL: c.Bytes.WAL, Segments: c.Bytes.Segments, Meta: c.Bytes.Meta},
			Total:    c.Total,
		})

		rep.Totals.Collections++
		rep.Totals.Segments += c.SegmentFiles
		rep.Totals.WALFiles += c.WALFiles
	}

	rep.Totals.Bytes = u.Total
	rep.BlobsBytes = u.Shared.Blobs
	rep.CursorsBytes = u.Shared.Cursors
	rep.SharedBytes = u.Shared.Total()

	return rep, nil
}

func printStats(out io.Writer, rep statsReport) {
	tb := tui.Table{
		Columns: []tui.Column{
			{Title: "collection", Keep: true},
			{Title: "mode"},
			{Title: "rows", Align: tui.Right},
			{Title: "size", Align: tui.Right},
			{Title: "segments", Align: tui.Right},
			{Title: "wal", Align: tui.Right},
			{Title: "quarantined", Align: tui.Right},
			{Title: "indexes"},
		},
	}

	for _, c := range rep.Collections {
		tb.Rows = append(tb.Rows, []string{
			c.Name, dash(c.Write), numOrDash(c.Rows), tui.HumanBytes(c.Total),
			tui.Thousands(int64(c.Segments)), tui.Thousands(int64(c.WALFiles)),
			numOrDash(c.Quarantined), indexList(c.Indexes),
		})
	}

	tb.Footer = []string{
		"total", "", numOrDash(rep.Totals.Rows), tui.HumanBytes(rep.Totals.Bytes),
		tui.Thousands(int64(rep.Totals.Segments)), tui.Thousands(int64(rep.Totals.WALFiles)),
		numOrDash(rep.Totals.Quarantined), "",
	}

	tb.Render(out, styleFor(out))

	fmt.Fprintln(out, statsFootnote(rep))
}

// statsFootnote is the line under the table: daemon identity and the files
// that belong to no collection.
func statsFootnote(rep statsReport) string {
	parts := []string{}

	if rep.Version != "" {
		parts = append(parts, "ndb "+rep.Version)
	}

	if rep.UptimeSeconds != nil {
		parts = append(parts, "up "+(time.Duration(*rep.UptimeSeconds)*time.Second).Round(time.Second).String())
	}

	parts = append(parts, "blobs "+tui.HumanBytes(rep.BlobsBytes), "cursors "+tui.HumanBytes(rep.CursorsBytes))

	line := strings.Join(parts, " · ")

	if rep.Source == "data-dir" {
		line += "\nrows, quarantine and indexes need a running daemon (-url or a reachable socket)"
	}

	return line
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

	return tui.Thousands(int64(*n))
}

func indexList(ix []indexStat) string {
	if len(ix) == 0 {
		return "-"
	}

	parts := make([]string, len(ix))

	for i, x := range ix {
		parts[i] = x.Field + " (" + indexState(x) + ")"
	}

	return strings.Join(parts, ", ")
}

func indexState(x indexStat) string {
	if x.State == "building" && x.Total > 0 {
		return fmt.Sprintf("building %d%%", x.Done*100/x.Total)
	}

	return x.State
}
