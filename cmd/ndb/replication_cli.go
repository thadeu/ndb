package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// replicationStatus is the answer of GET /_replication.
type replicationStatus struct {
	Databases []replicaDB `json:"databases"`
}

// replicaDB is the role of one database.
type replicaDB struct {
	Database   string        `json:"database"`
	Role       string        `json:"role"`
	Term       uint64        `json:"term"`
	Source     string        `json:"source"`
	State      string        `json:"state"`
	Synced     bool          `json:"synced"`
	LagRecords uint64        `json:"lag_records"`
	LagSeconds float64       `json:"lag_seconds"`
	LastError  string        `json:"last_error"`
	Copies     []replicaPeer `json:"copies"`
}

// replicaPeer is a copy connected to an owner.
type replicaPeer struct {
	Addr  string `json:"addr"`
	Since string `json:"since"`
}

// runReplication is "ndb replication status|promote|resync". Promote and
// resync go through the unix socket only.
func runReplication(args []string) error {
	return replicationCmd(args, os.Stdout)
}

func replicationCmd(args []string, out io.Writer) error {
	fl := flag.NewFlagSet("ndb replication", flag.ContinueOnError)

	cf := addConnFlags(fl)
	database := fl.String("database", "", "promote, resync: the database (default: the database of -url, else default)")

	pos, err := parseInterspersed(fl, args)
	if err != nil {
		return err
	}

	if len(pos) != 1 || (pos[0] != "status" && pos[0] != "promote" && pos[0] != "resync") {
		return errors.New("usage: ndb replication status | promote | resync [-database NAME] [-url unix:///sock] [-json]")
	}

	t, err := cf.resolve(fl)
	if err != nil {
		return fmt.Errorf("replication: %w", err)
	}

	c, err := t.need()
	if err != nil {
		return fmt.Errorf("replication: %w", err)
	}

	if *database != "" {
		c.database = *database
	}

	if pos[0] == "status" {
		var st replicationStatus

		if err := c.get("/_replication", &st); err != nil {
			return err
		}

		if *cf.asJSON {
			return writeJSON(out, st)
		}

		return printReplication(out, st)
	}

	var rep json.RawMessage

	if err := c.post("/_replication/"+pos[0], nil, &rep); err != nil {
		return err
	}

	if *cf.asJSON {
		return writeJSON(out, rep)
	}

	if pos[0] == "promote" {
		_, err = fmt.Fprintf(out, "promoted: %s. This database is the owner now. Remove it from NDB_REPLICA_OF before the next start.\n", rep)

		return err
	}

	_, err = fmt.Fprintf(out, "resync started: %s. See ndb replication status.\n", rep)

	return err
}

func printReplication(out io.Writer, st replicationStatus) error {
	var lines []string

	for _, d := range st.Databases {
		if d.Role == "copy" {
			line := fmt.Sprintf("%s: copy of %s, %s, lag %d records %.1fs, term %d", d.Database, d.Source, d.State, d.LagRecords, d.LagSeconds, d.Term)

			if !d.Synced {
				line += ", not synced yet (reads go to the owner)"
			}

			if d.LastError != "" {
				line += ", error: " + d.LastError
			}

			lines = append(lines, line)

			continue
		}

		line := fmt.Sprintf("%s: owner, term %d, %d copies", d.Database, d.Term, len(d.Copies))

		for _, p := range d.Copies {
			line += "\n  copy " + p.Addr + " since " + p.Since
		}

		lines = append(lines, line)
	}

	_, err := fmt.Fprintln(out, strings.Join(lines, "\n"))

	return err
}
