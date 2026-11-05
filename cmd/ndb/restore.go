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

// runRestore is "ndb restore <backup-dir>". The backup is copied to a stage
// dir inside the data dir and opened there, so recovery proves it is
// readable. Only then does it replace the data dir contents.
func runRestore(args []string) error {
	return restore(args, os.Stderr)
}

func restore(args []string, out io.Writer) error {
	fl := flag.NewFlagSet("ndb restore", flag.ContinueOnError)

	common := addDataDirFlags(fl)
	force := fl.Bool("force", false, "replace a data dir that is not empty")

	pos, err := parseInterspersed(fl, args)
	if err != nil {
		return err
	}

	if len(pos) != 1 {
		return errors.New("usage: ndb restore <backup-dir> [-config f] [-data-dir d] [-force]")
	}

	cfg, err := common.load(fl)
	if err != nil {
		return err
	}

	src, err := filepath.Abs(pos[0])
	if err != nil {
		return err
	}

	dataDir, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return err
	}

	if err := checkBackupDir(src, dataDir); err != nil {
		return err
	}

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}

	lk, err := lock.Acquire(dataDir)
	if errors.Is(err, lock.ErrLocked) {
		return fmt.Errorf("restore: %s is used by a running daemon; stop it first", dataDir)
	}

	if err != nil {
		return err
	}

	defer lk.Release()

	old, err := dataEntries(dataDir)
	if err != nil {
		return err
	}

	if len(old) > 0 && !*force {
		return fmt.Errorf("restore: %s is not empty; use -force to replace it", dataDir)
	}

	stage := filepath.Join(dataDir, stageDir)

	if err := os.RemoveAll(stage); err != nil {
		return err
	}

	defer os.RemoveAll(stage)

	if err := stageBackup(src, stage); err != nil {
		return fmt.Errorf("restore: copy: %w", err)
	}

	summary, err := verifyStage(stage)
	if err != nil {
		return fmt.Errorf("restore: the backup does not recover, %s is untouched: %w", dataDir, err)
	}

	if err := swapIn(dataDir, stage, old); err != nil {
		return fmt.Errorf("restore: %w", err)
	}

	fmt.Fprintf(out, "restore: %s restored to %s (%s)\n", src, dataDir, summary)

	return nil
}

// checkBackupDir refuses a source that is not a data dir copy, or that
// overlaps the target.
func checkBackupDir(src, dataDir string) error {
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("restore: %w", err)
	}

	if !info.IsDir() {
		return fmt.Errorf("restore: %s is not a directory", src)
	}

	if src == dataDir || strings.HasPrefix(src, dataDir+string(filepath.Separator)) {
		return fmt.Errorf("restore: the backup %s is inside the target %s", src, dataDir)
	}

	for _, name := range []string{"catalog.bolt", "collections"} {
		if _, err := os.Stat(filepath.Join(src, name)); err == nil {
			return nil
		}
	}

	return fmt.Errorf("restore: %s does not look like an ndb backup (no catalog.bolt or collections/)", src)
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

func stageBackup(src, stage string) error {
	return backup.CopyTree(src, stage, func(rel string) bool {
		return rel == "LOCK" || strings.HasSuffix(rel, ".tmp")
	})
}

// verifyStage opens the staged copy as a daemon would: recovery replays the
// WAL and rebuilds segments and indexes. A corrupt backup fails here.
func verifyStage(stage string) (string, error) {
	cfg := config.Config{DataDir: stage, AllowImplicit: true}

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
