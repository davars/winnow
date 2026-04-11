package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Core table DDL. These are hardcoded Layer 1 schemas.
const createTables = `
CREATE TABLE IF NOT EXISTS files (
    id            INTEGER PRIMARY KEY,
    store         TEXT NOT NULL,
    path          TEXT NOT NULL,
    size          INTEGER NOT NULL,
    mod_time      TEXT NOT NULL,
    found_at      TEXT NOT NULL,
    reconciled_at TEXT NOT NULL,
    missing       INTEGER NOT NULL DEFAULT 0,
    UNIQUE(store, path)
);

CREATE TABLE IF NOT EXISTS directories (
    id            INTEGER PRIMARY KEY,
    store         TEXT NOT NULL,
    path          TEXT NOT NULL,
    file_count    INTEGER NOT NULL,
    total_size    INTEGER NOT NULL,
    UNIQUE(store, path)
);

CREATE TABLE IF NOT EXISTS operations (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    file_id     INTEGER REFERENCES files(id),
    dir_id      INTEGER REFERENCES directories(id),
    src_store   TEXT NOT NULL,
    src_path    TEXT NOT NULL,
    dst_store   TEXT,
    dst_path    TEXT,
    rule        TEXT NOT NULL,
    reason      TEXT,
    executed_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS process_errors (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    file_id     INTEGER,
    dir_id      INTEGER,
    enricher    TEXT,
    rule        TEXT,
    error       TEXT NOT NULL,
    occurred_at TEXT NOT NULL
);
`

// filesProvider declares every extension column and index on the files table.
// The db package is the single owner of the files table's schema — no other
// package should declare columns on it. Packages that compute content-derived
// data (sha256, mime) write into these columns but do not manage them.
type filesProvider struct{}

func (filesProvider) Name() string      { return "files" }
func (filesProvider) TableName() string { return "files" }

func (filesProvider) Columns() []Column {
	return []Column{
		{Name: "sha256", Type: "TEXT"},
		{Name: "hashed_at", Type: "TEXT"},
		{Name: "mime_type", Type: "TEXT"},
		{Name: "mime_checked_at", Type: "TEXT"},
	}
}

func (filesProvider) Indexes() []Index {
	return []Index{
		{Name: "files_sha256", Table: "files", Columns: []string{"sha256"}},
	}
}

// Open opens (or creates) the SQLite database at the given path,
// enables WAL mode, and creates the core tables.
func Open(dbPath string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("creating data directory: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// Enable WAL mode for concurrent reads during writes.
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode=WAL").Scan(&mode); err != nil {
		db.Close()
		return nil, fmt.Errorf("enabling WAL mode: %w", err)
	}
	if mode != "wal" {
		db.Close()
		return nil, fmt.Errorf("expected WAL mode, got %q", mode)
	}

	if _, err := db.Exec(createTables); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating tables: %w", err)
	}

	if err := EnsureSchema(db, filesProvider{}); err != nil {
		db.Close()
		return nil, fmt.Errorf("ensuring files schema: %w", err)
	}

	return db, nil
}

// Stats holds summary counts from the database.
type Stats struct {
	Files       int64
	Directories int64
	Missing     int64
	Operations  int64
	Errors      int64
}

// GetStats queries the database for summary statistics.
func GetStats(db *sql.DB) (*Stats, error) {
	var s Stats
	err := db.QueryRow(`
		SELECT
			COALESCE(SUM(CASE WHEN missing = 0 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN missing = 1 THEN 1 ELSE 0 END), 0),
			(SELECT COUNT(*) FROM directories),
			(SELECT COUNT(*) FROM operations),
			(SELECT COUNT(*) FROM process_errors)
		FROM files
	`).Scan(&s.Files, &s.Missing, &s.Directories, &s.Operations, &s.Errors)
	if err != nil {
		return nil, fmt.Errorf("querying stats: %w", err)
	}
	return &s, nil
}
