package organize

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/davars/winnow/config"
	"github.com/davars/winnow/db"
	"github.com/davars/winnow/enricher"
)

func testSetup(t *testing.T) (*sql.DB, *config.Config) {
	t.Helper()
	tmp := t.TempDir()
	cfg := &config.Config{
		RawDir:   filepath.Join(tmp, "raw"),
		CleanDir: filepath.Join(tmp, "clean"),
		TrashDir: filepath.Join(tmp, "trash"),
		DataDir:  filepath.Join(tmp, "data"),
		Organize: config.OrganizeConfig{Timezone: "UTC"},
	}
	for _, d := range []string{cfg.RawDir, cfg.CleanDir, cfg.TrashDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	database, err := db.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	if err := enricher.SetupSchema(database, enricher.Mime{}); err != nil {
		t.Fatal(err)
	}
	if err := enricher.SetupSchema(database, enricher.EXIF{}); err != nil {
		t.Fatal(err)
	}
	return database, cfg
}

func writeFile(t *testing.T, base, rel, content string) {
	t.Helper()
	path := filepath.Join(base, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// insertFile writes a raw-store files row with the given content hash. Uses
// RFC3339 for mod_time so captureTime's fallback parser accepts it.
func insertFile(t *testing.T, database *sql.DB, store, path, sha, modTime string) int64 {
	t.Helper()
	res, err := database.Exec(`
		INSERT INTO files (store, path, size, mod_time, found_at, reconciled_at, missing, sha256, hashed_at)
		VALUES (?, ?, 10, ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 0, ?, '2026-01-01T00:00:00Z')
	`, store, path, modTime, sha)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func insertMime(t *testing.T, database *sql.DB, hash string, fileID int64, mimeType string) {
	t.Helper()
	_, err := database.Exec(`
		INSERT INTO mime (hash, file_id, processed_at, mime_type)
		VALUES (?, ?, '2026-01-01T00:00:00Z', ?)
	`, hash, fileID, mimeType)
	if err != nil {
		t.Fatal(err)
	}
}

func insertExif(t *testing.T, database *sql.DB, hash string, fileID int64, data string) {
	t.Helper()
	_, err := database.Exec(`
		INSERT INTO exif (hash, file_id, processed_at, data, tags_version)
		VALUES (?, ?, '2026-01-01T00:00:00Z', ?, 'test')
	`, hash, fileID, data)
	if err != nil {
		t.Fatal(err)
	}
}

func TestRunDefaultCopyPreservesRaw(t *testing.T) {
	database, cfg := testSetup(t)
	writeFile(t, cfg.RawDir, "DSC_0001.jpg", "photo-bytes")
	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	fileID := insertFile(t, database, "raw", "DSC_0001.jpg", hash, "2020-05-10T14:00:00Z")
	insertMime(t, database, hash, fileID, "image/jpeg")
	insertExif(t, database, hash, fileID,
		`{"EXIF:DateTimeOriginal":"2020:05:10 14:00:00","EXIF:OffsetTimeOriginal":"+00:00"}`)

	stats, err := Run(context.Background(), database, cfg, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Organized != 1 || stats.Errors != 0 {
		t.Fatalf("stats = %+v", stats)
	}

	// Raw original still present.
	if _, err := os.Stat(filepath.Join(cfg.RawDir, "DSC_0001.jpg")); err != nil {
		t.Errorf("raw original missing: %v", err)
	}
	// Clean copy exists at the expected path.
	want := filepath.Join(cfg.CleanDir, "media/2020/05/20200510T140000Z-aaaaaaaa-DSC_0001.jpg")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("clean copy missing at %s: %v", want, err)
	}
	// Raw row still exists, clean row added.
	assertRowCount(t, database, `SELECT COUNT(*) FROM files WHERE store='raw'`, 1)
	assertRowCount(t, database, `SELECT COUNT(*) FROM files WHERE store='clean'`, 1)
	assertRowCount(t, database, `SELECT COUNT(*) FROM operations WHERE rule='organize'`, 1)
}

func TestRunRemoveOriginals(t *testing.T) {
	database, cfg := testSetup(t)
	writeFile(t, cfg.RawDir, "a.jpg", "x")
	hash := strings.Repeat("b", 64)
	fileID := insertFile(t, database, "raw", "a.jpg", hash, "2021-01-02T03:04:05Z")
	insertMime(t, database, hash, fileID, "image/jpeg")

	stats, err := Run(context.Background(), database, cfg, Opts{RemoveOriginals: true})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Organized != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	if _, err := os.Stat(filepath.Join(cfg.RawDir, "a.jpg")); !os.IsNotExist(err) {
		t.Errorf("raw original should have been removed: %v", err)
	}
	assertRowCount(t, database, `SELECT COUNT(*) FROM files WHERE store='raw'`, 0)
	assertRowCount(t, database, `SELECT COUNT(*) FROM files WHERE store='clean'`, 1)
}

func TestRunFallbackToModTime(t *testing.T) {
	database, cfg := testSetup(t)
	writeFile(t, cfg.RawDir, "no_exif.jpg", "x")
	hash := strings.Repeat("c", 64)
	fileID := insertFile(t, database, "raw", "no_exif.jpg", hash, "2019-09-08T07:06:05Z")
	insertMime(t, database, hash, fileID, "image/jpeg")
	// No exif row at all.

	stats, err := Run(context.Background(), database, cfg, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Organized != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	want := filepath.Join(cfg.CleanDir, "media/2019/09")
	entries, err := os.ReadDir(want)
	if err != nil {
		t.Fatalf("read %s: %v", want, err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 entry in %s, got %v", want, entries)
	}

	// Verify reason says mod_time.
	var reason string
	if err := database.QueryRow(
		`SELECT reason FROM operations WHERE rule='organize' LIMIT 1`,
	).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reason, "files.mod_time") {
		t.Errorf("reason = %q, want mention of files.mod_time", reason)
	}
}

func TestRunExifDataNull(t *testing.T) {
	database, cfg := testSetup(t)
	writeFile(t, cfg.RawDir, "null_exif.jpg", "x")
	hash := strings.Repeat("d", 64)
	fileID := insertFile(t, database, "raw", "null_exif.jpg", hash, "2018-07-01T00:00:00Z")
	insertMime(t, database, hash, fileID, "image/jpeg")
	// Insert exif row with NULL data (parse-failure leaves it NULL).
	if _, err := database.Exec(`
		INSERT INTO exif (hash, file_id, processed_at, data, tags_version)
		VALUES (?, ?, '2026-01-01T00:00:00Z', NULL, 'test')
	`, hash, fileID); err != nil {
		t.Fatal(err)
	}

	stats, err := Run(context.Background(), database, cfg, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Organized != 1 || stats.Errors != 0 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestRunIdempotentRerun(t *testing.T) {
	database, cfg := testSetup(t)
	writeFile(t, cfg.RawDir, "idem.jpg", "x")
	hash := strings.Repeat("e", 64)
	fileID := insertFile(t, database, "raw", "idem.jpg", hash, "2020-05-10T14:00:00Z")
	insertMime(t, database, hash, fileID, "image/jpeg")

	if _, err := Run(context.Background(), database, cfg, Opts{}); err != nil {
		t.Fatal(err)
	}

	stats, err := Run(context.Background(), database, cfg, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Organized != 0 || stats.Skipped != 1 {
		t.Errorf("stats = %+v, want Organized=0 Skipped=1", stats)
	}
}

func TestRunDryRunNoChanges(t *testing.T) {
	database, cfg := testSetup(t)
	writeFile(t, cfg.RawDir, "dry.jpg", "x")
	hash := strings.Repeat("f", 64)
	fileID := insertFile(t, database, "raw", "dry.jpg", hash, "2020-05-10T14:00:00Z")
	insertMime(t, database, hash, fileID, "image/jpeg")

	stats, err := Run(context.Background(), database, cfg, Opts{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Organized != 0 {
		t.Errorf("dry-run should not count as Organized: %+v", stats)
	}
	// No FS changes.
	if _, err := os.Stat(filepath.Join(cfg.CleanDir, "media")); !os.IsNotExist(err) {
		t.Errorf("clean/media should not exist after dry-run")
	}
	// No DB changes.
	assertRowCount(t, database, `SELECT COUNT(*) FROM files WHERE store='clean'`, 0)
	assertRowCount(t, database, `SELECT COUNT(*) FROM operations`, 0)
}

func TestRunNonMediaExcluded(t *testing.T) {
	database, cfg := testSetup(t)
	writeFile(t, cfg.RawDir, "doc.txt", "x")
	hash := strings.Repeat("1", 64)
	fileID := insertFile(t, database, "raw", "doc.txt", hash, "2020-05-10T14:00:00Z")
	insertMime(t, database, hash, fileID, "text/plain")

	stats, err := Run(context.Background(), database, cfg, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Considered != 0 {
		t.Errorf("Considered = %d, want 0 (text/plain not in EXIFMimeTypes)", stats.Considered)
	}
}

func TestRunMissingMimeTableErrors(t *testing.T) {
	tmp := t.TempDir()
	cfg := &config.Config{
		RawDir:   filepath.Join(tmp, "raw"),
		CleanDir: filepath.Join(tmp, "clean"),
		TrashDir: filepath.Join(tmp, "trash"),
		DataDir:  filepath.Join(tmp, "data"),
		Organize: config.OrganizeConfig{Timezone: "UTC"},
	}
	for _, d := range []string{cfg.RawDir, cfg.CleanDir, cfg.TrashDir} {
		_ = os.MkdirAll(d, 0o755)
	}
	database, err := db.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// Don't set up the mime enricher table.
	_, err = Run(context.Background(), database, cfg, Opts{})
	if err == nil {
		t.Fatal("expected error when mime table missing")
	}
	if !strings.Contains(err.Error(), "mime") {
		t.Errorf("error = %v, want mention of mime", err)
	}
}

func TestRunCollision(t *testing.T) {
	database, cfg := testSetup(t)
	writeFile(t, cfg.RawDir, "collide.jpg", "x")
	hash := strings.Repeat("2", 64)
	fileID := insertFile(t, database, "raw", "collide.jpg", hash, "2020-05-10T14:00:00Z")
	insertMime(t, database, hash, fileID, "image/jpeg")

	// Pre-create the destination path — note hash prefix "22222222".
	destRel := "media/2020/05/20200510T140000Z-22222222-collide.jpg"
	dest := filepath.Join(cfg.CleanDir, destRel)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("prior"), 0o644); err != nil {
		t.Fatal(err)
	}

	stats, err := Run(context.Background(), database, cfg, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Collided != 1 || stats.Organized != 0 {
		t.Errorf("stats = %+v, want Collided=1 Organized=0", stats)
	}
}

func TestRunPerFileErrorContinues(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod semantics differ on windows")
	}
	if os.Getuid() == 0 {
		t.Skip("root bypasses read-permission errors")
	}
	database, cfg := testSetup(t)

	// File 1: unreadable source (chmod 000).
	writeFile(t, cfg.RawDir, "unreadable.jpg", "x")
	hash1 := strings.Repeat("3", 64)
	id1 := insertFile(t, database, "raw", "unreadable.jpg", hash1, "2020-05-10T14:00:00Z")
	insertMime(t, database, hash1, id1, "image/jpeg")
	if err := os.Chmod(filepath.Join(cfg.RawDir, "unreadable.jpg"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(cfg.RawDir, "unreadable.jpg"), 0o644) })

	// File 2: fine.
	writeFile(t, cfg.RawDir, "ok.jpg", "x")
	hash2 := strings.Repeat("4", 64)
	id2 := insertFile(t, database, "raw", "ok.jpg", hash2, "2020-06-10T14:00:00Z")
	insertMime(t, database, hash2, id2, "image/jpeg")

	stats, err := Run(context.Background(), database, cfg, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Organized != 1 || stats.Errors != 1 {
		t.Errorf("stats = %+v, want Organized=1 Errors=1", stats)
	}
	assertRowCount(t, database, `SELECT COUNT(*) FROM process_errors WHERE rule='organize'`, 1)
}

func TestRunMissingTimezoneErrors(t *testing.T) {
	database, cfg := testSetup(t)
	cfg.Organize.Timezone = ""
	_, err := Run(context.Background(), database, cfg, Opts{})
	if err == nil {
		t.Fatal("expected error for missing organize.timezone")
	}
}

func TestRunInvalidTimezoneErrors(t *testing.T) {
	database, cfg := testSetup(t)
	cfg.Organize.Timezone = "Bogus/Place"
	_, err := Run(context.Background(), database, cfg, Opts{})
	if err == nil {
		t.Fatal("expected error for invalid organize.timezone")
	}
}

func assertRowCount(t *testing.T, database *sql.DB, query string, want int) {
	t.Helper()
	var got int
	if err := database.QueryRow(query).Scan(&got); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	if got != want {
		t.Errorf("query %q: got %d, want %d", query, got, want)
	}
}

