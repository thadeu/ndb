package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

// runHealth is "ndb health": exit 0 and print "ok" when the daemon is ready,
// else exit 1. It is the container health check, since the image has no
// shell and no curl.
func runHealth(args []string) error {
	return health(args, os.Stdout)
}

func health(args []string, out io.Writer) error {
	fl := flag.NewFlagSet("ndb health", flag.ContinueOnError)

	cf := addConnFlags(fl)

	pos, err := parseInterspersed(fl, args)
	if err != nil {
		return err
	}

	if len(pos) != 0 {
		return errors.New("usage: ndb health [-url u]")
	}

	c, err := ttlClient(cf, fl, "health")
	if err != nil {
		return err
	}

	if _, err := c.call("GET", "/_health", nil); err != nil {
		return fmt.Errorf("health: %w", err)
	}

	_, err = fmt.Fprintln(out, "ok")

	return err
}
