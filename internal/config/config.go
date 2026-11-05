// Package config loads the optional ndb.yml.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/thadeu/ndb/internal/catalog"
)

var ErrInvalid = errors.New("config: invalid ndb.yml")

type Listen struct {
	Unix string `yaml:"unix"`
	TCP  string `yaml:"tcp"`
}

type Config struct {
	DataDir       string `yaml:"data_dir"`
	Listen        Listen `yaml:"listen"`
	MemoryLimit   int64  `yaml:"-"`
	AllowImplicit bool   `yaml:"allow_implicit"`

	Collections map[string]catalog.Def `yaml:"-"`
}

// file is the YAML shape; sizes are strings like "1GiB".
type file struct {
	Config `yaml:",inline"`

	MemoryLimit string                `yaml:"memory_limit"`
	Collections map[string]collection `yaml:"collections"`
}

type collection struct {
	catalog.Def `yaml:",inline"`

	MaxLineSize   string `yaml:"max_line_size"`
	BlobThreshold string `yaml:"blob_threshold"`
}

func defaults() Config {
	return Config{
		DataDir:     "/var/lib/ndb",
		Listen:      Listen{Unix: "/run/ndb.sock"},
		Collections: map[string]catalog.Def{},
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

	if cfg.MemoryLimit, err = parseSize(f.MemoryLimit); err != nil {
		return Config{}, fmt.Errorf("%w: memory_limit: %v", ErrInvalid, err)
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

	return cfg, nil
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
