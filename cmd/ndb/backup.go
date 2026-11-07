package main

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/thadeu/ndb/internal/backup"
	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/lock"
)

// runBackup is "ndb backup <dest|-> [flags]". It writes one .ndb file (or
// .ndb.gz with -gzip). With the daemon stopped it reads the data dir; with the
// daemon running it streams a consistent online backup from the unix socket
// (see engine.DB.Backup).
func runBackup(args []string) error {
	return backupTo(args, os.Stdout, os.Stderr)
}

func backupTo(args []string, stdout, stderr io.Writer) error {
	fl := flag.NewFlagSet("ndb backup", flag.ContinueOnError)

	conn := addConnFlags(fl)
	gz := fl.Bool("gzip", false, "compress the file with gzip (.ndb.gz)")
	force := fl.Bool("force", false, "replace the file if it exists")
	noBlobs := fl.Bool("no-blobs", false, "leave the blob content out (documents keep their references)")

	pos, err := parseInterspersed(fl, args)
	if err != nil {
		return err
	}

	if len(pos) != 1 {
		return errors.New("usage: ndb backup <dest|-> [-gzip] [-force] [-no-blobs] [-config f] [-data-dir d] [-url u]")
	}

	t, err := conn.resolve(fl)
	if err != nil {
		return err
	}

	produce := func(w io.Writer) error {
		return produceBackup(t, w, *noBlobs)
	}

	if pos[0] == "-" {
		return writeStream(stdout, *gz, produce)
	}

	path := backupPath(pos[0], *gz)

	if err := writeFile(path, *gz, *force, produce); err != nil {
		return err
	}

	fmt.Fprintf(stderr, "backup: %s written to %s\n", t.mode(), path)

	return nil
}

func (t target) mode() string {
	if t.client != nil {
		return "online"
	}

	return "offline"
}

func produceBackup(t target, w io.Writer, noBlobs bool) error {
	if t.client != nil {
		return backupOnline(t.client, w, noBlobs)
	}

	err := engine.Offline(t.cfg.DataDir, w, backup.Options{Version: version, NoBlobs: noBlobs})

	if errors.Is(err, lock.ErrLocked) {
		_, need := t.need()

		return fmt.Errorf("the data dir is used by a running daemon; ask it with -url: %w", need)
	}

	return err
}

func backupOnline(c *apiClient, w io.Writer, noBlobs bool) error {
	body, err := json.Marshal(map[string]bool{"no_blobs": noBlobs})
	if err != nil {
		return err
	}

	rc, err := c.stream("POST", "/_backup", body)
	if err != nil {
		return err
	}

	defer rc.Close()

	_, err = io.Copy(w, rc)

	return err
}

// backupPath adds .ndb or .ndb.gz unless dest already ends with it.
func backupPath(dest string, gz bool) string {
	ext := ".ndb"

	if gz {
		ext = ".ndb.gz"
	}

	if strings.HasSuffix(dest, ".ndb") || strings.HasSuffix(dest, ".ndb.gz") {
		return dest
	}

	return dest + ext
}

func writeStream(out io.Writer, gz bool, produce func(io.Writer) error) error {
	if !gz {
		return produce(out)
	}

	zw := gzip.NewWriter(out)

	if err := produce(zw); err != nil {
		return err
	}

	return zw.Close()
}

// writeFile writes to <path>.part and renames it, so a failed backup never
// leaves a file that looks complete.
func writeFile(path string, gz, force bool, produce func(io.Writer) error) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}

	if _, err := os.Stat(abs); err == nil && !force {
		return fmt.Errorf("backup: %s exists; use -force to replace it", abs)
	}

	part := abs + ".part"

	f, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}

	err = writeStream(f, gz, produce)

	if err == nil {
		err = f.Sync()
	}

	if cerr := f.Close(); err == nil {
		err = cerr
	}

	if err != nil {
		os.Remove(part)

		return err
	}

	return os.Rename(part, abs)
}
