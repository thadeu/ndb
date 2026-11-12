package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

type keyState struct {
	Encryption struct {
		Key      string `json:"key"`
		KeyID    string `json:"key_id"`
		Source   string `json:"source"`
		Rotation *struct {
			From    string   `json:"from"`
			Pending []string `json:"pending"`
		} `json:"rotation"`
	} `json:"encryption"`
}

type keyRotated struct {
	Key   string `json:"key,omitempty"`
	KeyID string `json:"key_id"`
	From  string `json:"from"`
}

// runKey is "ndb key status|rotate": the encryption key of the daemon. A
// rotation goes through the unix socket only, so only a user with a shell
// on the host rotates the key.
func runKey(args []string) error {
	return key(args, os.Stdout)
}

func key(args []string, out io.Writer) error {
	fl := flag.NewFlagSet("ndb key", flag.ContinueOnError)

	cf := addConnFlags(fl)
	env := fl.Bool("env", false, "rotate: make the new key for NDB_ENCRYPTION_KEY, shown once")

	pos, err := parseInterspersed(fl, args)
	if err != nil {
		return err
	}

	if len(pos) != 1 || pos[0] != "status" && pos[0] != "rotate" {
		return errors.New("usage: ndb key status | ndb key rotate [-env] [-url unix:///sock] [-json]")
	}

	t, err := cf.resolve(fl)
	if err != nil {
		return fmt.Errorf("key: %w", err)
	}

	c, err := t.need()
	if err != nil {
		return fmt.Errorf("key: %w", err)
	}

	if pos[0] == "status" {
		var st keyState

		if err := c.get("/_cluster/settings", &st); err != nil {
			return err
		}

		if *cf.asJSON {
			return writeJSON(out, st.Encryption)
		}

		return printKeyState(out, st)
	}

	body := []byte(`{}`)

	if *env {
		body = []byte(`{"env":true}`)
	}

	var rep keyRotated

	if err := c.post("/_cluster/key", body, &rep); err != nil {
		return err
	}

	if *cf.asJSON {
		return writeJSON(out, rep)
	}

	if !*env {
		_, err := fmt.Fprintf(out, "rotation started: %s -> %s. Every encrypted value is sealed again in the background; see ndb key status.\n", rep.From, rep.KeyID)

		return err
	}

	_, err = fmt.Fprintf(out, `The new key %s, shown only this time. Keep a copy in your secret store first:
without it, the encrypted fields cannot be read.

  NDB_ENCRYPTION_KEY=%s

Then restart the daemon with it, and with the current key in
NDB_ENCRYPTION_KEY_OLD (for a key kept in the data dir, NDB_ENCRYPTION_KEY_OLD
is not needed). When ndb key status shows no rotation, remove
NDB_ENCRYPTION_KEY_OLD.
`, rep.KeyID, rep.Key)

	return err
}

func printKeyState(out io.Writer, st keyState) error {
	e := st.Encryption

	lines := []string{
		fmt.Sprintf("key:    %s (%s)", e.KeyID, e.Key),
		"source: " + e.Source,
	}

	if r := e.Rotation; r != nil {
		lines = append(lines, fmt.Sprintf("rotation: %s -> %s, %d collections left: %s", r.From, e.KeyID, len(r.Pending), strings.Join(r.Pending, ", ")))
	} else {
		lines = append(lines, "rotation: none")
	}

	_, err := fmt.Fprintln(out, strings.Join(lines, "\n"))

	return err
}
