package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/thadeu/ndb/internal/diskusage"
	"github.com/thadeu/ndb/internal/tui"
)

// runSize is "ndb size": disk usage per collection and per part. It asks the
// daemon (GET /v1/size); with -data-dir, or when no daemon answers, it scans
// the data dir itself, which works with the daemon stopped or running.
func runSize(args []string) error {
	return size(args, os.Stdout)
}

func size(args []string, out io.Writer) error {
	fl := flag.NewFlagSet("ndb size", flag.ContinueOnError)

	cf := addConnFlags(fl)

	if err := fl.Parse(args); err != nil {
		return err
	}

	if fl.NArg() != 0 {
		return errors.New("usage: ndb size [-config f] [-data-dir d | -url u] [-json]")
	}

	t, err := cf.resolve(fl)
	if err != nil {
		return fmt.Errorf("size: %w", err)
	}

	u, err := collectUsage(t)
	if err != nil {
		return fmt.Errorf("size: %w", err)
	}

	if *cf.asJSON {
		return writeJSON(out, u)
	}

	printUsage(out, u)

	return nil
}

func collectUsage(t target) (diskusage.Usage, error) {
	if t.client == nil {
		return diskusage.Scan(t.cfg.DataDir)
	}

	var u diskusage.Usage

	err := t.client.get("/v1/size", &u)

	return u, err
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)

	enc.SetIndent("", "  ")

	return enc.Encode(v)
}

func printUsage(out io.Writer, u diskusage.Usage) {
	tb := tui.Table{
		Columns: []tui.Column{
			{Title: "name", Keep: true},
			{Title: "wal", Align: tui.Right},
			{Title: "segments", Align: tui.Right},
			{Title: "meta", Align: tui.Right},
			{Title: "blobs", Align: tui.Right},
			{Title: "cursors", Align: tui.Right},
			{Title: "total", Align: tui.Right},
		},
	}

	for _, c := range u.Collections {
		tb.Rows = append(tb.Rows, partsRow(c.Name, c.Bytes))
	}

	tb.Rows = append(tb.Rows, partsRow("(shared)", u.Shared))
	tb.Footer = partsRow("total", u.Sum())

	tb.Render(out, styleFor(out))
}

func partsRow(name string, p diskusage.Parts) []string {
	return []string{
		name, tui.HumanBytes(p.WAL), tui.HumanBytes(p.Segments), tui.HumanBytes(p.Meta),
		tui.HumanBytes(p.Blobs), tui.HumanBytes(p.Cursors), tui.HumanBytes(p.Total()),
	}
}
