package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
)

// DefaultMaxStaleness is the default max_staleness if not configured.
const DefaultMaxStaleness = "48h"

// ReconcileConfig holds reconcile-specific configuration.
type ReconcileConfig struct {
	MaxStaleness string `toml:"max_staleness"`
}

// OrganizeConfig holds organize-specific configuration.
type OrganizeConfig struct {
	// Timezone is the IANA name used to interpret naive EXIF timestamps when no
	// matching offset tag is present. Required by `winnow organize`; other
	// subcommands ignore it.
	Timezone string `toml:"timezone"`
}

// Config holds the application configuration.
type Config struct {
	RawDir   string `toml:"raw_dir"`
	CleanDir string `toml:"clean_dir"`
	TrashDir string `toml:"trash_dir"`
	DataDir  string `toml:"data_dir"`

	PreProcessHook string `toml:"pre_process_hook,omitempty"`

	Reconcile ReconcileConfig `toml:"reconcile"`
	Organize  OrganizeConfig  `toml:"organize"`
}

// Validate checks that all required fields are set.
func (c *Config) Validate() error {
	if c.RawDir == "" {
		return fmt.Errorf("raw_dir is required")
	}
	if c.CleanDir == "" {
		return fmt.Errorf("clean_dir is required")
	}
	if c.TrashDir == "" {
		return fmt.Errorf("trash_dir is required")
	}
	if c.DataDir == "" {
		return fmt.Errorf("data_dir is required")
	}
	return nil
}

// DBPath returns the path to the SQLite database file.
func (c *Config) DBPath() string {
	return filepath.Join(c.DataDir, "winnow.db")
}

// MaxStalenessDuration returns the parsed max_staleness duration,
// falling back to DefaultMaxStaleness if not configured.
func (c *Config) MaxStalenessDuration() (time.Duration, error) {
	s := c.Reconcile.MaxStaleness
	if s == "" {
		s = DefaultMaxStaleness
	}
	return time.ParseDuration(s)
}

// Location returns the IANA time zone specified by organize.timezone. Required
// by `winnow organize` to interpret naive EXIF timestamps deterministically.
func (c *Config) Location() (*time.Location, error) {
	if c.Organize.Timezone == "" {
		return nil, fmt.Errorf("organize.timezone not set")
	}
	return time.LoadLocation(c.Organize.Timezone)
}

// Stores returns a map of store name to directory path.
func (c *Config) Stores() map[string]string {
	return map[string]string{
		"raw":   c.RawDir,
		"clean": c.CleanDir,
		"trash": c.TrashDir,
	}
}

// Load reads and parses a config file from the given path.
func Load(path string) (*Config, error) {
	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("loading config from %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return &cfg, nil
}

// Save writes cfg to path as TOML, creating parent directories as needed.
// Existing files at path are truncated. Used by `winnow init` and by
// subcommands that prompt the user for previously-missing fields.
func Save(path string, cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating config file: %w", err)
	}
	defer f.Close()
	if err := toml.NewEncoder(f).Encode(cfg); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	return nil
}

// xdgConfigPath returns the XDG-based config path, or "" if it cannot be determined.
func xdgConfigPath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "winnow", "winnow.toml")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "winnow", "winnow.toml")
	}
	return ""
}

// Find locates the config file using the search order:
//  1. explicit path (from -c flag)
//  2. $WINNOW_CONFIG
//  3. $XDG_CONFIG_HOME/winnow/winnow.toml
//  4. ./winnow.toml
//
// Returns the path to the first file that exists, or an error if none found.
func Find(explicit string) (string, error) {
	var candidates []string

	if explicit != "" {
		candidates = append(candidates, explicit)
	}

	if env := os.Getenv("WINNOW_CONFIG"); env != "" {
		candidates = append(candidates, env)
	}

	if xdg := xdgConfigPath(); xdg != "" {
		candidates = append(candidates, xdg)
	}

	candidates = append(candidates, "winnow.toml")

	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}

	return "", fmt.Errorf("no config file found (searched: %v)", candidates)
}

// DefaultConfigPath returns the default path for writing a new config file.
func DefaultConfigPath() string {
	if p := xdgConfigPath(); p != "" {
		return p
	}
	return "winnow.toml"
}
