package main

import (
	"errors"
	"flag"
	"io"
	"net/url"
	"os"
)

// runGet is "ndb get <collection> <_id>": the document with its "_v".
func runGet(args []string) error {
	return get(args, os.Stdout)
}

func get(args []string, out io.Writer) error {
	fl := flag.NewFlagSet("ndb get", flag.ContinueOnError)

	cf := addConnFlags(fl)

	pos, err := parseInterspersed(fl, args)
	if err != nil {
		return err
	}

	if len(pos) != 2 {
		return errors.New("usage: ndb get <collection> <_id> [-url u]")
	}

	c, err := ttlClient(cf, fl, "get")
	if err != nil {
		return err
	}

	r, err := c.call("GET", "/v1/c/"+url.PathEscape(pos[0])+"/"+url.PathEscape(pos[1]), nil)
	if err != nil {
		return err
	}

	_, err = out.Write(r.Body)

	return err
}
