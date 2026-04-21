package db

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestOpenCreatesDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "sub", "winnow.db")

	database, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// Verify the file was created.
	if database == nil {
		t.Fatal("expected non-nil database")
	}
}

func TestOpenWALMode(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "winnow.db")

	database, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	var mode string
	err = database.QueryRow("PRAGMA journal_mode").Scan(&mode)
	if err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
}

func TestCoreTables(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "winnow.db")

	database, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	tables := []string{"files", "directories", "operations", "process_errors", "settings"}
	for _, table := range tables {
		t.Run(table, func(t *testing.T) {
			var name string
			err := database.QueryRow(
				"SELECT name FROM sqlite_master WHERE type='table' AND name=?",
				table,
			).Scan(&name)
			if err == sql.ErrNoRows {
				t.Fatalf("table %q does not exist", table)
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCoreTableColumns(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "winnow.db")

	database, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	tests := []struct {
		table   string
		columns []string
	}{
		{"files", []string{"id", "store", "path", "size", "mod_time", "found_at", "reconciled_at", "missing", "sha256", "hashed_at"}},
		{"directories", []string{"id", "store", "path", "file_count", "total_size"}},
		{"operations", []string{"id", "file_id", "dir_id", "src_store", "src_path", "dst_store", "dst_path", "rule", "reason", "executed_at"}},
		{"process_errors", []string{"id", "file_id", "dir_id", "enricher", "rule", "error", "occurred_at"}},
		{"settings", []string{"id", "raw_dir", "clean_dir", "trash_dir", "pre_process_hook", "reconcile_max_staleness", "organize_timezone"}},
	}

	for _, tt := range tests {
		t.Run(tt.table, func(t *testing.T) {
			cols := tableColumns(t, database, tt.table)
			if len(cols) != len(tt.columns) {
				t.Fatalf("table %q has %d columns, want %d: got %v", tt.table, len(cols), len(tt.columns), cols)
			}
			for i, want := range tt.columns {
				if cols[i] != want {
					t.Errorf("column %d = %q, want %q", i, cols[i], want)
				}
			}
		})
	}
}

func TestGetStatsEmpty(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "winnow.db")

	database, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	stats, err := GetStats(database)
	if err != nil {
		t.Fatal(err)
	}

	if stats.Files != 0 || stats.Directories != 0 || stats.Missing != 0 || stats.Operations != 0 || stats.Errors != 0 {
		t.Errorf("expected all zeros, got %+v", stats)
	}
}

func TestOpenIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "winnow.db")

	// Open twice — second open should succeed (CREATE TABLE IF NOT EXISTS).
	db1, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db1.Close()

	db2, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db2.Close()
}

func tableColumns(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull int
		var dfltValue *string
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dfltValue, &pk); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, name)
	}
	return cols
}
