package cmd

import (
	"database/sql"

	"github.com/davars/winnow/config"
	"github.com/davars/winnow/db"
)

// openDB loads the config and opens the database. The caller must close the
// returned *sql.DB when done.
func openDB() (*config.Config, *sql.DB, error) {
	path, err := config.Find(cfgFile)
	if err != nil {
		return nil, nil, err
	}

	cfg, err := config.Load(path)
	if err != nil {
		return nil, nil, err
	}

	database, err := db.Open(cfg.DBPath())
	if err != nil {
		return nil, nil, err
	}

	return cfg, database, nil
}
