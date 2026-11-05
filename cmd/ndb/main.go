// Command ndb is the daemon. It serves the HTTP API on a unix socket and,
// optionally, on TCP. It listens before recovery, so /health answers 503
// until the data dir is open.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/thadeu/ndb/internal/config"
	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/server"
)

const shutdownTimeout = 30 * time.Second

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "ndb:", err)
		os.Exit(1)
	}
}

// run reads the flags and ndb.yml, then serves until SIGTERM or SIGINT.
func run(args []string) error {
	if len(args) > 0 && args[0] == "backup" {
		return runBackup(args[1:])
	}

	fl := flag.NewFlagSet("ndb", flag.ContinueOnError)

	cfgPath := fl.String("config", "ndb.yml", "optional config file (absent = defaults)")
	dataDir := fl.String("data-dir", "", "data dir (overrides ndb.yml)")
	unix := fl.String("unix", "", "unix socket path (overrides ndb.yml)")
	tcp := fl.String("tcp", "", "TCP listen address, e.g. 127.0.0.1:7070 (overrides ndb.yml)")

	if err := fl.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}

	// A flag that is set overrides ndb.yml, even when empty: "-unix ''"
	// turns the unix listener off.
	set := map[string]bool{}

	fl.Visit(func(f *flag.Flag) { set[f.Name] = true })

	if set["data-dir"] {
		cfg.DataDir = *dataDir
	}

	if set["unix"] {
		cfg.Listen.Unix = *unix
	}

	if set["tcp"] {
		cfg.Listen.TCP = *tcp
	}

	// memory_limit is the Go heap soft limit (plan §11.4). Segment reads
	// still decode into the heap; mmap is not used yet.
	if cfg.MemoryLimit > 0 {
		debug.SetMemoryLimit(cfg.MemoryLimit)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)

	defer stop()

	return serve(ctx, cfg)
}

// serve listens, recovers the data dir, and stops in order when ctx ends:
// no new requests, in-flight requests finish, then the collections close.
func serve(ctx context.Context, cfg config.Config) error {
	log := slog.Default()

	listeners, err := listen(cfg.Listen)
	if err != nil {
		return err
	}

	srv := server.New()

	srv.SetBlobLimit(cfg.BlobMaxSize)

	errc := make(chan error, len(listeners))

	for _, l := range listeners {
		go func() { errc <- srv.Serve(l) }()
	}

	db, err := engine.Open(cfg, log)
	if err != nil {
		_ = srv.Shutdown(context.Background())

		return err
	}

	srv.Attach(db)

	log.Info("ndb ready", "data_dir", cfg.DataDir, "unix", cfg.Listen.Unix, "tcp", cfg.Listen.TCP)

	var stopErr error

	select {
	case <-ctx.Done():
	case stopErr = <-errc:
	}

	log.Info("ndb stopping")

	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)

	defer cancel()

	if err := srv.Shutdown(sctx); err != nil {
		log.Warn("shutdown timed out, requests still running", "err", err)
	}

	return errors.Join(stopErr, db.Close())
}

// listen opens the configured listeners. A stale socket file from a dead
// daemon is removed first.
func listen(l config.Listen) ([]net.Listener, error) {
	var out []net.Listener

	closeAll := func() {
		for _, x := range out {
			x.Close()
		}
	}

	if l.Unix != "" {
		if err := removeStaleSocket(l.Unix); err != nil {
			return nil, err
		}

		ul, err := net.Listen("unix", l.Unix)
		if err != nil {
			return nil, err
		}

		out = append(out, ul)
	}

	if l.TCP != "" {
		tl, err := net.Listen("tcp", l.TCP)
		if err != nil {
			closeAll()

			return nil, err
		}

		out = append(out, tl)
	}

	return out, nil
}

func removeStaleSocket(path string) error {
	fi, err := os.Stat(path)

	if os.IsNotExist(err) {
		return nil
	}

	if err != nil {
		return err
	}

	if fi.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%s exists and is not a socket", path)
	}

	return os.Remove(path)
}
