// Package config loads the optional ndb.yml.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/thadeu/ndb/internal/catalog"
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

type Config struct {
	DataDir       string `yaml:"data_dir"`
	Listen        Listen `yaml:"listen"`
	MemoryLimit   int64  `yaml:"-"`
	WriteBuffer   int64  `yaml:"-"`
	GroupLimit    int64  `yaml:"-"`
	BlobMaxSize   int64  `yaml:"-"`
	AllowImplicit bool   `yaml:"allow_implicit"`

	Collections map[string]catalog.Def `yaml:"-"`
}

// file is the YAML shape; sizes are strings like "1GiB".
type file struct {
	Config `yaml:",inline"`

	MemoryLimit string                `yaml:"memory_limit"`
	WriteBuffer string                `yaml:"write_buffer"`
	GroupLimit  string                `yaml:"group_limit"`
	BlobMaxSize string                `yaml:"blob_max_size"`
	Collections map[string]collection `yaml:"collections"`
}

type collection struct {
	catalog.Def `yaml:",inline"`

	MaxLineSize   string `yaml:"max_line_size"`
	BlobThreshold string `yaml:"blob_threshold"`
}

func defaults() Config {
	return Config{
		DataDir:       "/var/lib/ndb",
		Listen:        Listen{Unix: "/run/ndb/ndb.sock", UnixMode: 0o660},
		AllowImplicit: true,
		Collections:   map[string]catalog.Def{},
	}
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

	cfg.resolve(filepath.Dir(path))

	return cfg, nil
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
