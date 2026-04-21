package cmd

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/davars/winnow/config"
	"github.com/davars/winnow/db"
)

type bootstrapState struct {
	Bootstrap  *config.Bootstrap
	ConfigPath string
}

func openBootstrapDB() (*bootstrapState, *sql.DB, error) {
	bootstrap, configPath, err := config.Resolve(dataDir, cfgFile)
	if err != nil {
		return nil, nil, err
	}

	database, err := db.Open(bootstrap.DBPath())
	if err != nil {
		return nil, nil, err
	}

	return &bootstrapState{
		Bootstrap:  bootstrap,
		ConfigPath: configPath,
	}, database, nil
}

func openDB() (*config.Config, *sql.DB, error) {
	state, database, err := openBootstrapDB()
	if err != nil {
		return nil, nil, err
	}

	settings, err := db.LoadSettings(database)
	if err != nil {
		database.Close()
		if errors.Is(err, db.ErrSettingsNotConfigured) {
			return nil, nil, fmt.Errorf("settings not configured — run `winnow init` or `winnow import-config`")
		}
		return nil, nil, err
	}

	if err := settings.Validate(); err != nil {
		database.Close()
		return nil, nil, fmt.Errorf("invalid config: %w", err)
	}

	cfg := &config.Config{
		RawDir:         settings.RawDir,
		CleanDir:       settings.CleanDir,
		TrashDir:       settings.TrashDir,
		DataDir:        state.Bootstrap.DataDir,
		PreProcessHook: settings.PreProcessHook,
		Reconcile:      settings.Reconcile,
		Organize:       settings.Organize,
	}
	return cfg, database, nil
}
