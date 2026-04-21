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

// Bootstrap is the minimal on-disk locator config. It only needs the data
// directory so winnow can find the SQLite database.
type Bootstrap struct {
	DataDir string `toml:"data_dir"`
}

// Validate checks that the locator config is usable.
func (b *Bootstrap) Validate() error {
	if b.DataDir == "" {
		return fmt.Errorf("data_dir is required")
	}
	return nil
}

// DBPath returns the path to the SQLite database file.
func (b *Bootstrap) DBPath() string {
	return filepath.Join(b.DataDir, "winnow.db")
}

// Settings holds runtime settings persisted in the database.
type Settings struct {
	RawDir   string
	CleanDir string
	TrashDir string

	PreProcessHook string

	Reconcile ReconcileConfig
	Organize  OrganizeConfig
}

// Config is a convenience wrapper for code that needs both the locator and the
// runtime settings in one value. Production bootstrap/runtime flow should use
// Bootstrap and Settings directly.
type Config struct {
	RawDir   string
	CleanDir string
	TrashDir string
	DataDir  string

	PreProcessHook string

	Reconcile ReconcileConfig
	Organize  OrganizeConfig
}

// DefaultSettings returns runtime defaults for a new install.
func DefaultSettings() *Settings {
	return &Settings{
		Reconcile: ReconcileConfig{MaxStaleness: DefaultMaxStaleness},
	}
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
	return c.BootstrapConfig().DBPath()
}

// MaxStalenessDuration returns the parsed max_staleness duration,
// falling back to DefaultMaxStaleness if not configured.
func (c *Config) MaxStalenessDuration() (time.Duration, error) {
	return c.RuntimeSettings().MaxStalenessDuration()
}

// Location returns the configured IANA timezone.
func (c *Config) Location() (*time.Location, error) {
	return c.RuntimeSettings().Location()
}

// Stores returns a map of store name to directory path.
func (c *Config) Stores() map[string]string {
	return c.RuntimeSettings().Stores()
}

// BootstrapConfig returns the locator portion of the combined config.
func (c *Config) BootstrapConfig() *Bootstrap {
	return &Bootstrap{DataDir: c.DataDir}
}

// RuntimeSettings returns the runtime settings portion of the combined config.
func (c *Config) RuntimeSettings() *Settings {
	return &Settings{
		RawDir:         c.RawDir,
		CleanDir:       c.CleanDir,
		TrashDir:       c.TrashDir,
		PreProcessHook: c.PreProcessHook,
		Reconcile:      c.Reconcile,
		Organize:       c.Organize,
	}
}

// Validate checks that all required store paths are set.
func (s *Settings) Validate() error {
	if s.RawDir == "" {
		return fmt.Errorf("raw_dir is required")
	}
	if s.CleanDir == "" {
		return fmt.Errorf("clean_dir is required")
	}
	if s.TrashDir == "" {
		return fmt.Errorf("trash_dir is required")
	}
	return nil
}

// MaxStalenessDuration returns the parsed max_staleness duration,
// falling back to DefaultMaxStaleness if not configured.
func (s *Settings) MaxStalenessDuration() (time.Duration, error) {
	value := s.Reconcile.MaxStaleness
	if value == "" {
		value = DefaultMaxStaleness
	}
	return time.ParseDuration(value)
}

// Location returns the IANA time zone specified by organize.timezone. Required
// by `winnow organize` to interpret naive EXIF timestamps deterministically.
func (s *Settings) Location() (*time.Location, error) {
	if s.Organize.Timezone == "" {
		return nil, fmt.Errorf("organize.timezone not set")
	}
	return time.LoadLocation(s.Organize.Timezone)
}

// Stores returns a map of store name to directory path.
func (s *Settings) Stores() map[string]string {
	return map[string]string{
		"raw":   s.RawDir,
		"clean": s.CleanDir,
		"trash": s.TrashDir,
	}
}

// LegacyConfig is the previous full TOML config format. It is only used by
// the temporary import-config migration command.
type LegacyConfig struct {
	RawDir   string `toml:"raw_dir"`
	CleanDir string `toml:"clean_dir"`
	TrashDir string `toml:"trash_dir"`
	DataDir  string `toml:"data_dir"`

	PreProcessHook string `toml:"pre_process_hook,omitempty"`

	Reconcile ReconcileConfig `toml:"reconcile"`
	Organize  OrganizeConfig  `toml:"organize"`
}

// Validate checks that all required fields are set.
func (c *LegacyConfig) Validate() error {
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

// BootstrapConfig returns the minimal locator portion of the legacy config.
func (c *LegacyConfig) BootstrapConfig() *Bootstrap {
	return &Bootstrap{DataDir: c.DataDir}
}

// RuntimeSettings returns the database-backed settings portion of the legacy config.
func (c *LegacyConfig) RuntimeSettings() *Settings {
	return &Settings{
		RawDir:         c.RawDir,
		CleanDir:       c.CleanDir,
		TrashDir:       c.TrashDir,
		PreProcessHook: c.PreProcessHook,
		Reconcile:      c.Reconcile,
		Organize:       c.Organize,
	}
}

// Load reads and parses a locator config file from the given path.
func Load(path string) (*Bootstrap, error) {
	var cfg Bootstrap
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("loading config from %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return &cfg, nil
}

// LoadLegacy reads and parses a legacy full config file from the given path.
func LoadLegacy(path string) (*LegacyConfig, error) {
	var cfg LegacyConfig
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("loading legacy config from %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid legacy config %s: %w", path, err)
	}
	return &cfg, nil
}

// Save writes cfg to path as TOML, creating parent directories as needed.
// Existing files at path are truncated.
func Save(path string, cfg *Bootstrap) error {
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

	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("no config file found (searched: %v)", candidates)
}

// Resolve returns the locator config from either the explicit data-dir override
// or the standard config-file search order. When the data-dir override is used,
// the returned config path is empty because no locator file was consulted.
func Resolve(dataDir, explicit string) (*Bootstrap, string, error) {
	if dataDir != "" {
		bootstrap := &Bootstrap{DataDir: dataDir}
		if err := bootstrap.Validate(); err != nil {
			return nil, "", err
		}
		return bootstrap, "", nil
	}

	path, err := Find(explicit)
	if err != nil {
		return nil, "", err
	}

	bootstrap, err := Load(path)
	if err != nil {
		return nil, "", err
	}
	return bootstrap, path, nil
}

// DefaultConfigPath returns the default path for writing a new config file.
func DefaultConfigPath() string {
	if path := xdgConfigPath(); path != "" {
		return path
	}
	return "winnow.toml"
}
