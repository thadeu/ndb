package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/thadeu/ndb/internal/backup"
	"github.com/thadeu/ndb/internal/config"
	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/lock"
)

const stageDir = ".restore-stage"

// runRestore is "ndb restore <file|-> [flags]". The backup file is read in
// one pass into a stage dir inside the data dir, with its header, every WAL
// line CRC and its end line checked. The stage is then opened like a daemon
// would, to prove it recovers. Only then does it replace the data dir
// contents; any earlier failure leaves the data dir as it was.
//
// With -at it is a PITR restore instead, by a running daemon (pitr.go).
func runRestore(args []string) error {
	if isPITRRestore(args) {
		return pitrRestore(args, os.Stdout)
	}

	return restore(args, os.Stdin, os.Stderr)
}

func restore(args []string, stdin io.Reader, out io.Writer) error {
	fl := flag.NewFlagSet("ndb restore", flag.ContinueOnError)

	common := addDataDirFlags(fl)
	force := fl.Bool("force", false, "replace a data dir that is not empty")

	pos, err := parseInterspersed(fl, args)
	if err != nil {
		return err
	}

	if len(pos) != 1 {
		return errors.New("usage: ndb restore <file|-> [-config f] [-data-dir d] [-force]")
	}

	cfg, err := common.load(fl)
	if err != nil {
		return err
	}

	dataDir, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return err
	}

	in, name, closeIn, err := openBackup(pos[0], dataDir, stdin)
	if err != nil {
		return err
	}

	defer closeIn()

	summary, err := restoreInto(dataDir, in, *force)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "restore: %s restored to %s (%s)\n", name, dataDir, summary)

	return nil
}

// openBackup opens the file, or stdin for "-", and refuses a file that lies
// inside the target.
func openBackup(arg, dataDir string, stdin io.Reader) (io.Reader, string, func(), error) {
	if arg == "-" {
		return stdin, "stdin", func() {}, nil
	}

	src, err := filepath.Abs(arg)
	if err != nil {
		return nil, "", nil, err
	}

	if strings.HasPrefix(src, dataDir+string(filepath.Separator)) {
		return nil, "", nil, fmt.Errorf("restore: the backup %s is inside the target %s", src, dataDir)
	}

	f, err := os.Open(src)
	if err != nil {
		return nil, "", nil, fmt.Errorf("restore: %w", err)
	}

	if info, err := f.Stat(); err != nil || info.IsDir() {
		f.Close()

		return nil, "", nil, fmt.Errorf("restore: %s is not a backup file", src)
	}

	return f, src, func() { f.Close() }, nil
}

func restoreInto(dataDir string, in io.Reader, force bool) (string, error) {
	_, statErr := os.Stat(dataDir)
	created := os.IsNotExist(statErr)

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return "", err
	}

	summary, err := restoreLocked(dataDir, in, force)

	if err != nil && created {
		os.RemoveAll(dataDir)
	}

	return summary, err
}

func restoreLocked(dataDir string, in io.Reader, force bool) (string, error) {
	lk, err := lock.Acquire(dataDir)
	if errors.Is(err, lock.ErrLocked) {
		return "", fmt.Errorf("restore: %s is used by a running daemon; stop it first", dataDir)
	}

	if err != nil {
		return "", err
	}

	defer lk.Release()

	old, err := dataEntries(dataDir)
	if err != nil {
		return "", err
	}

	if len(old) > 0 && !force {
		return "", fmt.Errorf("restore: %s is not empty; use -force to replace it", dataDir)
	}

	stage := filepath.Join(dataDir, stageDir)

	if err := os.RemoveAll(stage); err != nil {
		return "", err
	}

	defer os.RemoveAll(stage)

	if err := backup.Extract(in, stage); err != nil {
		return "", fmt.Errorf("restore: %w; %s is untouched", err, dataDir)
	}

	summary, err := verifyStage(stage)
	if err != nil {
		return "", fmt.Errorf("restore: the backup does not recover, %s is untouched: %w", dataDir, err)
	}

	if err := swapIn(dataDir, stage, old); err != nil {
		return "", fmt.Errorf("restore: %w", err)
	}

	return summary, nil
}

// dataEntries lists what is in the data dir, ignoring the LOCK file we hold.
func dataEntries(dataDir string) ([]string, error) {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return nil, err
	}

	var names []string

	for _, e := range entries {
		if e.Name() != "LOCK" && e.Name() != stageDir {
			names = append(names, e.Name())
		}
	}

	return names, nil
}

// verifyStage opens the staged copy as a daemon would: recovery replays the
// WAL and rebuilds segments and indexes. A corrupt backup fails here.
func verifyStage(stage string) (string, error) {
	cfg := config.Config{DataDir: stage, AllowImplicit: true, EncryptionKey: os.Getenv("NDB_ENCRYPTION_KEY")}

	db, err := engine.Open(cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		return "", err
	}

	rows := 0
	stats := db.AllStats()

	for _, st := range stats {
		rows += st.Rows
	}

	if err := db.Close(); err != nil {
		return "", err
	}

	return fmt.Sprintf("%d collections, %d rows", len(stats), rows), nil
}

// swapIn removes the old contents, then moves the staged entries in place.
func swapIn(dataDir, stage string, old []string) error {
	for _, name := range old {
		if err := os.RemoveAll(filepath.Join(dataDir, name)); err != nil {
			return err
		}
	}

	entries, err := os.ReadDir(stage)
	if err != nil {
		return err
	}

	for _, e := range entries {
		if e.Name() == "LOCK" {
			continue
		}

		if err := os.Rename(filepath.Join(stage, e.Name()), filepath.Join(dataDir, e.Name())); err != nil {
			return err
		}
	}

	return nil
}
