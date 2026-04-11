package enricher

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/davars/winnow/worker"
)

func getMime(t *testing.T, database *sql.DB, path string) sql.NullString {
	t.Helper()
	var mt sql.NullString
	err := database.QueryRow(`
		SELECT m.mime_type FROM mime m
		JOIN files f ON f.sha256 = m.hash
		WHERE f.store = 'raw' AND f.path = ?
	`, path).Scan(&mt)
	if errors.Is(err, sql.ErrNoRows) {
		return sql.NullString{}
	}
	if err != nil {
		t.Fatal(err)
	}
	return mt
}

func TestMimeDetectsCommonTypes(t *testing.T) {
	database, cfg := testSetup(t)

	writeFile(t, cfg.RawDir, "hello.txt", []byte("hello world\n"))
	writeFile(t, cfg.RawDir, "tiny.png", mustHex(t, minimalPNGHex))
	writeFile(t, cfg.RawDir, "tiny.jpg", mustHex(t, exifJPEGHex))
	writeFile(t, cfg.RawDir, "tiny.pdf", []byte(minimalPDF))

	walkHash(t, database, cfg)

	_, stats, err := Run(context.Background(), database, Mime{}, cfg.Stores(), worker.Opts{Workers: 2})
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
		if !got.Valid || got.String != want {
			t.Errorf("%s: mime_type = %+v, want %q", path, got, want)
		}
	}
}

func TestMimeDedupByHash(t *testing.T) {
	database, cfg := testSetup(t)

	writeFile(t, cfg.RawDir, "a.txt", []byte("same content\n"))
	writeFile(t, cfg.RawDir, "b.txt", []byte("same content\n"))

	walkHash(t, database, cfg)

	_, stats, err := Run(context.Background(), database, Mime{}, cfg.Stores(), worker.Opts{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Processed != 1 {
		t.Errorf("Processed = %d, want 1 (two files, one hash)", stats.Processed)
	}

	var rowCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM mime`).Scan(&rowCount); err != nil {
		t.Fatal(err)
	}
	if rowCount != 1 {
		t.Errorf("mime rows = %d, want 1", rowCount)
	}
}

func TestMimeMissingFilesSkipped(t *testing.T) {
	database, cfg := testSetup(t)

	writeFile(t, cfg.RawDir, "present.txt", []byte("hello"))
	writeFile(t, cfg.RawDir, "gone.txt", []byte("bye"))

	walkHash(t, database, cfg)

	if _, err := database.Exec(
		`UPDATE files SET missing = 1 WHERE store = 'raw' AND path = 'gone.txt'`,
	); err != nil {
		t.Fatal(err)
	}

	_, stats, err := Run(context.Background(), database, Mime{}, cfg.Stores(), worker.Opts{Workers: 1})
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

func TestMimeIdempotentRerun(t *testing.T) {
	database, cfg := testSetup(t)

	writeFile(t, cfg.RawDir, "file.txt", []byte("content"))
	walkHash(t, database, cfg)

	if _, _, err := Run(context.Background(), database, Mime{}, cfg.Stores(), worker.Opts{Workers: 1}); err != nil {
		t.Fatal(err)
	}

	_, stats, err := Run(context.Background(), database, Mime{}, cfg.Stores(), worker.Opts{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Processed != 0 {
		t.Errorf("Processed = %d, want 0", stats.Processed)
	}
}

func TestMimeStaleContentRedetected(t *testing.T) {
	database, cfg := testSetup(t)

	writeFile(t, cfg.RawDir, "file", []byte("hello world\n"))
	walkHash(t, database, cfg)
	if _, _, err := Run(context.Background(), database, Mime{}, cfg.Stores(), worker.Opts{Workers: 1}); err != nil {
		t.Fatal(err)
	}

	orig := getMime(t, database, "file")
	if !orig.Valid || orig.String != "text/plain" {
		t.Fatalf("orig mime = %+v, want text/plain", orig)
	}

	// sha256 re-hash detection compares mtime to hashed_at, so the replacement
	// write needs a distinct mtime second for the new content to be picked up.
	time.Sleep(1100 * time.Millisecond)
	writeFile(t, cfg.RawDir, "file", mustHex(t, exifJPEGHex))
	walkHash(t, database, cfg)

	_, stats, err := Run(context.Background(), database, Mime{}, cfg.Stores(), worker.Opts{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Processed != 1 {
		t.Errorf("Processed = %d, want 1 (new content should be re-detected)", stats.Processed)
	}

	got := getMime(t, database, "file")
	if !got.Valid || got.String != "image/jpeg" {
		t.Errorf("new mime = %+v, want image/jpeg", got)
	}
}

func TestMimeErrorOnUnreadableFile(t *testing.T) {
	database, cfg := testSetup(t)

	writeFile(t, cfg.RawDir, "bad", []byte("content"))
	walkHash(t, database, cfg)

	os.Chmod(filepath.Join(cfg.RawDir, "bad"), 0o000)
	t.Cleanup(func() {
		os.Chmod(filepath.Join(cfg.RawDir, "bad"), 0o644)
	})

	_, stats, err := Run(context.Background(), database, Mime{}, cfg.Stores(), worker.Opts{Workers: 1})
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
