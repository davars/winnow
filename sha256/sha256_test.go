package sha256

import (
	"context"
	"crypto/sha256"
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

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func expectedHash(content string) string {
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:])
}

func TestHashesComputedCorrectly(t *testing.T) {
	database, cfg := testSetup(t)

	writeFile(t, cfg.RawDir, "a.txt", "hello")
	writeFile(t, cfg.RawDir, "b.txt", "world")

	if _, err := walk.Run(context.Background(), database, cfg); err != nil {
		t.Fatal(err)
	}

	stats, err := Run(context.Background(), database, cfg.Stores(), worker.Opts{Workers: 2})
	if err != nil {
		t.Fatal(err)
	}

	if stats.Processed != 2 {
		t.Errorf("Processed = %d, want 2", stats.Processed)
	}
	if stats.Errors != 0 {
		t.Errorf("Errors = %d, want 0", stats.Errors)
	}

	var hash string
	if err := database.QueryRow(
		`SELECT sha256 FROM files WHERE store = 'raw' AND path = 'a.txt'`,
	).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash != expectedHash("hello") {
		t.Errorf("hash for a.txt = %s, want %s", hash, expectedHash("hello"))
	}

	if err := database.QueryRow(
		`SELECT sha256 FROM files WHERE store = 'raw' AND path = 'b.txt'`,
	).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash != expectedHash("world") {
		t.Errorf("hash for b.txt = %s, want %s", hash, expectedHash("world"))
	}

	// Verify hashed_at is set.
	var hashedAt string
	if err := database.QueryRow(
		`SELECT hashed_at FROM files WHERE store = 'raw' AND path = 'a.txt'`,
	).Scan(&hashedAt); err != nil {
		t.Fatal(err)
	}
	if hashedAt == "" {
		t.Error("hashed_at should be set")
	}
}

func TestMissingFilesSkipped(t *testing.T) {
	database, cfg := testSetup(t)

	writeFile(t, cfg.RawDir, "present.txt", "here")
	writeFile(t, cfg.RawDir, "gone.txt", "bye")

	if _, err := walk.Run(context.Background(), database, cfg); err != nil {
		t.Fatal(err)
	}

	// Mark one file as missing.
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

	// The missing file should have no hash.
	var hash sql.NullString
	if err := database.QueryRow(
		`SELECT sha256 FROM files WHERE store = 'raw' AND path = 'gone.txt'`,
	).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash.Valid {
		t.Error("missing file should not have been hashed")
	}
}

func TestStaleHashRecomputed(t *testing.T) {
	database, cfg := testSetup(t)

	writeFile(t, cfg.RawDir, "file.txt", "original")

	if _, err := walk.Run(context.Background(), database, cfg); err != nil {
		t.Fatal(err)
	}

	// First hash.
	if _, err := Run(context.Background(), database, cfg.Stores(), worker.Opts{Workers: 1}); err != nil {
		t.Fatal(err)
	}

	var origHash string
	if err := database.QueryRow(
		`SELECT sha256 FROM files WHERE store = 'raw' AND path = 'file.txt'`,
	).Scan(&origHash); err != nil {
		t.Fatal(err)
	}

	// Wait, modify file, re-walk to update mod_time.
	time.Sleep(1100 * time.Millisecond)
	writeFile(t, cfg.RawDir, "file.txt", "modified content")

	if _, err := walk.Run(context.Background(), database, cfg); err != nil {
		t.Fatal(err)
	}

	// Verify mod_time > hashed_at (staleness condition).
	var modTime, hashedAt string
	if err := database.QueryRow(
		`SELECT mod_time, hashed_at FROM files WHERE store = 'raw' AND path = 'file.txt'`,
	).Scan(&modTime, &hashedAt); err != nil {
		t.Fatal(err)
	}
	if modTime <= hashedAt {
		t.Fatalf("expected mod_time > hashed_at after file change: mod_time=%s, hashed_at=%s", modTime, hashedAt)
	}

	// Re-hash — should pick up the changed file.
	stats, err := Run(context.Background(), database, cfg.Stores(), worker.Opts{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}

	if stats.Processed != 1 {
		t.Errorf("Processed = %d, want 1 (stale hash should be recomputed)", stats.Processed)
	}

	var newHash string
	if err := database.QueryRow(
		`SELECT sha256 FROM files WHERE store = 'raw' AND path = 'file.txt'`,
	).Scan(&newHash); err != nil {
		t.Fatal(err)
	}

	if newHash == origHash {
		t.Error("hash should have changed after file modification")
	}
	if newHash != expectedHash("modified content") {
		t.Errorf("new hash = %s, want %s", newHash, expectedHash("modified content"))
	}
}

func TestIdempotentRerun(t *testing.T) {
	database, cfg := testSetup(t)

	writeFile(t, cfg.RawDir, "file.txt", "content")

	if _, err := walk.Run(context.Background(), database, cfg); err != nil {
		t.Fatal(err)
	}

	// First run.
	if _, err := Run(context.Background(), database, cfg.Stores(), worker.Opts{Workers: 1}); err != nil {
		t.Fatal(err)
	}

	// Second run — nothing to do.
	stats, err := Run(context.Background(), database, cfg.Stores(), worker.Opts{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}

	if stats.Processed != 0 {
		t.Errorf("Processed = %d, want 0 (nothing should need hashing)", stats.Processed)
	}
}

func TestErrorOnUnreadableFile(t *testing.T) {
	database, cfg := testSetup(t)

	writeFile(t, cfg.RawDir, "bad.txt", "content")

	if _, err := walk.Run(context.Background(), database, cfg); err != nil {
		t.Fatal(err)
	}

	// Make the file unreadable.
	os.Chmod(filepath.Join(cfg.RawDir, "bad.txt"), 0o000)
	t.Cleanup(func() {
		os.Chmod(filepath.Join(cfg.RawDir, "bad.txt"), 0o644)
	})

	stats, err := Run(context.Background(), database, cfg.Stores(), worker.Opts{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}

	if stats.Errors != 1 {
		t.Errorf("Errors = %d, want 1", stats.Errors)
	}

	// Error should be logged to process_errors.
	var errCount int
	if err := database.QueryRow(
		`SELECT COUNT(*) FROM process_errors WHERE enricher = 'sha256'`,
	).Scan(&errCount); err != nil {
		t.Fatal(err)
	}
	if errCount != 1 {
		t.Errorf("process_errors count = %d, want 1", errCount)
	}
}
