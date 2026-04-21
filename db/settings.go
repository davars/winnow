package db

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/davars/winnow/config"
)

// ErrSettingsNotConfigured indicates that the database exists but the runtime
// settings row has not been written yet.
var ErrSettingsNotConfigured = errors.New("settings not configured")

// HasSettings reports whether the singleton runtime settings row exists.
func HasSettings(database *sql.DB) (bool, error) {
	var exists int
	err := database.QueryRow(`SELECT 1 FROM settings WHERE id = 1`).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking settings: %w", err)
	}
	return true, nil
}

// LoadSettings loads the singleton runtime settings row from the database.
func LoadSettings(database *sql.DB) (*config.Settings, error) {
	var settings config.Settings
	err := database.QueryRow(`
		SELECT raw_dir, clean_dir, trash_dir, pre_process_hook, reconcile_max_staleness, organize_timezone
		FROM settings
		WHERE id = 1
	`).Scan(
		&settings.RawDir,
		&settings.CleanDir,
		&settings.TrashDir,
		&settings.PreProcessHook,
		&settings.Reconcile.MaxStaleness,
		&settings.Organize.Timezone,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSettingsNotConfigured
	}
	if err != nil {
		return nil, fmt.Errorf("loading settings: %w", err)
	}
	return &settings, nil
}

// SaveSettings upserts the singleton runtime settings row.
func SaveSettings(database *sql.DB, settings *config.Settings) error {
	if err := settings.Validate(); err != nil {
		return fmt.Errorf("invalid settings: %w", err)
	}
	_, err := database.Exec(`
		INSERT INTO settings (
			id, raw_dir, clean_dir, trash_dir, pre_process_hook, reconcile_max_staleness, organize_timezone
		)
		VALUES (1, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			raw_dir = excluded.raw_dir,
			clean_dir = excluded.clean_dir,
			trash_dir = excluded.trash_dir,
			pre_process_hook = excluded.pre_process_hook,
			reconcile_max_staleness = excluded.reconcile_max_staleness,
			organize_timezone = excluded.organize_timezone
	`,
		settings.RawDir,
		settings.CleanDir,
		settings.TrashDir,
		settings.PreProcessHook,
		settings.Reconcile.MaxStaleness,
		settings.Organize.Timezone,
	)
	if err != nil {
		return fmt.Errorf("saving settings: %w", err)
	}
	return nil
}
