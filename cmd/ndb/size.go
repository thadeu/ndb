package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

// runSize is "ndb size": disk usage per collection and per part. It reads
// the data dir and works with the daemon stopped or running.
func runSize(args []string) error {
	return size(args, os.Stdout)
}

func size(args []string, out io.Writer) error {
	fl := flag.NewFlagSet("ndb size", flag.ContinueOnError)

	common := addDataDirFlags(fl)
	asJSON := fl.Bool("json", false, "machine output")

	if err := fl.Parse(args); err != nil {
		return err
	}

	if fl.NArg() != 0 {
		return errors.New("usage: ndb size [-config f] [-data-dir d] [-json]")
	}

	cfg, err := common.load(fl)
	if err != nil {
		return err
	}

	u, err := scanUsage(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("size: %w", err)
	}

	if *asJSON {
		return writeJSON(out, u)
	}

	printUsage(out, u)

	return nil
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)

	enc.SetIndent("", "  ")

	return enc.Encode(v)
}

func printUsage(out io.Writer, u usage) {
	tw := newTable(out)

	fmt.Fprintln(tw, "NAME\tWAL\tSEGMENTS\tMETA\tBLOBS\tCURSORS\tTOTAL")

	var sum parts

	for _, c := range u.Collections {
		printPartsRow(tw, c.Name, c.Bytes)
		sum.add(c.Bytes)
	}

	printPartsRow(tw, "(shared)", u.Shared)
	sum.add(u.Shared)

	printPartsRow(tw, "TOTAL", sum)

	tw.Flush()
}

func printPartsRow(w io.Writer, name string, p parts) {
	fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", name,
		humanBytes(p.WAL), humanBytes(p.Segments), humanBytes(p.Meta),
		humanBytes(p.Blobs), humanBytes(p.Cursors), humanBytes(p.total()))
}
