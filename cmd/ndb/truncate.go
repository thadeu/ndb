package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"golang.org/x/term"
)

// runTruncate is "ndb truncate <collection>": remove every document, keep the
// definition and the indexes.
func runTruncate(args []string) error {
	return swapCommand("truncate", args, os.Stdin, os.Stdout)
}

// runDrop is "ndb drop <collection>": remove the collection, its definition
// and its watch consumers.
func runDrop(args []string) error {
	return swapCommand("drop", args, os.Stdin, os.Stdout)
}

// swapCommand runs truncate or drop. Without -yes it asks the user to type
// the collection name, and it refuses when stdin is not a terminal.
func swapCommand(verb string, args []string, in io.Reader, out io.Writer) error {
	fl := flag.NewFlagSet("ndb "+verb, flag.ContinueOnError)

	cf := addConnFlags(fl)
	yes := fl.Bool("yes", false, "do not ask for confirmation")

	pos, err := parseInterspersed(fl, args)
	if err != nil {
		return err
	}

	if len(pos) != 1 {
		return fmt.Errorf("usage: ndb %s <collection> [-yes] [-url u] [-json]", verb)
	}

	name := pos[0]

	if !*yes {
		if err := confirmName(verb, name, in, out); err != nil {
			return err
		}
	}

	t, err := cf.resolve(fl)
	if err != nil {
		return fmt.Errorf("%s: %w", verb, err)
	}

	c, err := t.need()
	if err != nil {
		return fmt.Errorf("%s: %w", verb, err)
	}

	path := "/" + url.PathEscape(name)
	query := "?confirm=" + url.QueryEscape(name)

	var rep map[string]any

	if verb == "truncate" {
		err = c.post(path+"/_truncate"+query, nil, &rep)
	} else {
		err = c.del(path+query, &rep)
	}

	if err != nil {
		return err
	}

	if *cf.asJSON {
		return writeJSON(out, rep)
	}

	past := map[string]string{"truncate": "truncated", "drop": "dropped"}[verb]

	fmt.Fprintf(out, "%s: %s\n", name, past)

	return nil
}

func confirmName(verb, name string, in io.Reader, out io.Writer) error {
	if f, ok := in.(*os.File); !ok || !term.IsTerminal(int(f.Fd())) {
		return fmt.Errorf("%s %s: stdin is not a terminal; add -yes to confirm", verb, name)
	}

	fmt.Fprintf(out, "This will %s %q. Type the collection name to confirm: ", verb, name)

	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}

	if strings.TrimSpace(line) != name {
		return fmt.Errorf("%s %s: canceled, the name did not match", verb, name)
	}

	return nil
}
