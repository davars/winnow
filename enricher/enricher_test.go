package enricher

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/davars/winnow/config"
	"github.com/davars/winnow/db"
	"github.com/davars/winnow/sha256"
	"github.com/davars/winnow/walk"
	"github.com/davars/winnow/worker"
)

// testSetup creates temp dirs, an on-disk DB, and a config.
func testSetup(t *testing.T) (*sql.DB, *config.Config) {
	t.Helper()
	tmp := t.TempDir()

	rawDir := filepath.Join(tmp, "raw")
	cleanDir := filepath.Join(tmp, "clean")
	trashDir := filepath.Join(tmp, "trash")
	dataDir := filepath.Join(tmp, "data")

	for _, d := range []string{rawDir, cleanDir, trashDir, dataDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	cfg := &config.Config{
		RawDir: rawDir, CleanDir: cleanDir, TrashDir: trashDir, DataDir: dataDir,
	}

	database, err := db.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	return database, cfg
}

func writeFile(t *testing.T, dir, rel string, content []byte) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

// walkHash runs walk + sha256 so enricher tests start with a populated
// files table including content hashes.
func walkHash(t *testing.T, database *sql.DB, cfg *config.Config) {
	t.Helper()
	ctx := context.Background()
	if _, err := walk.Run(ctx, database, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := sha256.Run(ctx, database, cfg.Stores(), worker.Opts{Workers: 1}); err != nil {
		t.Fatal(err)
	}
}

func walkHashMime(t *testing.T, database *sql.DB, cfg *config.Config) {
	t.Helper()
	walkHash(t, database, cfg)
	if _, _, err := Run(context.Background(), database, Mime{}, cfg.Stores(), worker.Opts{Workers: 1}); err != nil {
		t.Fatal(err)
	}
}

func TestSetupSchemaCreatesBaseTableAndColumns(t *testing.T) {
	database, _ := testSetup(t)

	if err := SetupSchema(database, EXIF{}); err != nil {
		t.Fatal(err)
	}

	// Base columns.
	baseCols := []string{"hash", "file_id", "processed_at"}
	for _, col := range baseCols {
		if !columnExists(t, database, "exif", col) {
			t.Errorf("base column %q missing", col)
		}
	}

	// Declared columns.
	for _, col := range []string{"create_date", "make", "model"} {
		if !columnExists(t, database, "exif", col) {
			t.Errorf("declared column %q missing", col)
		}
	}
}

func TestIdentifyByMimeTypePopulatesImageCandidates(t *testing.T) {
	database, cfg := testSetup(t)

	writeFile(t, cfg.RawDir, "photo.jpg", mustHex(t, exifJPEGHex))
	writeFile(t, cfg.RawDir, "notes.txt", []byte("hello\n"))
	writeFile(t, cfg.RawDir, "picture.png", mustHex(t, minimalPNGHex))

	walkHashMime(t, database, cfg)

	n, err := RunIdentify(context.Background(), database, EXIF{})
	if err != nil {
		t.Fatal(err)
	}

	// JPEG + PNG should be identified; text should not.
	if n != 2 {
		t.Errorf("identified = %d, want 2 (jpeg + png)", n)
	}

	// Text file's hash should not appear in exif.
	var textHash string
	if err := database.QueryRow(`SELECT sha256 FROM files WHERE path = 'notes.txt'`).Scan(&textHash); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM exif WHERE hash = ?`, textHash).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Error("non-image should not have been identified")
	}

	// Re-run should be idempotent.
	n2, err := RunIdentify(context.Background(), database, EXIF{})
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		t.Errorf("idempotent identify added %d more rows, want 0", n2)
	}
}

func TestProcessExtractsExifFields(t *testing.T) {
	database, cfg := testSetup(t)

	writeFile(t, cfg.RawDir, "photo.jpg", mustHex(t, exifJPEGHex))
	writeFile(t, cfg.RawDir, "plain.png", mustHex(t, minimalPNGHex))

	walkHashMime(t, database, cfg)

	identified, stats, err := Run(context.Background(), database, EXIF{}, cfg.Stores(), worker.Opts{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if identified != 2 {
		t.Errorf("identified = %d, want 2", identified)
	}
	if stats.Processed != 2 {
		t.Errorf("processed = %d, want 2", stats.Processed)
	}

	// The JPEG has EXIF data embedded (see exifJPEGHex generation).
	row := database.QueryRow(`
		SELECT e.create_date, e.make, e.model, e.processed_at
		FROM exif e JOIN files f ON f.sha256 = e.hash
		WHERE f.path = 'photo.jpg'
	`)
	var createDate, make, model sql.NullString
	var processedAt sql.NullString
	if err := row.Scan(&createDate, &make, &model, &processedAt); err != nil {
		t.Fatal(err)
	}
	if !processedAt.Valid {
		t.Error("processed_at should be set")
	}
	if !make.Valid || make.String != "TestMake" {
		t.Errorf("make = %+v, want TestMake", make)
	}
	if !model.Valid || model.String != "TestModel" {
		t.Errorf("model = %+v, want TestModel", model)
	}
	if !createDate.Valid || createDate.String != "2024:01:15 12:30:45" {
		t.Errorf("create_date = %+v, want 2024:01:15 12:30:45", createDate)
	}

	// The PNG has no EXIF; processed_at still set, fields NULL.
	row = database.QueryRow(`
		SELECT e.create_date, e.make, e.model, e.processed_at
		FROM exif e JOIN files f ON f.sha256 = e.hash
		WHERE f.path = 'plain.png'
	`)
	if err := row.Scan(&createDate, &make, &model, &processedAt); err != nil {
		t.Fatal(err)
	}
	if !processedAt.Valid {
		t.Error("processed_at should be set for PNG")
	}
	if make.Valid || model.Valid || createDate.Valid {
		t.Errorf("plain png should have NULL EXIF fields, got %+v %+v %+v", make, model, createDate)
	}
}

func TestProcessIdempotent(t *testing.T) {
	database, cfg := testSetup(t)

	writeFile(t, cfg.RawDir, "photo.jpg", mustHex(t, exifJPEGHex))
	walkHashMime(t, database, cfg)

	if _, _, err := Run(context.Background(), database, EXIF{}, cfg.Stores(), worker.Opts{Workers: 1}); err != nil {
		t.Fatal(err)
	}

	_, stats, err := Run(context.Background(), database, EXIF{}, cfg.Stores(), worker.Opts{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Processed != 0 {
		t.Errorf("second run processed = %d, want 0", stats.Processed)
	}
}

func TestWriteBatchRejectsUnknownColumn(t *testing.T) {
	database, _ := testSetup(t)
	if err := SetupSchema(database, EXIF{}); err != nil {
		t.Fatal(err)
	}

	src := &source{
		name:    "exif",
		table:   "exif",
		columns: []string{"create_date", "make", "model"},
		stores:  map[string]string{},
	}

	results := []worker.WorkResult{
		{
			Item: worker.WorkItem{Hash: "abc", FileID: 1},
			Values: map[string]any{
				"make":      "x",
				"bogus_col": "nope",
			},
		},
	}
	err := src.WriteBatch(context.Background(), database, results)
	if err == nil {
		t.Fatal("expected error for unknown column, got nil")
	}
}

func columnExists(t *testing.T, database *sql.DB, table, col string) bool {
	t.Helper()
	rows, err := database.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull int
		var dflt *string
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		if name == col {
			return true
		}
	}
	return false
}
