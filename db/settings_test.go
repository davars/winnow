package db

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/davars/winnow/config"
)

func TestLoadSettingsMissing(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "winnow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	if _, err := LoadSettings(database); !errors.Is(err, ErrSettingsNotConfigured) {
		t.Fatalf("LoadSettings() error = %v, want ErrSettingsNotConfigured", err)
	}
}

func TestSaveAndLoadSettings(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "winnow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	want := &config.Settings{
		RawDir:         "/tmp/raw",
		CleanDir:       "/tmp/clean",
		TrashDir:       "/tmp/trash",
		PreProcessHook: "/usr/local/bin/hook.sh",
		Reconcile:      config.ReconcileConfig{MaxStaleness: "24h"},
		Organize:       config.OrganizeConfig{Timezone: "America/New_York"},
	}
	if err := SaveSettings(database, want); err != nil {
		t.Fatal(err)
	}

	got, err := LoadSettings(database)
	if err != nil {
		t.Fatal(err)
	}
	if got.RawDir != want.RawDir || got.CleanDir != want.CleanDir || got.TrashDir != want.TrashDir {
		t.Fatalf("got stores %#v, want %#v", got.Stores(), want.Stores())
	}
	if got.PreProcessHook != want.PreProcessHook {
		t.Errorf("PreProcessHook = %q, want %q", got.PreProcessHook, want.PreProcessHook)
	}
	if got.Reconcile.MaxStaleness != want.Reconcile.MaxStaleness {
		t.Errorf("MaxStaleness = %q, want %q", got.Reconcile.MaxStaleness, want.Reconcile.MaxStaleness)
	}
	if got.Organize.Timezone != want.Organize.Timezone {
		t.Errorf("Timezone = %q, want %q", got.Organize.Timezone, want.Organize.Timezone)
	}
}

func TestHasSettings(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "winnow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	hasSettings, err := HasSettings(database)
	if err != nil {
		t.Fatal(err)
	}
	if hasSettings {
		t.Fatal("HasSettings() = true, want false before save")
	}

	if err := SaveSettings(database, &config.Settings{
		RawDir:   "/tmp/raw",
		CleanDir: "/tmp/clean",
		TrashDir: "/tmp/trash",
	}); err != nil {
		t.Fatal(err)
	}

	hasSettings, err = HasSettings(database)
	if err != nil {
		t.Fatal(err)
	}
	if !hasSettings {
		t.Fatal("HasSettings() = false, want true after save")
	}
}
