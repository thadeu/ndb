// Package config loads the optional ndb.yml.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/logging"
)

var ErrInvalid = errors.New("config: invalid ndb.yml")

type Listen struct {
	Unix string `yaml:"unix"`
	TCP  string `yaml:"tcp"`

	// UnixModeText is listen.unix_mode, an octal text such as "0660".
	UnixModeText string `yaml:"unix_mode"`

	// UnixMode is the permission of the unix socket file: 0660 by default
	// (owner and group), so app containers in the group can connect.
	// listen.unix_mode or the -unix-mode flag change it.
	UnixMode os.FileMode `yaml:"-"`
}

// EtcPath is where the daemon and the CLI look for ndb.yml when there is
// no -config flag, no NDB_CONFIG and no ndb.yml in the current directory.
const EtcPath = "/etc/ndb/ndb.yml"

// FindPath picks the config file: the -config flag, then NDB_CONFIG, then
// ./ndb.yml when it exists, then etc. A missing file still gives the defaults.
func FindPath(flagValue, etc string) string {
	if flagValue != "" {
		return flagValue
	}

	if env := os.Getenv("NDB_CONFIG"); env != "" {
		return env
	}

	if _, err := os.Stat("ndb.yml"); err == nil {
		return "ndb.yml"
	}

	return etc
}

// ParseUnixMode reads an octal permission such as "0660".
func ParseUnixMode(s string) (os.FileMode, error) {
	n, err := strconv.ParseUint(s, 8, 32)
	if err != nil || n > 0o777 {
		return 0, fmt.Errorf("unix mode %q: want an octal permission such as 0660", s)
	}

	return os.FileMode(n), nil
}

// DefaultPITR is the PITR window when ndb.yml does not set pitr.
const DefaultPITR = "1d"

// DefaultIdleClose is idle_close when ndb.yml does not set it.
const DefaultIdleClose = "10m"

// DefaultMemoryLimit is the Go heap soft limit when memory_limit is not set.
const DefaultMemoryLimit = 384 << 20

type Config struct {
	DataDir       string `yaml:"data_dir"`
	Listen        Listen `yaml:"listen"`
	MemoryLimit   int64  `yaml:"-"`
	WriteBuffer   int64  `yaml:"-"`
	GroupLimit    int64  `yaml:"-"`
	BlobMaxSize   int64  `yaml:"-"`
	ReadCache     int64  `yaml:"-"`
	AllowImplicit bool   `yaml:"allow_implicit"`

	// PITR is the point in time recovery window of every engine collection
	// that does not set its own: "off", or a duration up to 7d (default 1d).
	PITR string `yaml:"pitr"`

	// IdleClose is how long an engine collection goes unused before it is
	// closed (its files, memory and WAL reserve given back); the next call
	// opens it again. "off" keeps every collection open (default 10m).
	IdleClose string `yaml:"idle_close"`

	Collections map[string]catalog.Def `yaml:"-"`

	// Tokens are the bearer tokens of the TCP listener, each with what it
	// may do. The secret of each is in a file.
	Tokens []Token `yaml:"tokens"`

	// LogLevel is debug, info, warn, error or quiet (default info).
	// LogFormat is text or json (default text). NDB_LOG_LEVEL,
	// NDB_LOG_FORMAT and the flags of ndb serve override them.
	LogLevel  string `yaml:"log_level"`
	LogFormat string `yaml:"log_format"`

	// LogBodyMax is the largest body or document in a log line, in bytes
	// (default 2048; 0 = no bodies). LogSlowMs is the duration of a slow
	// request, a warn line (default 1000; 0 = off).
	LogBodyMax int `yaml:"log_body_max"`
	LogSlowMs  int `yaml:"slow_ms"`

	// CORSOrigins are the browser origins (https://app.example.com) that
	// may call the daemon. Empty (default): every request with an Origin
	// header is refused (CSRF guard).
	CORSOrigins []string `yaml:"cors_origins"`

	// AllowedHosts are more Host names that a TCP request with no token
	// may use; an IP, localhost and a name with no dot always pass (DNS
	// rebinding guard).
	AllowedHosts []string `yaml:"allowed_hosts"`

	// TrustProxy takes the client IP of the token lockout from the last
	// X-Forwarded-For hop. Set it only behind a proxy that sets the header.
	TrustProxy bool `yaml:"trust_proxy"`

	// AuthFailures failed token checks of one IP in AuthWindow lock the IP
	// out for AuthLockout (default 10 in 1m, for 5m; 0 = off).
	AuthFailures    int           `yaml:"auth_failures"`
	AuthWindowText  string        `yaml:"auth_window"`
	AuthLockoutText string        `yaml:"auth_lockout"`
	AuthWindow      time.Duration `yaml:"-"`
	AuthLockout     time.Duration `yaml:"-"`

	// MaxConns is the most open connections of the TCP listener, and
	// MaxWatches the most open _watch streams (0 = no limit, the default).
	MaxConns   int `yaml:"max_conns"`
	MaxWatches int `yaml:"max_watches"`
}

// The defaults of the token lockout.
const (
	DefaultAuthFailures = 10
	DefaultAuthWindow   = "1m"
	DefaultAuthLockout  = "5m"
)

// Token is one bearer token of ndb.yml: a name, the file that holds the
// secret, and the collections (names or globs, such as calls*) it may read,
// write or administer. Write includes read; admin includes both.
type Token struct {
	Name  string   `yaml:"name"`
	File  string   `yaml:"file"`
	Read  []string `yaml:"read"`
	Write []string `yaml:"write"`
	Admin []string `yaml:"admin"`
}

// file is the YAML shape; sizes are strings like "1GiB".
type file struct {
	Config `yaml:",inline"`

	MemoryLimit string                `yaml:"memory_limit"`
	WriteBuffer string                `yaml:"write_buffer"`
	GroupLimit  string                `yaml:"group_limit"`
	BlobMaxSize string                `yaml:"blob_max_size"`
	ReadCache   string                `yaml:"read_cache"`
	Collections map[string]collection `yaml:"collections"`
}

type collection struct {
	catalog.Def `yaml:",inline"`

	MaxLineSize   string `yaml:"max_line_size"`
	BlobThreshold string `yaml:"blob_threshold"`
}

func defaults() Config {
	cfg := Config{
		DataDir:       SystemDataDir,
		Listen:        Listen{Unix: "/run/ndb/ndb.sock", UnixMode: 0o660},
		AllowImplicit: true,
		PITR:          DefaultPITR,
		IdleClose:     DefaultIdleClose,
		Collections:   map[string]catalog.Def{},
		LogLevel:      logging.DefaultLevel,
		LogFormat:     logging.DefaultFormat,
		LogBodyMax:    logging.DefaultBodyMax,
		LogSlowMs:     logging.DefaultSlowMs,

		AuthFailures:    DefaultAuthFailures,
		AuthWindowText:  DefaultAuthWindow,
		AuthLockoutText: DefaultAuthLockout,
		AuthWindow:      time.Minute,
		AuthLockout:     5 * time.Minute,
	}

	if home, ok := userHomeFunc(); ok {
		cfg.DataDir = filepath.Join(home, ".ndb", "data")
		cfg.Listen.Unix = filepath.Join(home, ".ndb", "ndb.sock")
		cfg.Listen.TCP = UserTCP
	}

	return cfg
}

// SystemDataDir is the data dir of a server install (systemd, Docker).
const SystemDataDir = "/var/lib/ndb"

// UserTCP is the TCP listener of a user install.
const UserTCP = "127.0.0.1:7070"

// userHomeFunc is userHome; tests replace it.
var userHomeFunc = userHome

// userHome gives the home dir when ndb runs as a user install: not root,
// and no SystemDataDir on the machine. Then "ndb serve" with no flags works
// with no root: ~/.ndb/data, ~/.ndb/ndb.sock and 127.0.0.1:7070. A server
// install keeps the system paths, for the daemon and for the CLI clients.
func userHome() (string, bool) {
	if os.Geteuid() == 0 {
		return "", false
	}

	if info, err := os.Stat(SystemDataDir); err == nil && info.IsDir() {
		return "", false
	}

	home, err := os.UserHomeDir()

	if err != nil || home == "" {
		return "", false
	}

	return home, true
}

// Load reads path. A missing file gives the defaults: ndb.yml is optional.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)

	if errors.Is(err, os.ErrNotExist) {
		return defaults(), nil
	}

	if err != nil {
		return Config{}, err
	}

	f := file{Config: defaults()}

	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	if err := dec.Decode(&f); err != nil {
		return Config{}, fmt.Errorf("%w: %s: %v", ErrInvalid, path, err)
	}

	cfg := f.Config
	cfg.Collections = map[string]catalog.Def{}

	if cfg.Listen.UnixModeText != "" {
		if cfg.Listen.UnixMode, err = ParseUnixMode(cfg.Listen.UnixModeText); err != nil {
			return Config{}, fmt.Errorf("%w: listen.%v", ErrInvalid, err)
		}
	}

	if cfg.MemoryLimit, err = parseSize(f.MemoryLimit); err != nil {
		return Config{}, fmt.Errorf("%w: memory_limit: %v", ErrInvalid, err)
	}

	if cfg.WriteBuffer, err = parseSize(f.WriteBuffer); err != nil {
		return Config{}, fmt.Errorf("%w: write_buffer: %v", ErrInvalid, err)
	}

	if cfg.GroupLimit, err = parseSize(f.GroupLimit); err != nil {
		return Config{}, fmt.Errorf("%w: group_limit: %v", ErrInvalid, err)
	}

	if cfg.ReadCache, err = parseSize(f.ReadCache); err != nil {
		return Config{}, fmt.Errorf("%w: read_cache: %v", ErrInvalid, err)
	}

	if cfg.BlobMaxSize, err = parseSize(f.BlobMaxSize); err != nil {
		return Config{}, fmt.Errorf("%w: blob_max_size: %v", ErrInvalid, err)
	}

	for name, c := range f.Collections {
		def := c.Def
		def.Name = name

		if def.MaxLineSize, err = parseSize(c.MaxLineSize); err != nil {
			return Config{}, fmt.Errorf("%w: %s.max_line_size: %v", ErrInvalid, name, err)
		}

		if def.BlobThreshold, err = parseSize(c.BlobThreshold); err != nil {
			return Config{}, fmt.Errorf("%w: %s.blob_threshold: %v", ErrInvalid, name, err)
		}

		cfg.Collections[name] = def
	}

	if _, err := catalog.ParsePITR(cfg.PITR); err != nil {
		return Config{}, fmt.Errorf("%w: pitr: %v", ErrInvalid, err)
	}

	if _, err := ParseIdleClose(cfg.IdleClose); err != nil {
		return Config{}, fmt.Errorf("%w: idle_close: %v", ErrInvalid, err)
	}

	if err := checkTokens(cfg.Tokens); err != nil {
		return Config{}, err
	}

	if err := checkLog(cfg); err != nil {
		return Config{}, err
	}

	if err := cfg.checkSecurity(); err != nil {
		return Config{}, err
	}

	cfg.resolve(filepath.Dir(path))

	return cfg, nil
}

func checkTokens(tokens []Token) error {
	seen := map[string]bool{}

	for i, t := range tokens {
		switch {
		case t.Name == "":
			return fmt.Errorf("%w: tokens[%d]: name is required", ErrInvalid, i)
		case seen[t.Name]:
			return fmt.Errorf("%w: tokens: the name %q is used twice", ErrInvalid, t.Name)
		case t.File == "":
			return fmt.Errorf("%w: token %s: file is required", ErrInvalid, t.Name)
		case len(t.Read)+len(t.Write)+len(t.Admin) == 0:
			return fmt.Errorf("%w: token %s: give it read, write or admin collections", ErrInvalid, t.Name)
		}

		for _, list := range [][]string{t.Read, t.Write, t.Admin} {
			for _, g := range list {
				if _, err := filepath.Match(g, ""); err != nil || g == "" {
					return fmt.Errorf("%w: token %s: bad collection pattern %q", ErrInvalid, t.Name, g)
				}
			}
		}

		seen[t.Name] = true
	}

	return nil
}

func checkLog(cfg Config) error {
	if _, err := logging.ParseLevel(cfg.LogLevel); err != nil {
		return fmt.Errorf("%w: log_level: %v", ErrInvalid, err)
	}

	if _, err := logging.ParseFormat(cfg.LogFormat); err != nil {
		return fmt.Errorf("%w: log_format: %v", ErrInvalid, err)
	}

	if cfg.LogBodyMax < 0 {
		return fmt.Errorf("%w: log_body_max: %d is under 0", ErrInvalid, cfg.LogBodyMax)
	}

	if cfg.LogSlowMs < 0 {
		return fmt.Errorf("%w: slow_ms: %d is under 0", ErrInvalid, cfg.LogSlowMs)
	}

	return nil
}

// checkSecurity checks the browser guards and the limits, and parses the
// lockout durations.
func (c *Config) checkSecurity() error {
	for _, o := range c.CORSOrigins {
		if err := CheckOrigin(o); err != nil {
			return fmt.Errorf("%w: cors_origins: %v", ErrInvalid, err)
		}
	}

	for _, h := range c.AllowedHosts {
		if err := CheckHost(h); err != nil {
			return fmt.Errorf("%w: allowed_hosts: %v", ErrInvalid, err)
		}
	}

	if c.AuthFailures < 0 {
		return fmt.Errorf("%w: auth_failures: %d is under 0", ErrInvalid, c.AuthFailures)
	}

	var err error

	if c.AuthWindow, err = catalog.ParseDuration(c.AuthWindowText); err != nil {
		return fmt.Errorf("%w: auth_window: %v", ErrInvalid, err)
	}

	if c.AuthLockout, err = catalog.ParseDuration(c.AuthLockoutText); err != nil {
		return fmt.Errorf("%w: auth_lockout: %v", ErrInvalid, err)
	}

	if c.MaxConns < 0 {
		return fmt.Errorf("%w: max_conns: %d is under 0", ErrInvalid, c.MaxConns)
	}

	if c.MaxWatches < 0 {
		return fmt.Errorf("%w: max_watches: %d is under 0", ErrInvalid, c.MaxWatches)
	}

	return nil
}

// CheckOrigin tells if o is a browser origin: http or https, a host, an
// optional port, and no path ("https://app.example.com:8443").
func CheckOrigin(o string) error {
	u, err := url.Parse(o)

	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.User != nil {
		return fmt.Errorf("%q is not an origin: want scheme://host[:port], such as https://app.example.com", o)
	}

	return nil
}

// CheckHost tells if h is a host name with no scheme, port or path.
func CheckHost(h string) error {
	if h == "" || strings.ContainsAny(h, ":/ ") {
		return fmt.Errorf("%q is not a host name: want a name such as ndb.internal (no scheme, no port)", h)
	}

	return nil
}

// resolve anchors the relative paths of the file to dir, the directory of
// ndb.yml, so they do not depend on where the daemon was started.
func (c *Config) resolve(dir string) {
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}

	c.DataDir = anchor(dir, c.DataDir)
	c.Listen.Unix = anchor(dir, c.Listen.Unix)

	for name, def := range c.Collections {
		def.Path = anchor(dir, def.Path)
		c.Collections[name] = def
	}

	for i := range c.Tokens {
		c.Tokens[i].File = anchor(dir, c.Tokens[i].File)
	}
}

func anchor(dir, p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}

	return filepath.Join(dir, p)
}

// Apply declares every collection of the file in the catalog.
func (c Config) Apply(cat *catalog.Catalog) error {
	for _, def := range c.Collections {
		if _, err := cat.Ensure(def, catalog.FromYAML); err != nil {
			return fmt.Errorf("ndb.yml collection %s: %w", def.Name, err)
		}
	}

	return nil
}

// parseSize reads "512", "64KiB", "2MiB", "1GiB" (also KB/MB/GB as 1024-based).
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)

	if s == "" {
		return 0, nil
	}

	units := []struct {
		suffix string
		mult   int64
	}{
		{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10},
		{"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10},
	}

	for _, u := range units {
		if num, ok := strings.CutSuffix(s, u.suffix); ok {
			n, err := strconv.ParseInt(strings.TrimSpace(num), 10, 64)

			return n * u.mult, err
		}
	}

	return strconv.ParseInt(s, 10, 64)
}

// ParseIdleClose reads idle_close: "off" is 0 (never close), empty is the
// default, else a duration of at least a second (10m, 1h, 1d).
func ParseIdleClose(s string) (time.Duration, error) {
	switch s {
	case "off":
		return 0, nil
	case "":
		s = DefaultIdleClose
	}

	d, err := catalog.ParseDuration(s)
	if err != nil {
		return 0, err
	}

	if d < time.Second {
		return 0, fmt.Errorf("%q is under a second; use off to keep collections open", s)
	}

	return d, nil
}
