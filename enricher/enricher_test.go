package enricher

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	for _, col := range []string{"data"} {
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
		SELECT e.data, e.processed_at
		FROM exif e JOIN files f ON f.sha256 = e.hash
		WHERE f.path = 'photo.jpg'
	`)
	var data, processedAt sql.NullString
	if err := row.Scan(&data, &processedAt); err != nil {
		t.Fatal(err)
	}
	if !processedAt.Valid {
		t.Error("processed_at should be set")
	}
	if !data.Valid {
		t.Fatal("data should be set for JPEG with EXIF")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(data.String), &parsed); err != nil {
		t.Fatalf("data is not valid JSON: %v (raw: %q)", err, data.String)
	}
	if parsed["EXIF:CreateDate"] != "2024:01:15 12:30:45" {
		t.Errorf("EXIF:CreateDate = %v, want 2024:01:15 12:30:45", parsed["EXIF:CreateDate"])
	}
	// System/File/ExifTool groups are excluded at the exiftool level.
	for k := range parsed {
		for _, prefix := range []string{"System:", "File:", "ExifTool:"} {
			if strings.HasPrefix(k, prefix) {
				t.Errorf("excluded group leaked through: %q", k)
			}
		}
	}

	// The PNG has no EXIF:* timestamps but exiftool still reports PNG:*
	// container metadata (compression, color type, etc.).
	row = database.QueryRow(`
		SELECT e.data, e.processed_at
		FROM exif e JOIN files f ON f.sha256 = e.hash
		WHERE f.path = 'plain.png'
	`)
	if err := row.Scan(&data, &processedAt); err != nil {
		t.Fatal(err)
	}
	if !processedAt.Valid {
		t.Error("processed_at should be set for PNG")
	}
	if !data.Valid {
		t.Fatal("PNG data should be set (PNG:* tags are always reported)")
	}
	parsed = nil
	if err := json.Unmarshal([]byte(data.String), &parsed); err != nil {
		t.Fatalf("PNG data is not valid JSON: %v (raw: %q)", err, data.String)
	}
	if _, hasDate := parsed["EXIF:CreateDate"]; hasDate {
		t.Errorf("PNG should not have EXIF:CreateDate, got %v", parsed["EXIF:CreateDate"])
	}
	if parsed["PNG:Compression"] == nil {
		t.Errorf("PNG should have PNG:Compression, got data: %q", data.String)
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

func TestIdentifyResetsStaleTagsVersion(t *testing.T) {
	database, cfg := testSetup(t)

	writeFile(t, cfg.RawDir, "photo.jpg", mustHex(t, exifJPEGHex))
	walkHashMime(t, database, cfg)

	if _, _, err := Run(context.Background(), database, EXIF{}, cfg.Stores(), worker.Opts{Workers: 1}); err != nil {
		t.Fatal(err)
	}

	var firstProcessed, firstVersion sql.NullString
	if err := database.QueryRow(
		`SELECT processed_at, tags_version FROM exif`).Scan(&firstProcessed, &firstVersion); err != nil {
		t.Fatal(err)
	}
	if !firstProcessed.Valid || !firstVersion.Valid {
		t.Fatalf("expected processed_at and tags_version set, got %v %v", firstProcessed, firstVersion)
	}

	// Simulate a prior run with a different tag set.
	if _, err := database.Exec(`UPDATE exif SET tags_version = 'stale'`); err != nil {
		t.Fatal(err)
	}

	if _, err := RunIdentify(context.Background(), database, EXIF{}); err != nil {
		t.Fatal(err)
	}

	var processedAfterReset sql.NullString
	if err := database.QueryRow(`SELECT processed_at FROM exif`).Scan(&processedAfterReset); err != nil {
		t.Fatal(err)
	}
	if processedAfterReset.Valid {
		t.Errorf("processed_at should be NULL after stale version reset, got %q", processedAfterReset.String)
	}

	if _, err := RunProcess(context.Background(), database, EXIF{}, cfg.Stores(), worker.Opts{Workers: 1}); err != nil {
		t.Fatal(err)
	}

	var finalVersion sql.NullString
	if err := database.QueryRow(`SELECT tags_version FROM exif`).Scan(&finalVersion); err != nil {
		t.Fatal(err)
	}
	if finalVersion.String != firstVersion.String {
		t.Errorf("tags_version = %q, want %q", finalVersion.String, firstVersion.String)
	}
}

func TestIdentifyResetsNullTagsVersion(t *testing.T) {
	database, cfg := testSetup(t)

	writeFile(t, cfg.RawDir, "photo.jpg", mustHex(t, exifJPEGHex))
	walkHashMime(t, database, cfg)

	if _, _, err := Run(context.Background(), database, EXIF{}, cfg.Stores(), worker.Opts{Workers: 1}); err != nil {
		t.Fatal(err)
	}

	// Simulate pre-versioning rows: processed_at set, tags_version NULL.
	if _, err := database.Exec(`UPDATE exif SET tags_version = NULL`); err != nil {
		t.Fatal(err)
	}

	if _, err := RunIdentify(context.Background(), database, EXIF{}); err != nil {
		t.Fatal(err)
	}

	var processedAt sql.NullString
	if err := database.QueryRow(`SELECT processed_at FROM exif`).Scan(&processedAt); err != nil {
		t.Fatal(err)
	}
	if processedAt.Valid {
		t.Errorf("processed_at should be NULL after pre-versioning row reset, got %q", processedAt.String)
	}
}

func TestIdentifySkipsCurrentVersion(t *testing.T) {
	database, cfg := testSetup(t)

	writeFile(t, cfg.RawDir, "photo.jpg", mustHex(t, exifJPEGHex))
	walkHashMime(t, database, cfg)

	if _, _, err := Run(context.Background(), database, EXIF{}, cfg.Stores(), worker.Opts{Workers: 1}); err != nil {
		t.Fatal(err)
	}

	var beforeProcessed sql.NullString
	if err := database.QueryRow(`SELECT processed_at FROM exif`).Scan(&beforeProcessed); err != nil {
		t.Fatal(err)
	}

	if _, err := RunIdentify(context.Background(), database, EXIF{}); err != nil {
		t.Fatal(err)
	}

	var afterProcessed sql.NullString
	if err := database.QueryRow(`SELECT processed_at FROM exif`).Scan(&afterProcessed); err != nil {
		t.Fatal(err)
	}
	if afterProcessed.String != beforeProcessed.String {
		t.Errorf("processed_at changed on same-version re-identify: before=%q after=%q",
			beforeProcessed.String, afterProcessed.String)
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
		columns: []string{"data"},
		stores:  map[string]string{},
	}

	results := []worker.WorkResult{
		{
			Item: worker.WorkItem{Hash: "abc", FileID: 1},
			Values: map[string]any{
				"data":      "{}",
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
