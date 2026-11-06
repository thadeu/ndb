package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/thadeu/ndb/internal/tui"
)

type expireReport struct {
	ID        string `json:"_id"`
	V         uint64 `json:"v"`
	ExpiresAt string `json:"expires_at"`
}

type persistReport struct {
	ID string `json:"_id"`
	V  uint64 `json:"v"`
}

type ttlReport struct {
	ID        string  `json:"_id"`
	TTL       int64   `json:"ttl"`
	ExpiresAt *string `json:"expires_at"`
}

// runExpire is "ndb expire <collection> <_id> <duration>": the document
// expires that long from now. A bare number is seconds.
func runExpire(args []string) error {
	return expire(args, os.Stdout)
}

func expire(args []string, out io.Writer) error {
	fl := flag.NewFlagSet("ndb expire", flag.ContinueOnError)

	cf := addConnFlags(fl)

	pos, err := parseInterspersed(fl, args)
	if err != nil {
		return err
	}

	if len(pos) != 3 {
		return errors.New("usage: ndb expire <collection> <_id> <duration> [-url u] [-json]")
	}

	c, err := ttlClient(cf, fl, "expire")
	if err != nil {
		return err
	}

	body, err := json.Marshal(map[string]any{"_id": pos[1], "in": durationArg(pos[2])})
	if err != nil {
		return err
	}

	var rep expireReport

	if err := c.post("/v1/c/"+url.PathEscape(pos[0])+"/expire", body, &rep); err != nil {
		return err
	}

	if *cf.asJSON {
		return writeJSON(out, rep)
	}

	renderTTLTable(out, []string{"_id", "version", "expires at"}, []string{rep.ID, strconv.FormatUint(rep.V, 10), rep.ExpiresAt})

	return nil
}

// runPersist is "ndb persist <collection> <_id>": the document stops expiring.
func runPersist(args []string) error {
	return persist(args, os.Stdout)
}

func persist(args []string, out io.Writer) error {
	fl := flag.NewFlagSet("ndb persist", flag.ContinueOnError)

	cf := addConnFlags(fl)

	pos, err := parseInterspersed(fl, args)
	if err != nil {
		return err
	}

	if len(pos) != 2 {
		return errors.New("usage: ndb persist <collection> <_id> [-url u] [-json]")
	}

	c, err := ttlClient(cf, fl, "persist")
	if err != nil {
		return err
	}

	body, err := json.Marshal(map[string]any{"_id": pos[1]})
	if err != nil {
		return err
	}

	var rep persistReport

	if err := c.post("/v1/c/"+url.PathEscape(pos[0])+"/persist", body, &rep); err != nil {
		return err
	}

	if *cf.asJSON {
		return writeJSON(out, rep)
	}

	renderTTLTable(out, []string{"_id", "version"}, []string{rep.ID, strconv.FormatUint(rep.V, 10)})

	return nil
}

// runTTL is "ndb ttl <collection> <_id>": the time left, or -1 when the
// document never expires.
func runTTL(args []string) error {
	return ttl(args, os.Stdout)
}

func ttl(args []string, out io.Writer) error {
	fl := flag.NewFlagSet("ndb ttl", flag.ContinueOnError)

	cf := addConnFlags(fl)

	pos, err := parseInterspersed(fl, args)
	if err != nil {
		return err
	}

	if len(pos) != 2 {
		return errors.New("usage: ndb ttl <collection> <_id> [-url u] [-json]")
	}

	c, err := ttlClient(cf, fl, "ttl")
	if err != nil {
		return err
	}

	var rep ttlReport

	if err := c.get("/v1/c/"+url.PathEscape(pos[0])+"/"+url.PathEscape(pos[1])+"/ttl", &rep); err != nil {
		return err
	}

	if *cf.asJSON {
		return writeJSON(out, rep)
	}

	left, at := "never expires", "-"

	if rep.TTL >= 0 {
		left = (time.Duration(rep.TTL) * time.Second).String()
	}

	if rep.ExpiresAt != nil {
		at = *rep.ExpiresAt
	}

	renderTTLTable(out, []string{"_id", "ttl", "seconds", "expires at"}, []string{rep.ID, left, strconv.FormatInt(rep.TTL, 10), at})

	return nil
}

func ttlClient(cf connFlags, fl *flag.FlagSet, name string) (*apiClient, error) {
	t, err := cf.resolve(fl)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}

	c, err := t.need()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}

	return c, nil
}

// durationArg sends a bare number as seconds, anything else as the duration
// text of the API.
func durationArg(s string) any {
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		return n
	}

	return s
}

func renderTTLTable(out io.Writer, titles, row []string) {
	cols := make([]tui.Column, len(titles))

	for i, title := range titles {
		cols[i] = tui.Column{Title: title, Keep: i == 0}
	}

	tb := tui.Table{Columns: cols, Rows: [][]string{row}}

	tb.Render(out, styleFor(out))
}
