package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"time"

	"github.com/thadeu/ndb/internal/tui"
)

type compactSize struct {
	Segments int   `json:"segments"`
	Bytes    int64 `json:"bytes"`
}

type compactReport struct {
	Collection string      `json:"collection"`
	Before     compactSize `json:"before"`
	After      compactSize `json:"after"`
	DurationMS int64       `json:"duration_ms"`
}

// runCompact is "ndb compact <collection>": seal and merge the segments of an
// engine-mode collection now. It needs a running daemon.
func runCompact(args []string) error {
	return compact(args, os.Stdout)
}

func compact(args []string, out io.Writer) error {
	fl := flag.NewFlagSet("ndb compact", flag.ContinueOnError)

	cf := addConnFlags(fl)

	pos, err := parseInterspersed(fl, args)
	if err != nil {
		return err
	}

	if len(pos) != 1 {
		return errors.New("usage: ndb compact <collection> [-url u] [-json]")
	}

	t, err := cf.resolve(fl)
	if err != nil {
		return fmt.Errorf("compact: %w", err)
	}

	c, err := t.need()
	if err != nil {
		return fmt.Errorf("compact: %w", err)
	}

	c.timeout = 10 * time.Minute

	var rep compactReport

	if err := c.post("/v1/c/"+url.PathEscape(pos[0])+"/compact", nil, &rep); err != nil {
		return err
	}

	if *cf.asJSON {
		return writeJSON(out, rep)
	}

	tb := tui.Table{
		Columns: []tui.Column{
			{Title: "collection", Keep: true},
			{Title: "segments", Align: tui.Right},
			{Title: "size", Align: tui.Right},
			{Title: "time", Align: tui.Right},
		},
		Rows: [][]string{{
			rep.Collection,
			fmt.Sprintf("%s -> %s", tui.Thousands(int64(rep.Before.Segments)), tui.Thousands(int64(rep.After.Segments))),
			fmt.Sprintf("%s -> %s", tui.HumanBytes(rep.Before.Bytes), tui.HumanBytes(rep.After.Bytes)),
			(time.Duration(rep.DurationMS) * time.Millisecond).String(),
		}},
	}

	tb.Render(out, styleFor(out))

	return nil
}
