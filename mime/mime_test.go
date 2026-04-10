package mime

import (
	"context"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/davars/winnow/config"
	"github.com/davars/winnow/db"
	"github.com/davars/winnow/walk"
	"github.com/davars/winnow/worker"
)

// Minimal valid PNG (67 bytes: signature + IHDR + IDAT + IEND).
const minimalPNGHex = "89504e470d0a1a0a0000000d49484452000000010000000108060000001f15c4890000000d4944415478da6300010000000500010d0a2db40000000049454e44ae426082"

const minimalJPEGHex = "ffd8ffe000104a46494600010100000100010000ffd9"
const minimalPDF = "%PDF-1.4\n%%EOF\n"

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
		RawDir:   rawDir,
		CleanDir: cleanDir,
		TrashDir: trashDir,
		DataDir:  dataDir,
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

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func getMime(t *testing.T, database *sql.DB, path string) sql.NullString {
	t.Helper()
	var mt sql.NullString
	err := database.QueryRow(
		`SELECT mime_type FROM files WHERE store = 'raw' AND path = ?`, path,
	).Scan(&mt)
	if err != nil {
		t.Fatal(err)
	}
	return mt
}

func TestDetectsCommonTypes(t *testing.T) {
	database, cfg := testSetup(t)

	writeFile(t, cfg.RawDir, "hello.txt", []byte("hello world\n"))
	writeFile(t, cfg.RawDir, "tiny.png", mustHex(t, minimalPNGHex))
	writeFile(t, cfg.RawDir, "tiny.jpg", mustHex(t, minimalJPEGHex))
	writeFile(t, cfg.RawDir, "tiny.pdf", []byte(minimalPDF))

	if _, err := walk.Run(context.Background(), database, cfg); err != nil {
		t.Fatal(err)
	}

	stats, err := Run(context.Background(), database, cfg.Stores(), worker.Opts{Workers: 2})
	if err != nil {
		t.Fatal(err)
	}

	if stats.Processed != 4 {
		t.Errorf("Processed = %d, want 4", stats.Processed)
	}
	if stats.Errors != 0 {
		t.Errorf("Errors = %d, want 0", stats.Errors)
	}

	cases := map[string]string{
		"hello.txt": "text/plain",
		"tiny.png":  "image/png",
		"tiny.jpg":  "image/jpeg",
		"tiny.pdf":  "application/pdf",
	}
	for path, want := range cases {
		got := getMime(t, database, path)
		if !got.Valid {
			t.Errorf("%s: mime_type is NULL, want %q", path, want)
			continue
		}
		if got.String != want {
			t.Errorf("%s: mime_type = %q, want %q", path, got.String, want)
		}
	}
}

func TestMissingFilesSkipped(t *testing.T) {
	database, cfg := testSetup(t)

	writeFile(t, cfg.RawDir, "present.txt", []byte("hello"))
	writeFile(t, cfg.RawDir, "gone.txt", []byte("bye"))

	if _, err := walk.Run(context.Background(), database, cfg); err != nil {
		t.Fatal(err)
	}

	if _, err := database.Exec(
		`UPDATE files SET missing = 1 WHERE store = 'raw' AND path = 'gone.txt'`,
	); err != nil {
		t.Fatal(err)
	}

	stats, err := Run(context.Background(), database, cfg.Stores(), worker.Opts{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}

	if stats.Processed != 1 {
		t.Errorf("Processed = %d, want 1 (missing file should be skipped)", stats.Processed)
	}

	got := getMime(t, database, "gone.txt")
	if got.Valid {
		t.Error("missing file should not have been probed")
	}
}

func TestIdempotentRerun(t *testing.T) {
	database, cfg := testSetup(t)

	writeFile(t, cfg.RawDir, "file.txt", []byte("content"))

	if _, err := walk.Run(context.Background(), database, cfg); err != nil {
		t.Fatal(err)
	}

	if _, err := Run(context.Background(), database, cfg.Stores(), worker.Opts{Workers: 1}); err != nil {
		t.Fatal(err)
	}

	stats, err := Run(context.Background(), database, cfg.Stores(), worker.Opts{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}

	if stats.Processed != 0 {
		t.Errorf("Processed = %d, want 0 (nothing should need detection)", stats.Processed)
	}
}

func TestStaleDetectionRecomputed(t *testing.T) {
	database, cfg := testSetup(t)

	writeFile(t, cfg.RawDir, "file", []byte("hello world\n"))

	if _, err := walk.Run(context.Background(), database, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), database, cfg.Stores(), worker.Opts{Workers: 1}); err != nil {
		t.Fatal(err)
	}

	orig := getMime(t, database, "file")
	if !orig.Valid || orig.String != "text/plain" {
		t.Fatalf("orig mime = %+v, want text/plain", orig)
	}

	// Replace content with a JPEG; re-walk to bump mod_time.
	time.Sleep(1100 * time.Millisecond)
	writeFile(t, cfg.RawDir, "file", mustHex(t, minimalJPEGHex))

	if _, err := walk.Run(context.Background(), database, cfg); err != nil {
		t.Fatal(err)
	}

	stats, err := Run(context.Background(), database, cfg.Stores(), worker.Opts{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Processed != 1 {
		t.Errorf("Processed = %d, want 1 (stale detection should be redone)", stats.Processed)
	}

	got := getMime(t, database, "file")
	if !got.Valid || got.String != "image/jpeg" {
		t.Errorf("new mime = %+v, want image/jpeg", got)
	}
}

func TestErrorOnUnreadableFile(t *testing.T) {
	database, cfg := testSetup(t)

	writeFile(t, cfg.RawDir, "bad", []byte("content"))

	if _, err := walk.Run(context.Background(), database, cfg); err != nil {
		t.Fatal(err)
	}

	os.Chmod(filepath.Join(cfg.RawDir, "bad"), 0o000)
	t.Cleanup(func() {
		os.Chmod(filepath.Join(cfg.RawDir, "bad"), 0o644)
	})

	stats, err := Run(context.Background(), database, cfg.Stores(), worker.Opts{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}

	if stats.Errors != 1 {
		t.Errorf("Errors = %d, want 1", stats.Errors)
	}

	var errCount int
	if err := database.QueryRow(
		`SELECT COUNT(*) FROM process_errors WHERE enricher = 'mime'`,
	).Scan(&errCount); err != nil {
		t.Fatal(err)
	}
	if errCount != 1 {
		t.Errorf("process_errors count = %d, want 1", errCount)
	}

	got := getMime(t, database, "bad")
	if got.Valid {
		t.Error("mime_type should be NULL on error")
	}
}
