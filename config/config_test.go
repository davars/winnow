package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadValidBootstrap(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "winnow.toml")
	content := `
raw_dir   = "/tmp/raw"
clean_dir = "/tmp/clean"
trash_dir = "/tmp/trash"
data_dir  = "/tmp/data"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir != "/tmp/data" {
		t.Errorf("DataDir = %q, want /tmp/data", cfg.DataDir)
	}
}

func TestLoadMissingDataDir(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "winnow.toml")
	if err := os.WriteFile(cfgPath, []byte("raw_dir = \"/tmp/raw\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(cfgPath); err == nil {
		t.Fatal("expected error for missing data_dir")
	}
}

func TestLoadNonexistentFile(t *testing.T) {
	if _, err := Load("/nonexistent/winnow.toml"); err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestSaveWritesMinimalConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "winnow.toml")

	if err := Save(cfgPath, &Bootstrap{DataDir: "/tmp/data"}); err != nil {
		t.Fatal(err)
	}

	content, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(content)
	if !strings.Contains(got, `data_dir = "/tmp/data"`) {
		t.Fatalf("saved config missing data_dir: %s", got)
	}
	if strings.Contains(got, "raw_dir") {
		t.Fatalf("saved config should be minimal, got: %s", got)
	}
}

func TestLoadLegacyConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "winnow.toml")
	content := `
raw_dir   = "/tmp/raw"
clean_dir = "/tmp/clean"
trash_dir = "/tmp/trash"
data_dir  = "/tmp/data"
pre_process_hook = "/usr/local/bin/snapshot.sh"

[reconcile]
max_staleness = "24h"

[organize]
timezone = "America/Los_Angeles"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadLegacy(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir != "/tmp/data" {
		t.Errorf("DataDir = %q, want /tmp/data", cfg.DataDir)
	}
	if cfg.PreProcessHook != "/usr/local/bin/snapshot.sh" {
		t.Errorf("PreProcessHook = %q, want /usr/local/bin/snapshot.sh", cfg.PreProcessHook)
	}
	if cfg.Reconcile.MaxStaleness != "24h" {
		t.Errorf("MaxStaleness = %q, want 24h", cfg.Reconcile.MaxStaleness)
	}
	if cfg.Organize.Timezone != "America/Los_Angeles" {
		t.Errorf("Timezone = %q, want America/Los_Angeles", cfg.Organize.Timezone)
	}
}

func TestResolveWithDataDirOverride(t *testing.T) {
	bootstrap, path, err := Resolve("/tmp/data", "")
	if err != nil {
		t.Fatal(err)
	}
	if path != "" {
		t.Errorf("path = %q, want empty", path)
	}
	if bootstrap.DataDir != "/tmp/data" {
		t.Errorf("DataDir = %q, want /tmp/data", bootstrap.DataDir)
	}
}

func TestBootstrapDBPath(t *testing.T) {
	cfg := Bootstrap{DataDir: "/tmp/data"}
	if got := cfg.DBPath(); got != "/tmp/data/winnow.db" {
		t.Errorf("DBPath() = %q, want /tmp/data/winnow.db", got)
	}
}

func TestFindExplicitPath(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "winnow.toml")
	if err := os.WriteFile(cfgPath, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	found, err := Find(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if found != cfgPath {
		t.Errorf("Find() = %q, want %q", found, cfgPath)
	}
}

func TestFindEnvVar(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "winnow.toml")
	if err := os.WriteFile(cfgPath, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("WINNOW_CONFIG", cfgPath)

	found, err := Find("")
	if err != nil {
		t.Fatal(err)
	}
	if found != cfgPath {
		t.Errorf("Find() = %q, want %q", found, cfgPath)
	}
}

func TestFindXDGConfigHome(t *testing.T) {
	dir := t.TempDir()
	xdgDir := filepath.Join(dir, "winnow")
	if err := os.MkdirAll(xdgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(xdgDir, "winnow.toml")
	if err := os.WriteFile(cfgPath, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("WINNOW_CONFIG", "")

	found, err := Find("")
	if err != nil {
		t.Fatal(err)
	}
	if found != cfgPath {
		t.Errorf("Find() = %q, want %q", found, cfgPath)
	}
}

func TestFindSearchOrder(t *testing.T) {
	dir := t.TempDir()

	explicitPath := filepath.Join(dir, "explicit.toml")
	envPath := filepath.Join(dir, "env.toml")

	if err := os.WriteFile(explicitPath, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envPath, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("WINNOW_CONFIG", envPath)

	found, err := Find(explicitPath)
	if err != nil {
		t.Fatal(err)
	}
	if found != explicitPath {
		t.Errorf("Find() = %q, want %q (explicit should win)", found, explicitPath)
	}
}

func TestSettingsMaxStalenessDuration(t *testing.T) {
	cfg := Settings{RawDir: "/r", CleanDir: "/c", TrashDir: "/t"}

	d, err := cfg.MaxStalenessDuration()
	if err != nil {
		t.Fatal(err)
	}
	if d != 48*time.Hour {
		t.Errorf("default MaxStalenessDuration = %v, want 48h", d)
	}

	cfg.Reconcile.MaxStaleness = "24h"
	d, err = cfg.MaxStalenessDuration()
	if err != nil {
		t.Fatal(err)
	}
	if d != 24*time.Hour {
		t.Errorf("configured MaxStalenessDuration = %v, want 24h", d)
	}

	cfg.Reconcile.MaxStaleness = "bogus"
	if _, err := cfg.MaxStalenessDuration(); err == nil {
		t.Error("expected error for invalid max_staleness")
	}
}

func TestSettingsLocation(t *testing.T) {
	cfg := Settings{Organize: OrganizeConfig{Timezone: "America/New_York"}}
	loc, err := cfg.Location()
	if err != nil {
		t.Fatalf("Location() for valid tz: %v", err)
	}
	if loc.String() != "America/New_York" {
		t.Errorf("Location().String() = %q, want America/New_York", loc.String())
	}

	cfg = Settings{}
	if _, err := cfg.Location(); err == nil {
		t.Error("Location() for empty tz: expected error")
	}

	cfg = Settings{Organize: OrganizeConfig{Timezone: "Bogus/Place"}}
	if _, err := cfg.Location(); err == nil {
		t.Error("Location() for bogus tz: expected error")
	}
}

func TestCombinedConfigHelpers(t *testing.T) {
	cfg := Config{
		RawDir:   "/tmp/raw",
		CleanDir: "/tmp/clean",
		TrashDir: "/tmp/trash",
		DataDir:  "/tmp/data",
	}
	if got := cfg.DBPath(); got != "/tmp/data/winnow.db" {
		t.Errorf("DBPath() = %q, want /tmp/data/winnow.db", got)
	}
	stores := cfg.Stores()
	if stores["raw"] != "/tmp/raw" || stores["clean"] != "/tmp/clean" || stores["trash"] != "/tmp/trash" {
		t.Errorf("Stores() = %#v", stores)
	}
}

func TestFindNothingFound(t *testing.T) {
	t.Setenv("WINNOW_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	orig, _ := os.Getwd()
	defer os.Chdir(orig)
	_ = os.Chdir(t.TempDir())

	if _, err := Find(""); err == nil {
		t.Fatal("expected error when no config found")
	}
}
