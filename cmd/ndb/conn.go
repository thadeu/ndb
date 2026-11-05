package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/thadeu/ndb/internal/config"
	"github.com/thadeu/ndb/internal/tui"
)

// connFlags are the flags of every command that can ask a daemon: where the
// config is, which daemon to ask, and the output format.
type connFlags struct {
	dataDirFlags

	rawURL *string
	asJSON *bool
}

func addConnFlags(fl *flag.FlagSet) connFlags {
	return connFlags{
		dataDirFlags: addDataDirFlags(fl),
		rawURL:       fl.String("url", "", "running daemon: unix:///path/to.sock or tcp://host:port (default NDB_URL, then the socket of ndb.yml)"),
		asJSON:       fl.Bool("json", false, "machine output"),
	}
}

// target is the daemon a command talks to, or none (offline).
type target struct {
	cfg    config.Config
	client *apiClient

	// source names where the answer comes from: the URL, or "data-dir".
	source string
}

// resolve picks the daemon. Order: -url, then -data-dir (offline), then
// NDB_URL, then the unix socket of ndb.yml when something answers on it.
// Without any of them the target is offline.
func (c connFlags) resolve(fl *flag.FlagSet) (target, error) {
	if *c.rawURL != "" && flagSet(fl, "data-dir") {
		return target{}, errors.New("use -url or -data-dir, not both")
	}

	cfg, err := c.load(fl)
	if err != nil {
		return target{}, err
	}

	t := target{cfg: cfg, source: "data-dir"}

	rawURL := *c.rawURL

	if rawURL == "" && !flagSet(fl, "data-dir") {
		rawURL = os.Getenv("NDB_URL")
	}

	if rawURL != "" {
		t.client, err = newAPIClient(rawURL)
		t.source = rawURL

		return t, err
	}

	if flagSet(fl, "data-dir") || cfg.Listen.Unix == "" {
		return t, nil
	}

	sock := newUnixClient(cfg.Listen.Unix, os.Getenv("NDB_TOKEN"))

	if sock.alive() {
		t.client, t.source = sock, "unix://"+cfg.Listen.Unix
	}

	return t, nil
}

// need is the client, or an error when no daemon answers.
func (t target) need() (*apiClient, error) {
	if t.client != nil {
		return t.client, nil
	}

	where := "set -url or NDB_URL"

	if t.cfg.Listen.Unix != "" {
		where = fmt.Sprintf("nothing answers on %s; start it with \"ndb serve\" or set -url / NDB_URL", t.cfg.Listen.Unix)
	}

	return nil, fmt.Errorf("%w: %s", errNoDaemon, where)
}

// styleFor is the table style of out: detected on a terminal, plain otherwise.
func styleFor(out io.Writer) tui.Style {
	if f, ok := out.(*os.File); ok {
		return tui.StyleFor(f)
	}

	return tui.NewStyle(os.Getenv, false, 0)
}
