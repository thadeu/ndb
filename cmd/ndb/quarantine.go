package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"

	"github.com/thadeu/ndb/internal/tui"
)

type quarantineRow struct {
	TS        string `json:"ts"`
	File      string `json:"file"`
	Offset    int64  `json:"offset"`
	Error     string `json:"error"`
	Line      string `json:"line"`
	Truncated bool   `json:"truncated,omitempty"`
}

type quarantineReport struct {
	Collection string          `json:"collection"`
	Total      int             `json:"total"`
	Rows       []quarantineRow `json:"rows"`
}

// runQuarantine is "ndb quarantine <collection>": the lines of a direct
// collection that ndb could not apply, with file, offset and reason.
func runQuarantine(args []string) error {
	return quarantine(args, os.Stdout)
}

func quarantine(args []string, out io.Writer) error {
	fl := flag.NewFlagSet("ndb quarantine", flag.ContinueOnError)

	cf := addConnFlags(fl)
	limit := fl.Int("limit", 20, "show the last N bad lines (1-1000)")

	pos, err := parseInterspersed(fl, args)
	if err != nil {
		return err
	}

	if len(pos) != 1 {
		return errors.New("usage: ndb quarantine <collection> [-limit N] [-url u] [-json]")
	}

	t, err := cf.resolve(fl)
	if err != nil {
		return fmt.Errorf("quarantine: %w", err)
	}

	c, err := t.need()
	if err != nil {
		return fmt.Errorf("quarantine: %w", err)
	}

	var rep quarantineReport

	if err := c.get(fmt.Sprintf("/%s/_quarantine?limit=%d", url.PathEscape(pos[0]), *limit), &rep); err != nil {
		return err
	}

	if *cf.asJSON {
		return writeJSON(out, rep)
	}

	printQuarantine(out, rep)

	return nil
}

func printQuarantine(out io.Writer, rep quarantineReport) {
	if len(rep.Rows) == 0 {
		fmt.Fprintf(out, "%s: no quarantined lines\n", rep.Collection)

		return
	}

	tb := tui.Table{
		Columns: []tui.Column{
			{Title: "file"},
			{Title: "offset", Align: tui.Right},
			{Title: "error"},
			{Title: "line"},
		},
	}

	for _, r := range rep.Rows {
		tb.Rows = append(tb.Rows, []string{r.File, tui.Thousands(r.Offset), r.Error, r.Line})
	}

	tb.Render(out, styleFor(out))

	fmt.Fprintf(out, "showing %d of %d quarantined lines (newest last)\n", len(rep.Rows), rep.Total)
}
