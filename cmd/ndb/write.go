package main

import (
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"

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
// and the write operators, like the body of POST /v1/c/{coll}/update. With an
// _id as second argument, "ndb update <collection> <_id> '<ops json>'" updates
// that one document.
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

	path, body, err := updateTarget(pos)
	if err != nil {
		return err
	}

	c, err := ttlClient(cf, fl, "update")
	if err != nil {
		return err
	}

	q := url.Values{}

	setFlag(q, "many", *many && !byID(pos))
	setFlag(q, "upsert", *upsert)

	if *expireIn != "" {
		q.Set("expire_in", *expireIn)
	}

	var rep updateReport

	if err := c.post(path+encodeQuery(q), body, &rep); err != nil {
		return err
	}

	if *cf.asJSON {
		return writeJSON(out, rep)
	}

	renderTTLTable(out, []string{"matched", "modified"}, []string{strconv.Itoa(rep.Matched), strconv.Itoa(rep.Modified)})

	return nil
}

// byID tells the _id form of update and delete: the second argument is not
// a JSON object.
func byID(pos []string) bool {
	return len(pos) >= 2 && !strings.HasPrefix(strings.TrimSpace(pos[1]), "{")
}

// updateTarget is the path and the body of an update, in the filter form or
// in the _id form.
func updateTarget(pos []string) (string, []byte, error) {
	switch {
	case len(pos) == 3 && byID(pos):
		return "/v1/c/" + url.PathEscape(pos[0]) + "/" + url.PathEscape(pos[1]) + "/update", []byte(pos[2]), nil
	case len(pos) == 2 && !byID(pos):
		return "/v1/c/" + url.PathEscape(pos[0]) + "/update", []byte(pos[1]), nil
	}

	return "", nil, errors.New(`usage: ndb update <collection> '<json>' | <_id> '<ops json>' [--many] [--upsert] [--expire-in 60s] [-url u] [-json]`)
}

func encodeQuery(q url.Values) string {
	if len(q) == 0 {
		return ""
	}

	return "?" + q.Encode()
}

// runDelete is "ndb delete <collection> '<json>'": the JSON is a filter. A
// plain _id instead of the JSON deletes that one document.
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
		return errors.New(`usage: ndb delete <collection> '<json>' | <_id> [--many] [-url u] [-json]`)
	}

	c, err := ttlClient(cf, fl, "delete")
	if err != nil {
		return err
	}

	q := url.Values{}

	setFlag(q, "many", *many)

	var rep deleteReport

	if byID(pos) {
		err = c.del("/v1/c/"+url.PathEscape(pos[0])+"/"+url.PathEscape(pos[1]), &rep)
	} else {
		err = c.post(writePath(pos[0], "delete", q), []byte(pos[1]), &rep)
	}

	if err != nil {
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
