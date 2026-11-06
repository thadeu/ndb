package main

import (
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/url"
	"os"
	"strconv"

	"github.com/thadeu/ndb/internal/tui"
)

type updateReport struct {
	Matched  int             `json:"matched"`
	Modified int             `json:"modified"`
	Doc      json.RawMessage `json:"doc,omitempty"`
}

type deleteReport struct {
	Deleted int `json:"deleted"`
}

// runUpdate is "ndb update <collection> '<json>'": the JSON mixes the filter
// and the write operators, like the body of POST /v1/c/{coll}/update.
func runUpdate(args []string) error {
	return update(args, os.Stdout)
}

func update(args []string, out io.Writer) error {
	fl := flag.NewFlagSet("ndb update", flag.ContinueOnError)

	cf := addConnFlags(fl)
	many := fl.Bool("many", false, "update every match (the filter needs the id or an indexed field)")
	upsert := fl.Bool("upsert", false, "create the document when nothing matches")
	expireIn := fl.String("expire-in", "", "set the expiry to now + duration (30m, 1h, 3600)")

	pos, err := parseInterspersed(fl, args)
	if err != nil {
		return err
	}

	if len(pos) != 2 {
		return errors.New(`usage: ndb update <collection> '<json>' [--many] [--upsert] [--expire-in 60s] [-url u] [-json]`)
	}

	c, err := ttlClient(cf, fl, "update")
	if err != nil {
		return err
	}

	q := url.Values{}

	setFlag(q, "many", *many)
	setFlag(q, "upsert", *upsert)

	if *expireIn != "" {
		q.Set("expire_in", *expireIn)
	}

	var rep updateReport

	if err := c.post(writePath(pos[0], "update", q), []byte(pos[1]), &rep); err != nil {
		return err
	}

	if *cf.asJSON {
		return writeJSON(out, rep)
	}

	renderTTLTable(out, []string{"matched", "modified"}, []string{strconv.Itoa(rep.Matched), strconv.Itoa(rep.Modified)})

	return nil
}

// runDelete is "ndb delete <collection> '<json>'": the JSON is a filter.
func runDelete(args []string) error {
	return remove(args, os.Stdout)
}

func remove(args []string, out io.Writer) error {
	fl := flag.NewFlagSet("ndb delete", flag.ContinueOnError)

	cf := addConnFlags(fl)
	many := fl.Bool("many", false, "delete every match (the filter needs the id or an indexed field)")

	pos, err := parseInterspersed(fl, args)
	if err != nil {
		return err
	}

	if len(pos) != 2 {
		return errors.New(`usage: ndb delete <collection> '<json>' [--many] [-url u] [-json]`)
	}

	c, err := ttlClient(cf, fl, "delete")
	if err != nil {
		return err
	}

	q := url.Values{}

	setFlag(q, "many", *many)

	var rep deleteReport

	if err := c.post(writePath(pos[0], "delete", q), []byte(pos[1]), &rep); err != nil {
		return err
	}

	if *cf.asJSON {
		return writeJSON(out, rep)
	}

	tb := tui.Table{Columns: []tui.Column{{Title: "deleted", Keep: true}}, Rows: [][]string{{strconv.Itoa(rep.Deleted)}}}

	tb.Render(out, styleFor(out))

	return nil
}

func setFlag(q url.Values, name string, on bool) {
	if on {
		q.Set(name, "true")
	}
}

func writePath(coll, op string, q url.Values) string {
	path := "/v1/c/" + url.PathEscape(coll) + "/" + op

	if len(q) > 0 {
		path += "?" + q.Encode()
	}

	return path
}
