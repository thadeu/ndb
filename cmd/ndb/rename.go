package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
)

// runRename is "ndb rename <collection> <to>": the collection takes the new
// name. With -replace, an existing target goes aside first; that asks for
// the target name unless -yes is set.
func runRename(args []string) error {
	return rename(args, os.Stdin, os.Stdout)
}

func rename(args []string, in io.Reader, out io.Writer) error {
	fl := flag.NewFlagSet("ndb rename", flag.ContinueOnError)

	cf := addConnFlags(fl)
	replace := fl.Bool("replace", false, "put the collection in place of an existing target")
	yes := fl.Bool("yes", false, "do not ask for confirmation of -replace")

	pos, err := parseInterspersed(fl, args)
	if err != nil {
		return err
	}

	if len(pos) != 2 {
		return errors.New("usage: ndb rename <collection> <to> [-replace] [-yes] [-url u] [-json]")
	}

	from, to := pos[0], pos[1]

	if *replace && !*yes {
		if err := confirmName("replace", to, in, out); err != nil {
			return err
		}
	}

	t, err := cf.resolve(fl)
	if err != nil {
		return fmt.Errorf("rename: %w", err)
	}

	c, err := t.need()
	if err != nil {
		return fmt.Errorf("rename: %w", err)
	}

	var rep map[string]any

	body, err := json.Marshal(map[string]any{"to": to, "replace": *replace})
	if err != nil {
		return err
	}

	if err := c.post("/"+url.PathEscape(from)+"/_rename", body, &rep); err != nil {
		return err
	}

	if *cf.asJSON {
		return writeJSON(out, rep)
	}

	fmt.Fprintf(out, "%s: renamed to %s\n", from, to)

	return nil
}
