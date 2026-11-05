package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/thadeu/ndb/internal/backup"
	"github.com/thadeu/ndb/internal/config"
	"github.com/thadeu/ndb/internal/lock"
)

// runBackup is "ndb backup [flags] <dest>". With the daemon stopped it copies
// the whole data dir. With the daemon running it asks the daemon, on its unix
// socket, for a consistent online backup (see engine.DB.Backup).
func runBackup(args []string) error {
	fl := flag.NewFlagSet("ndb backup", flag.ContinueOnError)

	cfgPath := fl.String("config", "ndb.yml", "optional config file (absent = defaults)")
	dataDir := fl.String("data-dir", "", "data dir (overrides ndb.yml)")
	unix := fl.String("unix", "", "unix socket of the running daemon (overrides ndb.yml)")

	if err := fl.Parse(args); err != nil {
		return err
	}

	if fl.NArg() != 1 {
		return errors.New("usage: ndb backup [-config f] [-data-dir d] [-unix sock] <dest>")
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}

	set := map[string]bool{}

	fl.Visit(func(f *flag.Flag) { set[f.Name] = true })

	if set["data-dir"] {
		cfg.DataDir = *dataDir
	}

	if set["unix"] {
		cfg.Listen.Unix = *unix
	}

	dest, err := filepath.Abs(fl.Arg(0))
	if err != nil {
		return err
	}

	err = backup.Offline(cfg.DataDir, dest)

	if err == nil {
		fmt.Fprintf(os.Stderr, "backup: daemon is stopped, copied the whole data dir to %s\n", dest)

		return nil
	}

	if !errors.Is(err, lock.ErrLocked) {
		return err
	}

	return backupOnline(cfg.Listen.Unix, dest)
}

func backupOnline(sock, dest string) error {
	if sock == "" {
		return errors.New("the daemon is running and has no unix socket; online backup needs it")
	}

	body, err := json.Marshal(map[string]string{"dest": dest})
	if err != nil {
		return err
	}

	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer

			return d.DialContext(ctx, "unix", sock)
		},
	}}

	resp, err := client.Post("http://ndb/v1/backup", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}

	defer resp.Body.Close()

	out, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("daemon refused the backup (%d): %s", resp.StatusCode, bytes.TrimSpace(out))
	}

	fmt.Fprintf(os.Stderr, "backup: online, WAL prefix copied to %s: %s\n", dest, bytes.TrimSpace(out))

	return nil
}
