package walk

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/davars/winnow/config"
	"github.com/davars/winnow/db"
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

func TestWalkInsertsNewFiles(t *testing.T) {
	database, cfg := testSetup(t)

	writeFile(t, cfg.RawDir, "photo.jpg", "fake jpeg")
	writeFile(t, cfg.RawDir, "sub/doc.txt", "hello")

	stats, err := Run(context.Background(), database, cfg)
	if err != nil {
		t.Fatal(err)
	}

	if stats.FilesFound != 2 {
		t.Errorf("FilesFound = %d, want 2", stats.FilesFound)
	}

	// Verify files are in the DB.
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM files WHERE store = 'raw'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("files in DB = %d, want 2", count)
	}

	// Verify paths are relative.
	var path string
	if err := database.QueryRow(`SELECT path FROM files WHERE store = 'raw' AND path = 'photo.jpg'`).Scan(&path); err != nil {
		t.Fatalf("expected photo.jpg in DB: %v", err)
	}
}

func TestWalkUpdatesReconciledAt(t *testing.T) {
	database, cfg := testSetup(t)

	writeFile(t, cfg.RawDir, "file.txt", "content")

	// First walk.
	if _, err := Run(context.Background(), database, cfg); err != nil {
		t.Fatal(err)
	}

	var firstReconciled string
	if err := database.QueryRow(
		`SELECT reconciled_at FROM files WHERE store = 'raw' AND path = 'file.txt'`,
	).Scan(&firstReconciled); err != nil {
		t.Fatal(err)
	}

	// Wait to ensure time advances.
	time.Sleep(1100 * time.Millisecond)

	// Second walk.
	if _, err := Run(context.Background(), database, cfg); err != nil {
		t.Fatal(err)
	}

	var secondReconciled string
	if err := database.QueryRow(
		`SELECT reconciled_at FROM files WHERE store = 'raw' AND path = 'file.txt'`,
	).Scan(&secondReconciled); err != nil {
		t.Fatal(err)
	}

	if secondReconciled <= firstReconciled {
		t.Errorf("reconciled_at not updated: first=%s, second=%s", firstReconciled, secondReconciled)
	}

	// found_at should remain the same as the first walk's timestamp.
	var foundAt string
	if err := database.QueryRow(
		`SELECT found_at FROM files WHERE store = 'raw' AND path = 'file.txt'`,
	).Scan(&foundAt); err != nil {
		t.Fatal(err)
	}
	if foundAt != firstReconciled {
		t.Errorf("found_at changed: got %s, want %s", foundAt, firstReconciled)
	}
}

func TestWalkUpdatesSizeAndModTime(t *testing.T) {
	database, cfg := testSetup(t)

	writeFile(t, cfg.RawDir, "file.txt", "short")

	if _, err := Run(context.Background(), database, cfg); err != nil {
		t.Fatal(err)
	}

	var origSize int64
	var origModTime string
	if err := database.QueryRow(
		`SELECT size, mod_time FROM files WHERE store = 'raw' AND path = 'file.txt'`,
	).Scan(&origSize, &origModTime); err != nil {
		t.Fatal(err)
	}

	// Wait, then modify the file.
	time.Sleep(1100 * time.Millisecond)
	writeFile(t, cfg.RawDir, "file.txt", "much longer content now")

	if _, err := Run(context.Background(), database, cfg); err != nil {
		t.Fatal(err)
	}

	var newSize int64
	var newModTime string
	if err := database.QueryRow(
		`SELECT size, mod_time FROM files WHERE store = 'raw' AND path = 'file.txt'`,
	).Scan(&newSize, &newModTime); err != nil {
		t.Fatal(err)
	}

	if newSize <= origSize {
		t.Errorf("size not updated: orig=%d, new=%d", origSize, newSize)
	}
	if newModTime <= origModTime {
		t.Errorf("mod_time not updated: orig=%s, new=%s", origModTime, newModTime)
	}
}

func TestWalkMultipleStores(t *testing.T) {
	database, cfg := testSetup(t)

	writeFile(t, cfg.RawDir, "raw_file.txt", "raw")
	writeFile(t, cfg.CleanDir, "clean_file.txt", "clean")

	stats, err := Run(context.Background(), database, cfg)
	if err != nil {
		t.Fatal(err)
	}

	if stats.FilesFound != 2 {
		t.Errorf("FilesFound = %d, want 2", stats.FilesFound)
	}

	var rawCount, cleanCount int
	database.QueryRow(`SELECT COUNT(*) FROM files WHERE store = 'raw'`).Scan(&rawCount)
	database.QueryRow(`SELECT COUNT(*) FROM files WHERE store = 'clean'`).Scan(&cleanCount)

	if rawCount != 1 {
		t.Errorf("raw files = %d, want 1", rawCount)
	}
	if cleanCount != 1 {
		t.Errorf("clean files = %d, want 1", cleanCount)
	}
}

func TestDirectoryStats(t *testing.T) {
	database, cfg := testSetup(t)

	writeFile(t, cfg.RawDir, "a.txt", "hello")       // 5 bytes
	writeFile(t, cfg.RawDir, "sub/b.txt", "world!")   // 6 bytes
	writeFile(t, cfg.RawDir, "sub/c.txt", "test")     // 4 bytes

	if _, err := Run(context.Background(), database, cfg); err != nil {
		t.Fatal(err)
	}

	// Root directory "." should have 3 files, total 15 bytes.
	var rootCount, rootSize int64
	if err := database.QueryRow(
		`SELECT file_count, total_size FROM directories WHERE store = 'raw' AND path = '.'`,
	).Scan(&rootCount, &rootSize); err != nil {
		t.Fatalf("querying root dir: %v", err)
	}
	if rootCount != 3 {
		t.Errorf("root file_count = %d, want 3", rootCount)
	}
	if rootSize != 15 {
		t.Errorf("root total_size = %d, want 15", rootSize)
	}

	// "sub" directory should have 2 files, total 10 bytes.
	var subCount, subSize int64
	if err := database.QueryRow(
		`SELECT file_count, total_size FROM directories WHERE store = 'raw' AND path = 'sub'`,
	).Scan(&subCount, &subSize); err != nil {
		t.Fatalf("querying sub dir: %v", err)
	}
	if subCount != 2 {
		t.Errorf("sub file_count = %d, want 2", subCount)
	}
	if subSize != 10 {
		t.Errorf("sub total_size = %d, want 10", subSize)
	}
}

func TestStaleDirectoryDeleted(t *testing.T) {
	database, cfg := testSetup(t)

	writeFile(t, cfg.RawDir, "keep/a.txt", "keep")
	writeFile(t, cfg.RawDir, "remove/b.txt", "remove")

	if _, err := Run(context.Background(), database, cfg); err != nil {
		t.Fatal(err)
	}

	// Verify "remove" dir exists.
	var count int
	database.QueryRow(`SELECT COUNT(*) FROM directories WHERE store = 'raw' AND path = 'remove'`).Scan(&count)
	if count != 1 {
		t.Fatalf("expected 'remove' directory in DB, got count=%d", count)
	}

	// Remove the directory and its file from disk.
	os.RemoveAll(filepath.Join(cfg.RawDir, "remove"))

	stats, err := Run(context.Background(), database, cfg)
	if err != nil {
		t.Fatal(err)
	}

	if stats.DirsDeleted == 0 {
		t.Error("expected at least one stale directory deleted")
	}

	database.QueryRow(`SELECT COUNT(*) FROM directories WHERE store = 'raw' AND path = 'remove'`).Scan(&count)
	if count != 0 {
		t.Errorf("stale directory 'remove' still in DB")
	}
}

func TestWalkSkipsUnreadableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses permission checks")
	}

	database, cfg := testSetup(t)

	writeFile(t, cfg.RawDir, "readable.txt", "ok")
	writeFile(t, cfg.RawDir, "locked/secret.txt", "nope")

	lockedDir := filepath.Join(cfg.RawDir, "locked")
	if err := os.Chmod(lockedDir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(lockedDir, 0o755) })

	stats, err := Run(context.Background(), database, cfg)
	if err != nil {
		t.Fatalf("walk should not bail on permission errors: %v", err)
	}

	if stats.FilesFound != 1 {
		t.Errorf("FilesFound = %d, want 1 (only readable.txt should be walked)", stats.FilesFound)
	}

	var count int
	database.QueryRow(`SELECT COUNT(*) FROM files WHERE store = 'raw' AND path = 'readable.txt'`).Scan(&count)
	if count != 1 {
		t.Errorf("expected readable.txt in DB, count=%d", count)
	}
}

func TestMissingFileRediscovered(t *testing.T) {
	database, cfg := testSetup(t)

	writeFile(t, cfg.RawDir, "file.txt", "content")

	if _, err := Run(context.Background(), database, cfg); err != nil {
		t.Fatal(err)
	}

	// Manually mark the file as missing.
	if _, err := database.Exec(`UPDATE files SET missing = 1 WHERE store = 'raw' AND path = 'file.txt'`); err != nil {
		t.Fatal(err)
	}

	// Verify it's missing.
	var missing int
	database.QueryRow(`SELECT missing FROM files WHERE store = 'raw' AND path = 'file.txt'`).Scan(&missing)
	if missing != 1 {
		t.Fatalf("expected missing=1, got %d", missing)
	}

	// Walk again — file is still on disk, should be rediscovered.
	if _, err := Run(context.Background(), database, cfg); err != nil {
		t.Fatal(err)
	}

	database.QueryRow(`SELECT missing FROM files WHERE store = 'raw' AND path = 'file.txt'`).Scan(&missing)
	if missing != 0 {
		t.Errorf("file should no longer be missing after re-walk, got missing=%d", missing)
	}
}
