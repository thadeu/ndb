// Command ndb is the daemon. It serves the HTTP API on a unix socket and,
// optionally, on TCP. It listens before recovery, so /health answers 503
// until the data dir is open.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/thadeu/ndb/internal/config"
	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/logging"
	"github.com/thadeu/ndb/internal/segment"
	"github.com/thadeu/ndb/internal/server"
)

const shutdownTimeout = 30 * time.Second

// version is set at release build time: -ldflags "-X main.version=1.2.3".
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "ndb:", err)
		os.Exit(1)
	}
}

// run reads the flags and ndb.yml, then serves until SIGTERM or SIGINT.
func run(args []string) error {
	// "ndb" alone in a terminal is a person who wants the help. Without a
	// terminal (systemd, Docker, an old unit with no "serve") it still runs
	// the daemon.
	if len(args) == 0 && term.IsTerminal(int(os.Stdout.Fd())) {
		return runHelp(nil)
	}

	if len(args) > 0 {
		if cmd, ok := subcommands[args[0]]; ok {
			return cmd(args[1:])
		}

		if args[0] == "serve" || args[0] == "s" {
			args = args[1:]
		}
	}

	fl := flag.NewFlagSet("ndb", flag.ContinueOnError)

	fl.Usage = func() { fmt.Fprint(fl.Output(), helpText) }

	cfgPath := fl.String("config", "", "config file (default: NDB_CONFIG, then ./ndb.yml, then /etc/ndb/ndb.yml; absent = defaults)")
	dataDir := fl.String("data-dir", "", "data dir (overrides ndb.yml)")
	unix := fl.String("unix", "", "unix socket path (overrides ndb.yml)")
	tcp := fl.String("tcp", "", "TCP listen address, e.g. 127.0.0.1:7070 (overrides ndb.yml)")
	unixMode := fl.String("unix-mode", "", "permission of the unix socket, octal (default 0660, or listen.unix_mode)")
	showVersion := fl.Bool("version", false, "print the version and exit")

	tokenFile := fl.String("token-file", "", "file with the bearer token the TCP listener requires (default NDB_TOKEN_FILE, then NDB_TOKEN; unix socket stays open)")

	logLevel := fl.String("log-level", "", "debug, info, warn, error or quiet (default NDB_LOG_LEVEL, then log_level, then info)")
	logFormat := fl.String("log-format", "", "text or json (default NDB_LOG_FORMAT, then log_format, then text)")

	var corsOrigins, allowedHosts listFlag

	fl.Var(&corsOrigins, "cors-origin", "browser origin that may call the daemon, such as https://app.example.com (repeat it; overrides cors_origins)")
	fl.Var(&allowedHosts, "allowed-host", "Host name a TCP request with no token may use (repeat it; overrides allowed_hosts)")

	maxConns := fl.Int("max-conns", 0, "most open TCP connections (0 = no limit; overrides max_conns)")
	maxWatches := fl.Int("max-watches", 0, "most open _watch streams (0 = no limit; overrides max_watches)")

	if err := fl.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}

		return err
	}

	if *showVersion {
		fmt.Println("ndb", version)

		return nil
	}

	cfg, err := config.Load(config.FindPath(*cfgPath, config.EtcPath))
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

	if set["unix-mode"] {
		if cfg.Listen.UnixMode, err = parseUnixMode(*unixMode); err != nil {
			return err
		}
	}

	if set["cors-origin"] {
		if cfg.CORSOrigins, err = checkList(corsOrigins, "-cors-origin", config.CheckOrigin); err != nil {
			return err
		}
	}

	if set["allowed-host"] {
		if cfg.AllowedHosts, err = checkList(allowedHosts, "-allowed-host", config.CheckHost); err != nil {
			return err
		}
	}

	if set["max-conns"] {
		cfg.MaxConns = *maxConns
	}

	if set["max-watches"] {
		cfg.MaxWatches = *maxWatches
	}

	if cfg.MaxConns < 0 || cfg.MaxWatches < 0 {
		return errors.New("-max-conns and -max-watches must be zero or more")
	}

	cfg.EncryptionKey = os.Getenv("NDB_ENCRYPTION_KEY")
	cfg.EncryptionKeyOld = os.Getenv("NDB_ENCRYPTION_KEY_OLD")

	logSrc := logSources(cfg, os.Getenv, set["log-level"])

	logOpts, err := logOptions(cfg, os.Getenv, logFlags{
		level:     *logLevel,
		format:    *logFormat,
		levelSet:  set["log-level"],
		formatSet: set["log-format"],
	})

	if err != nil {
		return err
	}

	// memory_limit is the Go heap soft limit (plan §11.4). Without it (and
	// without GOMEMLIMIT) the default keeps the daemon near 512 MB: the GC
	// works harder instead of letting the heap double.
	switch {
	case cfg.MemoryLimit > 0:
		debug.SetMemoryLimit(cfg.MemoryLimit)
	case os.Getenv("GOMEMLIMIT") == "":
		debug.SetMemoryLimit(config.DefaultMemoryLimit)
	}

	if cfg.ReadCache > 0 {
		segment.GroupCacheBytes = cfg.ReadCache
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)

	defer stop()

	token, err := serveToken(*tokenFile, os.LookupEnv)

	if err != nil {
		return err
	}

	// The log lines go out from one goroutine, in blocks: a debug log does
	// not slow the requests. Close writes what waits.
	out := logging.NewAsync(os.Stderr, 0)

	defer out.Close()

	return serve(ctx, cfg, token, setupLog(out, logOpts), logSrc)
}

// listFlag is a flag that can be given more than once.
type listFlag []string

func (f *listFlag) String() string {
	return strings.Join(*f, ",")
}

func (f *listFlag) Set(v string) error {
	*f = append(*f, v)

	return nil
}

// checkList checks each value of a list flag.
func checkList(values []string, name string, check func(string) error) ([]string, error) {
	for _, v := range values {
		if err := check(v); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
	}

	return values, nil
}

// security is what the server takes from the browser guards and the
// limits of ndb.yml.
func security(cfg config.Config) server.Security {
	return server.Security{
		CORSOrigins:  cfg.CORSOrigins,
		AllowedHosts: cfg.AllowedHosts,
		TrustProxy:   cfg.TrustProxy,
		AuthFailures: cfg.AuthFailures,
		AuthWindow:   cfg.AuthWindow,
		AuthLockout:  cfg.AuthLockout,
		MaxWatches:   cfg.MaxWatches,
	}
}

// serveToken is the bearer token of the TCP listener: the file of
// -token-file, else the file of NDB_TOKEN_FILE, else NDB_TOKEN (for a
// secret store that gives env vars, in an image with no shell). A set but
// empty value is an error: it would turn the protection off without a word.
func serveToken(flagFile string, lookup func(string) (string, bool)) (string, error) {
	if flagFile != "" {
		return readTokenFile(flagFile)
	}

	if file, ok := lookup("NDB_TOKEN_FILE"); ok {
		if strings.TrimSpace(file) == "" {
			return "", fmt.Errorf("NDB_TOKEN_FILE is empty")
		}

		return readTokenFile(file)
	}

	value, ok := lookup("NDB_TOKEN")

	if !ok {
		return "", nil
	}

	token := strings.TrimSpace(value)

	if token == "" {
		return "", fmt.Errorf("NDB_TOKEN is empty")
	}

	return token, nil
}

// readTokenFile returns the trimmed token. An empty file is an error: it
// would turn the protection off without a word.
func readTokenFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("token file: %w", err)
	}

	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("token file %s is empty", path)
	}

	return token, nil
}

// scopedTokens reads the secret of each token of ndb.yml.
func scopedTokens(tokens []config.Token) ([]server.Token, error) {
	out := make([]server.Token, 0, len(tokens))

	for _, t := range tokens {
		secret, err := readTokenFile(t.File)
		if err != nil {
			return nil, fmt.Errorf("token %s: %w", t.Name, err)
		}

		out = append(out, server.Token{Name: t.Name, Secret: secret, Read: t.Read, Write: t.Write, Admin: t.Admin, Decrypt: t.Decrypt})
	}

	return out, nil
}

// serve listens, recovers the data dir, and stops in order when ctx ends:
// no new requests, in-flight requests finish, then the collections close.
func serve(ctx context.Context, cfg config.Config, token string, lg *logging.Logger, logSrc map[string]string) error {
	log := lg.Logger

	log.Info("ndb starting",
		"version", version,
		"pid", os.Getpid(),
		"data_dir", cfg.DataDir,
		"log_level", logging.LevelName(lg.Level()),
		"log_format", lg.Format(),
		"log_body_max", lg.BodyMax(),
		"slow_ms", lg.SlowMs(),
	)

	scoped, err := scopedTokens(cfg.Tokens)
	if err != nil {
		return err
	}

	listeners, err := listen(cfg.Listen, cfg.MaxConns)

	if err != nil {
		return err
	}

	srv := server.New()

	srv.SetVersion(version)
	srv.SetBlobLimit(cfg.BlobMaxSize)
	srv.SetToken(token)
	srv.SetTokens(scoped)
	srv.SetLogger(lg)
	srv.SetLogSources(logSrc)
	srv.SetSecurity(security(cfg))

	errc := make(chan error, len(listeners))

	for _, l := range listeners {
		go func() { errc <- srv.Serve(l) }()
	}

	log.Info("listening", "unix", cfg.Listen.Unix, "tcp", cfg.Listen.TCP, "token_file", token != "", "scoped_tokens", len(scoped),
		"cors_origins", len(cfg.CORSOrigins), "allowed_hosts", len(cfg.AllowedHosts), "max_conns", cfg.MaxConns, "max_watches", cfg.MaxWatches)

	began := time.Now()

	db, err := engine.Open(cfg, log)

	if err != nil {
		log.Error("recovery failed", "data_dir", cfg.DataDir, "err", err)

		_ = srv.Shutdown(context.Background())

		return err
	}

	srv.Attach(db)
	srv.ApplyStoredLog(db)

	log.Info("ndb ready",
		"version", version,
		"data_dir", cfg.DataDir,
		"collections", len(db.Definitions()),
		"recovery_ms", time.Since(began).Milliseconds(),
		"unix", cfg.Listen.Unix,
		"tcp", cfg.Listen.TCP,
	)

	var stopErr error

	select {
	case <-ctx.Done():
	case stopErr = <-errc:
	}

	if stopErr != nil {
		log.Error("listener failed", "err", stopErr)
	}

	log.Info("ndb stopping")

	// The seals and compactions stop first: a request that runs one (a
	// _compact) returns, so the wait for the requests in flight is short.
	db.Stop()

	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)

	defer cancel()

	if err := srv.Shutdown(sctx); err != nil {
		log.Warn("shutdown timed out, requests still running", "err", err)
	}

	err = errors.Join(stopErr, db.Close())

	if err != nil {
		log.Error("ndb stopped with an error", "err", err)

		return err
	}

	log.Info("ndb stopped")

	return nil
}

// listen opens the configured listeners. A stale socket file from a dead
// daemon is removed first. maxConns limits the open TCP connections (0 =
// no limit); the unix socket has no limit.
func listen(l config.Listen, maxConns int) ([]net.Listener, error) {
	var out []net.Listener

	closeAll := func() {
		for _, x := range out {
			x.Close()
		}
	}

	if l.Unix != "" {
		if err := checkSocketPath(l.Unix); err != nil {
			return nil, err
		}

		// The dir of the socket may not exist yet: ~/.ndb on a first run.
		if err := os.MkdirAll(filepath.Dir(l.Unix), 0o750); err != nil {
			return nil, err
		}

		if err := removeStaleSocket(l.Unix); err != nil {
			return nil, err
		}

		ul, err := net.Listen("unix", l.Unix)
		if err != nil {
			return nil, err
		}

		if l.UnixMode != 0 {
			if err := os.Chmod(l.Unix, l.UnixMode); err != nil {
				ul.Close()

				return nil, err
			}
		}

		out = append(out, ul)
	}

	if l.TCP != "" {
		tl, err := net.Listen("tcp", l.TCP)
		if err != nil {
			closeAll()

			return nil, err
		}

		out = append(out, server.LimitListener(tl, maxConns))
	}

	return out, nil
}

// parseUnixMode reads an octal permission such as "0660".
func parseUnixMode(s string) (os.FileMode, error) {
	n, err := strconv.ParseUint(s, 8, 32)
	if err != nil || n > 0o777 {
		return 0, fmt.Errorf("-unix-mode %q: want an octal permission such as 0660", s)
	}

	return os.FileMode(n), nil
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

	// A socket that answers belongs to a running ndb: removing it would
	// leave that daemon unreachable, and this one stops anyway on the lock
	// of its data dir.
	if conn, err := net.DialTimeout("unix", path, time.Second); err == nil {
		conn.Close()

		return fmt.Errorf("%s: another ndb is listening on it; stop it first or use another socket", path)
	}

	return os.Remove(path)
}
