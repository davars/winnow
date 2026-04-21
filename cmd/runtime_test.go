package cmd

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/davars/winnow/config"
	"github.com/davars/winnow/db"
	"github.com/spf13/cobra"
)

func TestRunQueryWorksWithBootstrapOnly(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "winnow.toml")
	dataDirPath := filepath.Join(tmp, "data")

	if err := os.MkdirAll(dataDirPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(cfgPath, &config.Bootstrap{DataDir: dataDirPath}); err != nil {
		t.Fatal(err)
	}

	database, err := db.Open(filepath.Join(dataDirPath, "winnow.db"))
	if err != nil {
		t.Fatal(err)
	}
	database.Close()

	setCmdGlobals(t, cfgPath, "")

	output, err := captureStdout(t, func() error {
		return runQuery("SELECT 1 AS n", formatTSV, true)
	})
	if err != nil {
		t.Fatal(err)
	}
	if output != "n\n1\n" {
		t.Fatalf("output = %q, want %q", output, "n\n1\n")
	}
}

func TestRunWalkErrorsWhenSettingsMissing(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "winnow.toml")
	dataDirPath := filepath.Join(tmp, "data")

	if err := os.MkdirAll(dataDirPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(cfgPath, &config.Bootstrap{DataDir: dataDirPath}); err != nil {
		t.Fatal(err)
	}

	database, err := db.Open(filepath.Join(dataDirPath, "winnow.db"))
	if err != nil {
		t.Fatal(err)
	}
	database.Close()

	setCmdGlobals(t, cfgPath, "")

	err = runWalk(&cobra.Command{})
	if err == nil {
		t.Fatal("expected missing settings error")
	}
	if !strings.Contains(err.Error(), "run `winnow init` or `winnow import-config`") {
		t.Fatalf("error = %q, want init/import-config guidance", err)
	}
}

func TestRunStatusVerbosePrintsSettings(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "winnow.toml")
	dataDirPath := filepath.Join(tmp, "data")

	if err := os.MkdirAll(dataDirPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(cfgPath, &config.Bootstrap{DataDir: dataDirPath}); err != nil {
		t.Fatal(err)
	}

	database, err := db.Open(filepath.Join(dataDirPath, "winnow.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveSettings(database, &config.Settings{
		RawDir:         "/tmp/raw",
		CleanDir:       "/tmp/clean",
		TrashDir:       "/tmp/trash",
		PreProcessHook: "/tmp/hook.sh",
		Reconcile:      config.ReconcileConfig{MaxStaleness: "24h"},
		Organize:       config.OrganizeConfig{Timezone: "America/New_York"},
	}); err != nil {
		t.Fatal(err)
	}
	database.Close()

	setCmdGlobals(t, cfgPath, "")

	output, err := captureStdout(t, func() error {
		return runStatus(true)
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Config: " + cfgPath,
		"data_dir:  " + dataDirPath,
		"raw_dir:   /tmp/raw",
		"clean_dir: /tmp/clean",
		"trash_dir: /tmp/trash",
		"hook:      /tmp/hook.sh",
		"staleness: 24h",
		"timezone:  America/New_York",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func TestRunImportConfigImportsLegacyConfigAndRewritesFile(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "winnow.toml")
	dataDirPath := filepath.Join(tmp, "data")
	legacy := `
raw_dir   = "/tmp/raw"
clean_dir = "/tmp/clean"
trash_dir = "/tmp/trash"
data_dir  = "` + dataDirPath + `"
pre_process_hook = "/tmp/hook.sh"

[reconcile]
max_staleness = "24h"

[organize]
timezone = "America/New_York"
`
	if err := os.WriteFile(cfgPath, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	setCmdGlobals(t, cfgPath, "")

	if err := runImportConfig(false); err != nil {
		t.Fatal(err)
	}

	bootstrap, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if bootstrap.DataDir != dataDirPath {
		t.Errorf("DataDir = %q, want %q", bootstrap.DataDir, dataDirPath)
	}

	content, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "raw_dir") {
		t.Fatalf("legacy keys should be removed, got:\n%s", string(content))
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
	if settings.RawDir != "/tmp/raw" || settings.CleanDir != "/tmp/clean" || settings.TrashDir != "/tmp/trash" {
		t.Fatalf("unexpected imported stores: %#v", settings.Stores())
	}
	if settings.PreProcessHook != "/tmp/hook.sh" {
		t.Errorf("PreProcessHook = %q, want /tmp/hook.sh", settings.PreProcessHook)
	}
}

func TestRunImportConfigRequiresForceToReplaceExistingSettings(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "winnow.toml")
	dataDirPath := filepath.Join(tmp, "data")
	legacy := `
raw_dir   = "/tmp/new-raw"
clean_dir = "/tmp/new-clean"
trash_dir = "/tmp/new-trash"
data_dir  = "` + dataDirPath + `"
`
	if err := os.WriteFile(cfgPath, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	database, err := db.Open(filepath.Join(dataDirPath, "winnow.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveSettings(database, &config.Settings{
		RawDir:   "/tmp/original-raw",
		CleanDir: "/tmp/original-clean",
		TrashDir: "/tmp/original-trash",
	}); err != nil {
		t.Fatal(err)
	}
	database.Close()

	setCmdGlobals(t, cfgPath, "")

	err = runImportConfig(false)
	if err == nil {
		t.Fatal("expected --force error")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Fatalf("error = %q, want --force guidance", err)
	}

	if err := runImportConfig(true); err != nil {
		t.Fatal(err)
	}

	database, err = db.Open(filepath.Join(dataDirPath, "winnow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	settings, err := db.LoadSettings(database)
	if err != nil {
		t.Fatal(err)
	}
	if settings.RawDir != "/tmp/new-raw" || settings.CleanDir != "/tmp/new-clean" || settings.TrashDir != "/tmp/new-trash" {
		t.Fatalf("force import did not replace settings: %#v", settings.Stores())
	}
}

func setCmdGlobals(t *testing.T, configPath, explicitDataDir string) {
	t.Helper()
	prevCfgFile, prevDataDir := cfgFile, dataDir
	cfgFile, dataDir = configPath, explicitDataDir
	t.Cleanup(func() {
		cfgFile, dataDir = prevCfgFile, prevDataDir
	})
}

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = origStdout }()

	runErr := fn()
	_ = w.Close()

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()

	return buf.String(), runErr
}
