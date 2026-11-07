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

// runUpdate is "ndb update <collection> '<query>' '<update>'": the atomic
// update of POST /{coll}/_update_by_query. With an _id as second argument,
// "ndb update <collection> <_id> '<update>'" updates that one document
// (POST /{coll}/_update/{id}).
func runUpdate(args []string) error {
	return update(args, os.Stdout)
}

func update(args []string, out io.Writer) error {
	fl := flag.NewFlagSet("ndb update", flag.ContinueOnError)

	cf := addConnFlags(fl)
	many := fl.Bool("many", false, "update every match (the query needs the id or an indexed field)")
	upsert := fl.Bool("upsert", false, "create the document when nothing matches")
	expireIn := fl.String("expire-in", "", "set the expiry to now + duration (30m, 1h, 3600)")

	pos, err := parseInterspersed(fl, args)
	if err != nil {
		return err
	}

	path, req, err := updateTarget(pos)
	if err != nil {
		return err
	}

	if *many && !byID(pos) {
		req["many"] = true
	}

	if *upsert {
		req["upsert"] = true
	}

	if *expireIn != "" {
		req["expire_in"] = durationArg(*expireIn)
	}

	body, err := json.Marshal(req)
	if err != nil {
		return err
	}

	c, err := ttlClient(cf, fl, "update")
	if err != nil {
		return err
	}

	var rep updateReport

	if err := c.post(path, body, &rep); err != nil {
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

// updateTarget is the path and the body of an update, in the query form or
// in the _id form.
func updateTarget(pos []string) (string, map[string]any, error) {
	usage := errors.New(`usage: ndb update <collection> '<query json>' '<update json>' | <_id> '<update json>' [--many] [--upsert] [--expire-in 60s] [-url u] [-json]`)

	if len(pos) != 3 {
		return "", nil, usage
	}

	update, err := validJSON("update", pos[2], '{')
	if err != nil {
		return "", nil, err
	}

	if byID(pos) {
		return docPath(pos[0], "_update", pos[1]), map[string]any{"update": update}, nil
	}

	q, err := validJSON("query", pos[1], '{')
	if err != nil {
		return "", nil, err
	}

	return "/" + url.PathEscape(pos[0]) + "/_update_by_query", map[string]any{"query": q, "update": update}, nil
}

// docPath is /{coll}/{action}/{id}.
func docPath(coll, action, id string) string {
	return "/" + url.PathEscape(coll) + "/" + action + "/" + url.PathEscape(id)
}

// runDelete is "ndb delete <collection> '<query>'"; a plain _id instead of
// the JSON deletes that one document.
func runDelete(args []string) error {
	return remove(args, os.Stdout)
}

func remove(args []string, out io.Writer) error {
	fl := flag.NewFlagSet("ndb delete", flag.ContinueOnError)

	cf := addConnFlags(fl)
	many := fl.Bool("many", false, "delete every match (the query needs the id or an indexed field)")

	pos, err := parseInterspersed(fl, args)
	if err != nil {
		return err
	}

	if len(pos) != 2 {
		return errors.New(`usage: ndb delete <collection> '<query json>' | <_id> [--many] [-url u] [-json]`)
	}

	c, err := ttlClient(cf, fl, "delete")
	if err != nil {
		return err
	}

	var rep deleteReport

	if byID(pos) {
		err = c.del(docPath(pos[0], "_doc", pos[1]), &rep)
	} else {
		var q json.RawMessage

		if q, err = validJSON("query", pos[1], '{'); err != nil {
			return err
		}

		body, _ := json.Marshal(map[string]any{"query": q, "many": *many})
		err = c.post("/"+url.PathEscape(pos[0])+"/_delete_by_query", body, &rep)
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
