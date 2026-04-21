package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/davars/winnow/config"
	"github.com/davars/winnow/db"
)

func TestResolveInitTargetCreateMode(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("WINNOW_CONFIG", "")

	dest, bootstrap, editing, err := resolveInitTarget("", "")
	if err != nil {
		t.Fatal(err)
	}
	if editing {
		t.Fatal("editing should be false for create mode")
	}
	if dest == "" {
		t.Fatal("dest is empty")
	}
	if bootstrap.DataDir != "" {
		t.Errorf("DataDir = %q, want empty", bootstrap.DataDir)
	}
}

func TestResolveInitTargetEditMode(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "winnow.toml")

	if err := config.Save(cfgPath, &config.Bootstrap{DataDir: "/tmp/data"}); err != nil {
		t.Fatal(err)
	}

	t.Setenv("WINNOW_CONFIG", cfgPath)

	dest, bootstrap, editing, err := resolveInitTarget("", "")
	if err != nil {
		t.Fatal(err)
	}
	if !editing {
		t.Fatal("editing should be true")
	}
	if dest != cfgPath {
		t.Errorf("dest = %q, want %q", dest, cfgPath)
	}
	if bootstrap.DataDir != "/tmp/data" {
		t.Errorf("DataDir = %q, want /tmp/data", bootstrap.DataDir)
	}
}

func TestResolveInitTargetExplicitFlagWins(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "custom.toml")

	dest, bootstrap, editing, err := resolveInitTarget("/tmp/override", cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if editing {
		t.Fatal("editing should be false for a new explicit path")
	}
	if dest != cfgPath {
		t.Errorf("dest = %q, want %q", dest, cfgPath)
	}
	if bootstrap.DataDir != "/tmp/override" {
		t.Errorf("DataDir = %q, want /tmp/override", bootstrap.DataDir)
	}
}

func TestRunInitWithIOWritesBootstrapAndDBSettings(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "winnow.toml")
	dataDirPath := filepath.Join(tmp, "data")
	rawDir := filepath.Join(tmp, "raw")
	cleanDir := filepath.Join(tmp, "clean")
	trashDir := filepath.Join(tmp, "trash")

	for _, dir := range []string{dataDirPath, rawDir, cleanDir, trashDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	prevCfgFile, prevDataDir := cfgFile, dataDir
	cfgFile, dataDir = cfgPath, ""
	t.Cleanup(func() {
		cfgFile, dataDir = prevCfgFile, prevDataDir
	})

	input := strings.Join([]string{
		dataDirPath,
		rawDir,
		cleanDir,
		trashDir,
		"America/New_York",
		"/usr/local/bin/hook.sh",
		"24h",
		"",
	}, "\n")
	var out bytes.Buffer
	if err := runInitWithIO(strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}

	bootstrap, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if bootstrap.DataDir != dataDirPath {
		t.Errorf("DataDir = %q, want %q", bootstrap.DataDir, dataDirPath)
	}

	database, err := db.Open(bootstrap.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	settings, err := db.LoadSettings(database)
	if err != nil {
		t.Fatal(err)
	}
	if settings.RawDir != rawDir {
		t.Errorf("RawDir = %q, want %q", settings.RawDir, rawDir)
	}
	if settings.CleanDir != cleanDir {
		t.Errorf("CleanDir = %q, want %q", settings.CleanDir, cleanDir)
	}
	if settings.TrashDir != trashDir {
		t.Errorf("TrashDir = %q, want %q", settings.TrashDir, trashDir)
	}
	if settings.PreProcessHook != "/usr/local/bin/hook.sh" {
		t.Errorf("PreProcessHook = %q, want /usr/local/bin/hook.sh", settings.PreProcessHook)
	}
	if settings.Reconcile.MaxStaleness != "24h" {
		t.Errorf("MaxStaleness = %q, want 24h", settings.Reconcile.MaxStaleness)
	}
	if settings.Organize.Timezone != "America/New_York" {
		t.Errorf("Timezone = %q, want America/New_York", settings.Organize.Timezone)
	}
}

func TestRunInitWithIOCreatesMissingDirectories(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "winnow.toml")
	dataDirPath := filepath.Join(tmp, "data")
	rawDir := filepath.Join(tmp, "raw")
	cleanDir := filepath.Join(tmp, "clean")
	trashDir := filepath.Join(tmp, "trash")

	prevCfgFile, prevDataDir := cfgFile, dataDir
	cfgFile, dataDir = cfgPath, ""
	t.Cleanup(func() {
		cfgFile, dataDir = prevCfgFile, prevDataDir
	})

	input := strings.Join([]string{
		dataDirPath,
		"y",
		rawDir,
		cleanDir,
		trashDir,
		"",
		"",
		"",
		"y",
		"y",
		"y",
		"",
	}, "\n")
	var out bytes.Buffer
	if err := runInitWithIO(strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}

	for _, dir := range []string{dataDirPath, rawDir, cleanDir, trashDir} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Fatalf("expected %s to exist as a directory", dir)
		}
	}
}

func TestRunInitWithIORejectsInvalidDuration(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "winnow.toml")
	dataDirPath := filepath.Join(tmp, "data")
	rawDir := filepath.Join(tmp, "raw")
	cleanDir := filepath.Join(tmp, "clean")
	trashDir := filepath.Join(tmp, "trash")

	for _, dir := range []string{dataDirPath, rawDir, cleanDir, trashDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	prevCfgFile, prevDataDir := cfgFile, dataDir
	cfgFile, dataDir = cfgPath, ""
	t.Cleanup(func() {
		cfgFile, dataDir = prevCfgFile, prevDataDir
	})

	input := strings.Join([]string{
		dataDirPath,
		rawDir,
		cleanDir,
		trashDir,
		"",
		"",
		"bogus",
		"24h",
		"",
	}, "\n")
	var out bytes.Buffer
	if err := runInitWithIO(strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `Invalid duration "bogus".`) {
		t.Fatalf("output = %q, want invalid duration message", out.String())
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
