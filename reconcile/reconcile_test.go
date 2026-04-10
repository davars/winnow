package reconcile

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/davars/winnow/db"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "data", "winnow.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func insertFile(t *testing.T, database *sql.DB, store, path string, reconciledAt time.Time, missing int) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	rat := reconciledAt.UTC().Format(time.RFC3339)
	_, err := database.Exec(
		`INSERT INTO files (store, path, size, mod_time, found_at, reconciled_at, missing)
		 VALUES (?, ?, 100, ?, ?, ?, ?)`,
		store, path, now, now, rat, missing,
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestStaleFilesMarkedMissing(t *testing.T) {
	database := openTestDB(t)

	staleTime := time.Now().UTC().Add(-72 * time.Hour)
	insertFile(t, database, "raw", "old_file.txt", staleTime, 0)

	stats, err := Run(context.Background(), database, 48*time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	if stats.Marked != 1 {
		t.Errorf("Marked = %d, want 1", stats.Marked)
	}

	var missing int
	database.QueryRow(`SELECT missing FROM files WHERE path = 'old_file.txt'`).Scan(&missing)
	if missing != 1 {
		t.Errorf("missing = %d, want 1", missing)
	}
}

func TestRecentFilesUntouched(t *testing.T) {
	database := openTestDB(t)

	recentTime := time.Now().UTC().Add(-1 * time.Hour)
	insertFile(t, database, "raw", "recent_file.txt", recentTime, 0)

	stats, err := Run(context.Background(), database, 48*time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	if stats.Marked != 0 {
		t.Errorf("Marked = %d, want 0", stats.Marked)
	}

	var missing int
	database.QueryRow(`SELECT missing FROM files WHERE path = 'recent_file.txt'`).Scan(&missing)
	if missing != 0 {
		t.Errorf("missing = %d, want 0", missing)
	}
}

func TestAlreadyMissingFilesSkipped(t *testing.T) {
	database := openTestDB(t)

	staleTime := time.Now().UTC().Add(-72 * time.Hour)
	insertFile(t, database, "raw", "already_missing.txt", staleTime, 1)

	stats, err := Run(context.Background(), database, 48*time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	if stats.Marked != 0 {
		t.Errorf("Marked = %d, want 0 (already missing files should be skipped)", stats.Marked)
	}
}

func TestMixedFiles(t *testing.T) {
	database := openTestDB(t)

	staleTime := time.Now().UTC().Add(-72 * time.Hour)
	recentTime := time.Now().UTC().Add(-1 * time.Hour)

	insertFile(t, database, "raw", "stale1.txt", staleTime, 0)
	insertFile(t, database, "raw", "stale2.txt", staleTime, 0)
	insertFile(t, database, "raw", "recent.txt", recentTime, 0)
	insertFile(t, database, "raw", "already_missing.txt", staleTime, 1)

	stats, err := Run(context.Background(), database, 48*time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	if stats.Marked != 2 {
		t.Errorf("Marked = %d, want 2", stats.Marked)
	}

	// Verify each file's state.
	tests := []struct {
		path        string
		wantMissing int
	}{
		{"stale1.txt", 1},
		{"stale2.txt", 1},
		{"recent.txt", 0},
		{"already_missing.txt", 1},
	}
	for _, tt := range tests {
		var missing int
		if err := database.QueryRow(`SELECT missing FROM files WHERE path = ?`, tt.path).Scan(&missing); err != nil {
			t.Fatalf("querying %s: %v", tt.path, err)
		}
		if missing != tt.wantMissing {
			t.Errorf("%s: missing = %d, want %d", tt.path, missing, tt.wantMissing)
		}
	}
}

func TestReconcileIdempotent(t *testing.T) {
	database := openTestDB(t)

	staleTime := time.Now().UTC().Add(-72 * time.Hour)
	insertFile(t, database, "raw", "stale.txt", staleTime, 0)

	// First run.
	stats1, err := Run(context.Background(), database, 48*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if stats1.Marked != 1 {
		t.Fatalf("first run: Marked = %d, want 1", stats1.Marked)
	}

	// Second run — should mark nothing new.
	stats2, err := Run(context.Background(), database, 48*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if stats2.Marked != 0 {
		t.Errorf("second run: Marked = %d, want 0", stats2.Marked)
	}
}

func TestReconcileMultipleStores(t *testing.T) {
	database := openTestDB(t)

	staleTime := time.Now().UTC().Add(-72 * time.Hour)
	insertFile(t, database, "raw", "raw_stale.txt", staleTime, 0)
	insertFile(t, database, "clean", "clean_stale.txt", staleTime, 0)
	insertFile(t, database, "trash", "trash_stale.txt", staleTime, 0)

	stats, err := Run(context.Background(), database, 48*time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	if stats.Marked != 3 {
		t.Errorf("Marked = %d, want 3", stats.Marked)
	}
}

