package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/tui"
)

// runCollections is "ndb collections [name]": the catalog of a running
// daemon, or the definition and index state of one collection.
func runCollections(args []string) error {
	return collections(args, os.Stdout)
}

func collections(args []string, out io.Writer) error {
	fl := flag.NewFlagSet("ndb collections", flag.ContinueOnError)

	cf := addConnFlags(fl)

	pos, err := parseInterspersed(fl, args)
	if err != nil {
		return err
	}

	if len(pos) > 1 {
		return errors.New("usage: ndb collections [name] [-url u] [-json]")
	}

	t, err := cf.resolve(fl)
	if err != nil {
		return fmt.Errorf("collections: %w", err)
	}

	c, err := t.need()
	if err != nil {
		return fmt.Errorf("collections: %w", err)
	}

	if len(pos) == 1 {
		return showCollection(c, pos[0], *cf.asJSON, out)
	}

	var list struct {
		Collections []catalog.Def `json:"collections"`
	}

	if err := c.get("/v1/c", &list); err != nil {
		return err
	}

	if *cf.asJSON {
		return writeJSON(out, list)
	}

	printCollections(out, list.Collections)

	return nil
}

func printCollections(out io.Writer, defs []catalog.Def) {
	tb := tui.Table{
		Columns: []tui.Column{
			{Title: "name", Keep: true},
			{Title: "write"},
			{Title: "time"},
			{Title: "unique"},
			{Title: "indexes"},
			{Title: "ttl"},
			{Title: "retention"},
			{Title: "path"},
		},
	}

	for _, d := range defs {
		tb.Rows = append(tb.Rows, []string{
			d.Name, dash(string(d.Write)), dash(d.Time), dash(strings.Join(d.Unique, ", ")), dash(strings.Join(d.Indexes, ", ")),
			dash(d.TTL), dash(d.Retention), dash(d.Path),
		})
	}

	tb.Render(out, styleFor(out))
}

type collectionBody struct {
	Definition catalog.Def `json:"definition"`
	Indexes    []indexStat `json:"indexes"`
}

func showCollection(c *apiClient, name string, asJSON bool, out io.Writer) error {
	var body collectionBody

	if err := c.get("/v1/c/"+url.PathEscape(name), &body); err != nil {
		return err
	}

	if asJSON {
		return writeJSON(out, body)
	}

	d := body.Definition

	tb := tui.Table{Columns: []tui.Column{{Title: "field", Keep: true}, {Title: "value"}}}

	for _, kv := range [][2]string{
		{"name", d.Name},
		{"write", string(d.Write)},
		{"time", d.Time},
		{"path", d.Path},
		{"durability", string(d.Durability)},
		{"retention", d.Retention},
		{"wal_retention", d.WALRetention},
		{"ttl", d.TTL},
		{"expires_field", d.ExpiresField},
		{"unique", strings.Join(d.Unique, ", ")},
		{"indexes", indexList(body.Indexes)},
	} {
		tb.Rows = append(tb.Rows, []string{kv[0], dash(kv[1])})
	}

	tb.Render(out, styleFor(out))

	return nil
}
