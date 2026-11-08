package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/tui"
)

// pitrRestore is "ndb restore <collection> -until T -into NAME": a copy of
// the collection as it was at T, made by the daemon from its PITR window.
func pitrRestore(args []string, out io.Writer) error {
	fl := flag.NewFlagSet("ndb restore", flag.ContinueOnError)

	cf := addConnFlags(fl)
	until := fl.String("until", "", "the instant: RFC 3339 (2026-11-12T14:04:00Z) or a time ago (15m, 2h)")
	into := fl.String("into", "", "the new collection that gets the copy")

	pos, err := parseInterspersed(fl, args)
	if err != nil {
		return err
	}

	if len(pos) != 1 || *until == "" || *into == "" {
		return errors.New("usage: ndb restore <collection> -until T -into NAME [-url u] [-json]")
	}

	at, err := parseUntil(*until, time.Now())
	if err != nil {
		return err
	}

	t, err := cf.resolve(fl)
	if err != nil {
		return fmt.Errorf("restore: %w", err)
	}

	c, err := t.need()
	if err != nil {
		return fmt.Errorf("restore: %w", err)
	}

	c.timeout = 30 * time.Minute

	body, err := json.Marshal(map[string]string{"until": at.UTC().Format(time.RFC3339Nano), "into": *into})
	if err != nil {
		return err
	}

	var rep map[string]any

	if err := c.post("/"+url.PathEscape(pos[0])+"/_restore", body, &rep); err != nil {
		return err
	}

	if *cf.asJSON {
		return writeJSON(out, rep)
	}

	fmt.Fprintf(out, "%s: restored into %s as of %s\n", pos[0], *into, at.UTC().Format(time.RFC3339))

	return nil
}

// isPITRRestore tells a PITR restore (-until) from a backup restore.
func isPITRRestore(args []string) bool {
	return slices.ContainsFunc(args, func(a string) bool {
		name, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")

		return strings.HasPrefix(a, "-") && name == "until"
	})
}

// parseUntil reads an RFC 3339 instant, or a duration ago (15m, 2h, 1d).
func parseUntil(s string, now time.Time) (time.Time, error) {
	if at, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return at, nil
	}

	ago, err := catalog.ParseDuration(s)
	if err != nil {
		return time.Time{}, fmt.Errorf("-until %q: want an RFC 3339 time or a time ago such as 15m", s)
	}

	return now.Add(-ago), nil
}

type pitrRow struct {
	Collection string    `json:"collection"`
	Window     string    `json:"window"`
	Oldest     time.Time `json:"oldest"`
	Newest     time.Time `json:"newest"`
	Snapshots  int       `json:"snapshots"`
	Bytes      int64     `json:"bytes"`
	Dropped    bool      `json:"dropped"`
}

// runPITR is "ndb pitr [collection]": the PITR window of every collection,
// or of one.
func runPITR(args []string) error {
	return pitrList(args, os.Stdout)
}

func pitrList(args []string, out io.Writer) error {
	fl := flag.NewFlagSet("ndb pitr", flag.ContinueOnError)

	cf := addConnFlags(fl)

	pos, err := parseInterspersed(fl, args)
	if err != nil {
		return err
	}

	if len(pos) > 1 {
		return errors.New("usage: ndb pitr [collection] [-url u] [-json]")
	}

	t, err := cf.resolve(fl)
	if err != nil {
		return fmt.Errorf("pitr: %w", err)
	}

	c, err := t.need()
	if err != nil {
		return fmt.Errorf("pitr: %w", err)
	}

	var rows []pitrRow

	if len(pos) == 1 {
		var one pitrRow

		err = c.get("/"+url.PathEscape(pos[0])+"/_pitr", &one)
		rows = []pitrRow{one}
	} else {
		var all struct {
			Collections []pitrRow `json:"collections"`
		}

		err = c.get("/_pitr", &all)
		rows = all.Collections
	}

	if err != nil {
		return err
	}

	if *cf.asJSON {
		return writeJSON(out, rows)
	}

	tb := tui.Table{
		Columns: []tui.Column{
			{Title: "collection", Keep: true},
			{Title: "window"},
			{Title: "restorable from"},
			{Title: "snapshots", Align: tui.Right},
			{Title: "disk", Align: tui.Right},
		},
	}

	for _, r := range rows {
		from := "-"

		if !r.Oldest.IsZero() {
			from = r.Oldest.Local().Format("2006-01-02 15:04:05")
		}

		name := r.Collection

		if r.Dropped {
			name += " (dropped)"
		}

		tb.Rows = append(tb.Rows, []string{name, r.Window, from, tui.Thousands(int64(r.Snapshots)), tui.HumanBytes(r.Bytes)})
	}

	tb.Render(out, styleFor(out))

	return nil
}
