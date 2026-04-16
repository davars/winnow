package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/davars/winnow/config"
)

func TestResolveInitTarget_CreateMode(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("WINNOW_CONFIG", "")

	dest, cfg, err := resolveInitTarget("")
	if err != nil {
		t.Fatal(err)
	}

	if dest == "" {
		t.Fatal("dest is empty")
	}
	if cfg.Reconcile.MaxStaleness != config.DefaultMaxStaleness {
		t.Errorf("MaxStaleness = %q, want %q", cfg.Reconcile.MaxStaleness, config.DefaultMaxStaleness)
	}
	if cfg.Organize.Timezone == "" {
		t.Error("Timezone should be set to system default or UTC")
	}
}

func TestResolveInitTarget_EditMode(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "winnow.toml")

	cfg := config.Config{
		RawDir:   "/tmp/raw",
		CleanDir: "/tmp/clean",
		TrashDir: "/tmp/trash",
		DataDir:  "/tmp/data",
		Organize: config.OrganizeConfig{Timezone: "Europe/Berlin"},
	}
	if err := config.Save(cfgPath, &cfg); err != nil {
		t.Fatal(err)
	}

	t.Setenv("WINNOW_CONFIG", cfgPath)

	dest, loaded, err := resolveInitTarget("")
	if err != nil {
		t.Fatal(err)
	}

	if dest != cfgPath {
		t.Errorf("dest = %q, want %q", dest, cfgPath)
	}
	if loaded.RawDir != "/tmp/raw" {
		t.Errorf("RawDir = %q, want /tmp/raw", loaded.RawDir)
	}
	if loaded.Organize.Timezone != "Europe/Berlin" {
		t.Errorf("Timezone = %q, want Europe/Berlin", loaded.Organize.Timezone)
	}
}

func TestResolveInitTarget_PermissiveLoad(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "winnow.toml")

	// Write a config missing raw_dir (which would fail strict Load)
	f, err := os.Create(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := toml.NewEncoder(f).Encode(config.Config{
		CleanDir: "/tmp/clean",
		TrashDir: "/tmp/trash",
		DataDir:  "/tmp/data",
	}); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	t.Setenv("WINNOW_CONFIG", cfgPath)

	_, loaded, err := resolveInitTarget("")
	if err != nil {
		t.Fatalf("resolveInitTarget should tolerate invalid config, got: %v", err)
	}
	if loaded.RawDir != "" {
		t.Errorf("RawDir = %q, want empty", loaded.RawDir)
	}
	if loaded.CleanDir != "/tmp/clean" {
		t.Errorf("CleanDir = %q, want /tmp/clean", loaded.CleanDir)
	}
}

func TestResolveInitTarget_ExplicitFlag(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "winnow.toml")

	if err := config.Save(cfgPath, &config.Config{
		RawDir:   "/a",
		CleanDir: "/b",
		TrashDir: "/c",
		DataDir:  "/d",
	}); err != nil {
		t.Fatal(err)
	}

	// Even with XDG pointing elsewhere, explicit flag wins
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("WINNOW_CONFIG", "")

	dest, _, err := resolveInitTarget(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if dest != cfgPath {
		t.Errorf("dest = %q, want %q", dest, cfgPath)
	}
}

func TestExpandTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}

	tests := []struct {
		in, want string
	}{
		{"~/foo", filepath.Join(home, "foo")},
		{"/absolute/path", "/absolute/path"},
		{"relative/path", "relative/path"},
		{"~nope", "~nope"},
	}
	for _, tc := range tests {
		if got := expandTilde(tc.in); got != tc.want {
			t.Errorf("expandTilde(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestExpandAndResolve(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}

	got, err := expandAndResolve("~/foo")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "foo")
	if got != want {
		t.Errorf("expandAndResolve(\"~/foo\") = %q, want %q", got, want)
	}
}

func TestDirSuggestions(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "alpha"))
	mustMkdir(t, filepath.Join(root, "alpha-two"))
	mustWrite(t, filepath.Join(root, "alpha-file"), "not a dir")

	got := dirSuggestions(filepath.Join(root, "alph"))
	if len(got) != 2 {
		t.Fatalf("got %v, want 2 dir suggestions", got)
	}
	for _, s := range got {
		if !filepath.IsAbs(s) {
			t.Errorf("suggestion %q is not absolute", s)
		}
		if s[len(s)-1] != filepath.Separator {
			t.Errorf("suggestion %q should end with separator", s)
		}
	}
}

func TestDirSuggestionsEmpty(t *testing.T) {
	got := dirSuggestions("")
	if len(got) != 0 {
		t.Errorf("got %v, want empty for empty input", got)
	}
}
