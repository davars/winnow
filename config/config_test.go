package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadValidConfig(t *testing.T) {
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

	if cfg.RawDir != "/tmp/raw" {
		t.Errorf("RawDir = %q, want /tmp/raw", cfg.RawDir)
	}
	if cfg.CleanDir != "/tmp/clean" {
		t.Errorf("CleanDir = %q, want /tmp/clean", cfg.CleanDir)
	}
	if cfg.TrashDir != "/tmp/trash" {
		t.Errorf("TrashDir = %q, want /tmp/trash", cfg.TrashDir)
	}
	if cfg.DataDir != "/tmp/data" {
		t.Errorf("DataDir = %q, want /tmp/data", cfg.DataDir)
	}
}

func TestLoadMissingField(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "winnow.toml")
	content := `
raw_dir   = "/tmp/raw"
clean_dir = "/tmp/clean"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected error for missing fields")
	}
}

func TestLoadNonexistentFile(t *testing.T) {
	_, err := Load("/nonexistent/winnow.toml")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestDBPath(t *testing.T) {
	cfg := Config{DataDir: "/tmp/data"}
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
	// Explicit path takes precedence over WINNOW_CONFIG.
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

func TestFindNothingFound(t *testing.T) {
	t.Setenv("WINNOW_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// Change to a temp dir with no winnow.toml.
	orig, _ := os.Getwd()
	defer os.Chdir(orig)
	os.Chdir(t.TempDir())

	_, err := Find("")
	if err == nil {
		t.Fatal("expected error when no config found")
	}
}
