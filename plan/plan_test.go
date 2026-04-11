package plan

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/davars/winnow/config"
	"github.com/davars/winnow/db"
)

func testSetup(t *testing.T) (*sql.DB, *config.Config) {
	t.Helper()
	tmp := t.TempDir()
	cfg := &config.Config{
		RawDir:   filepath.Join(tmp, "raw"),
		CleanDir: filepath.Join(tmp, "clean"),
		TrashDir: filepath.Join(tmp, "trash"),
		DataDir:  filepath.Join(tmp, "data"),
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

func insertFile(t *testing.T, database *sql.DB, store, path string) int64 {
	t.Helper()
	res, err := database.Exec(`
		INSERT INTO files (store, path, size, mod_time, found_at, reconciled_at, missing)
		VALUES (?, ?, 0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 0)
	`, store, path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func insertDir(t *testing.T, database *sql.DB, store, path string) int64 {
	t.Helper()
	res, err := database.Exec(`
		INSERT INTO directories (store, path, file_count, total_size)
		VALUES (?, ?, 0, 0)
	`, store, path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestExecuteTrashMovesFile(t *testing.T) {
	database, cfg := testSetup(t)

	writeFile(t, cfg.RawDir, "photos/.DS_Store", "junk")
	fileID := insertFile(t, database, "raw", "photos/.DS_Store")

	p := &Plan{Ops: []Op{{
		Kind:     OpTrash,
		FileID:   fileID,
		SrcStore: "raw",
		SrcPath:  "photos/.DS_Store",
		DstStore: "trash",
		DstPath:  "photos/.DS_Store",
		Rule:     "junk",
		Reason:   "junk file: .DS_Store",
	}}}

	stats, err := Execute(context.Background(), database, p, ExecuteOpts{Stores: cfg.Stores()})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Succeeded != 1 || stats.Failed != 0 {
		t.Errorf("stats = %+v, want {1 0}", stats)
	}

	if _, err := os.Stat(filepath.Join(cfg.RawDir, "photos/.DS_Store")); !os.IsNotExist(err) {
		t.Errorf("source still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.TrashDir, "photos/.DS_Store")); err != nil {
		t.Errorf("destination missing: %v", err)
	}

	var store, path string
	database.QueryRow(`SELECT store, path FROM files WHERE id = ?`, fileID).Scan(&store, &path)
	if store != "trash" || path != "photos/.DS_Store" {
		t.Errorf("files row not updated: store=%q path=%q", store, path)
	}

	var opCount int
	database.QueryRow(
		`SELECT COUNT(*) FROM operations WHERE file_id = ? AND dst_store = 'trash' AND rule = 'junk'`,
		fileID,
	).Scan(&opCount)
	if opCount != 1 {
		t.Errorf("operations count = %d, want 1", opCount)
	}
}

func TestExecuteRemoveDirDeletesAndUpdatesDB(t *testing.T) {
	database, cfg := testSetup(t)

	if err := os.MkdirAll(filepath.Join(cfg.RawDir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	dirID := insertDir(t, database, "raw", "empty")

	p := &Plan{Ops: []Op{{
		Kind:     OpRemoveDir,
		DirID:    dirID,
		SrcStore: "raw",
		SrcPath:  "empty",
		Rule:     "junk",
		Reason:   "empty directory",
	}}}

	stats, err := Execute(context.Background(), database, p, ExecuteOpts{Stores: cfg.Stores()})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Succeeded != 1 || stats.Failed != 0 {
		t.Errorf("stats = %+v, want {1 0}", stats)
	}

	if _, err := os.Stat(filepath.Join(cfg.RawDir, "empty")); !os.IsNotExist(err) {
		t.Errorf("directory still exists: %v", err)
	}

	var count int
	database.QueryRow(`SELECT COUNT(*) FROM directories WHERE id = ?`, dirID).Scan(&count)
	if count != 0 {
		t.Errorf("directories row not deleted: count=%d", count)
	}

	var opCount int
	database.QueryRow(
		`SELECT COUNT(*) FROM operations WHERE dir_id = ? AND rule = 'junk'`, dirID,
	).Scan(&opCount)
	if opCount != 1 {
		t.Errorf("operations count = %d, want 1", opCount)
	}
}

func TestExecuteRemoveDirNonEmptyFails(t *testing.T) {
	database, cfg := testSetup(t)

	writeFile(t, cfg.RawDir, "notempty/file.txt", "content")
	dirID := insertDir(t, database, "raw", "notempty")

	p := &Plan{Ops: []Op{{
		Kind:     OpRemoveDir,
		DirID:    dirID,
		SrcStore: "raw",
		SrcPath:  "notempty",
		Rule:     "junk",
		Reason:   "empty directory",
	}}}

	stats, err := Execute(context.Background(), database, p, ExecuteOpts{Stores: cfg.Stores()})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Failed != 1 {
		t.Errorf("expected Failed=1, got %+v", stats)
	}

	var errCount int
	database.QueryRow(
		`SELECT COUNT(*) FROM process_errors WHERE dir_id = ? AND rule = 'junk'`, dirID,
	).Scan(&errCount)
	if errCount != 1 {
		t.Errorf("process_errors count = %d, want 1", errCount)
	}
}

func TestExecuteMissingSourceLoggedAndContinues(t *testing.T) {
	database, cfg := testSetup(t)

	// file_id references a row, but file does not exist on disk
	fileID := insertFile(t, database, "raw", "ghost.jpg")
	writeFile(t, cfg.RawDir, "real.jpg", "ok")
	realID := insertFile(t, database, "raw", "real.jpg")

	p := &Plan{Ops: []Op{
		{
			Kind: OpTrash, FileID: fileID,
			SrcStore: "raw", SrcPath: "ghost.jpg",
			DstStore: "trash", DstPath: "ghost.jpg",
			Rule: "junk", Reason: "junk file",
		},
		{
			Kind: OpTrash, FileID: realID,
			SrcStore: "raw", SrcPath: "real.jpg",
			DstStore: "trash", DstPath: "real.jpg",
			Rule: "junk", Reason: "junk file",
		},
	}}

	stats, err := Execute(context.Background(), database, p, ExecuteOpts{Stores: cfg.Stores()})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Succeeded != 1 || stats.Failed != 1 {
		t.Errorf("stats = %+v, want {1 1}", stats)
	}

	var errCount int
	database.QueryRow(
		`SELECT COUNT(*) FROM process_errors WHERE file_id = ?`, fileID,
	).Scan(&errCount)
	if errCount != 1 {
		t.Errorf("process_errors for ghost file = %d, want 1", errCount)
	}

	// real file should have moved successfully
	if _, err := os.Stat(filepath.Join(cfg.TrashDir, "real.jpg")); err != nil {
		t.Errorf("real file not moved: %v", err)
	}
}

func TestExecuteOrderingDeepestDirFirst(t *testing.T) {
	database, cfg := testSetup(t)

	writeFile(t, cfg.RawDir, "a/b/c/junk.txt", "x")
	fileID := insertFile(t, database, "raw", "a/b/c/junk.txt")
	aID := insertDir(t, database, "raw", "a")
	abID := insertDir(t, database, "raw", "a/b")
	abcID := insertDir(t, database, "raw", "a/b/c")

	p := &Plan{Ops: []Op{
		// Intentionally out of order.
		{Kind: OpRemoveDir, DirID: aID, SrcStore: "raw", SrcPath: "a", Rule: "junk", Reason: "empty"},
		{Kind: OpRemoveDir, DirID: abID, SrcStore: "raw", SrcPath: "a/b", Rule: "junk", Reason: "empty"},
		{Kind: OpRemoveDir, DirID: abcID, SrcStore: "raw", SrcPath: "a/b/c", Rule: "junk", Reason: "empty"},
		{Kind: OpTrash, FileID: fileID,
			SrcStore: "raw", SrcPath: "a/b/c/junk.txt",
			DstStore: "trash", DstPath: "a/b/c/junk.txt",
			Rule: "junk", Reason: "junk"},
	}}

	stats, err := Execute(context.Background(), database, p, ExecuteOpts{Stores: cfg.Stores()})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Succeeded != 4 || stats.Failed != 0 {
		t.Errorf("stats = %+v, want {4 0}", stats)
	}

	for _, rel := range []string{"a/b/c", "a/b", "a"} {
		if _, err := os.Stat(filepath.Join(cfg.RawDir, rel)); !os.IsNotExist(err) {
			t.Errorf("%s still exists", rel)
		}
	}
}

func TestExecutePreProcessHookRunsFirst(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell hook test is POSIX-only")
	}
	database, cfg := testSetup(t)

	marker := filepath.Join(t.TempDir(), "hook.ran")
	hookPath := filepath.Join(t.TempDir(), "hook.sh")
	hookScript := "#!/bin/sh\ntouch " + marker + "\n"
	if err := os.WriteFile(hookPath, []byte(hookScript), 0o755); err != nil {
		t.Fatal(err)
	}

	writeFile(t, cfg.RawDir, ".DS_Store", "junk")
	fileID := insertFile(t, database, "raw", ".DS_Store")

	p := &Plan{Ops: []Op{{
		Kind: OpTrash, FileID: fileID,
		SrcStore: "raw", SrcPath: ".DS_Store",
		DstStore: "trash", DstPath: ".DS_Store",
		Rule: "junk", Reason: "junk file",
	}}}

	_, err := Execute(context.Background(), database, p, ExecuteOpts{
		Stores:         cfg.Stores(),
		PreProcessHook: hookPath,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(marker); err != nil {
		t.Errorf("pre_process_hook did not run: %v", err)
	}
}

func TestExecutePreProcessHookFailureAborts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell hook test is POSIX-only")
	}
	database, cfg := testSetup(t)

	hookPath := filepath.Join(t.TempDir(), "bad.sh")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	writeFile(t, cfg.RawDir, ".DS_Store", "junk")
	fileID := insertFile(t, database, "raw", ".DS_Store")

	p := &Plan{Ops: []Op{{
		Kind: OpTrash, FileID: fileID,
		SrcStore: "raw", SrcPath: ".DS_Store",
		DstStore: "trash", DstPath: ".DS_Store",
		Rule: "junk", Reason: "junk file",
	}}}

	_, err := Execute(context.Background(), database, p, ExecuteOpts{
		Stores:         cfg.Stores(),
		PreProcessHook: hookPath,
	})
	if err == nil {
		t.Fatal("expected error from failing pre_process_hook")
	}

	// File should NOT have moved.
	if _, err := os.Stat(filepath.Join(cfg.RawDir, ".DS_Store")); err != nil {
		t.Errorf("source file missing after aborted hook: %v", err)
	}
}

func TestPrintEmpty(t *testing.T) {
	p := &Plan{}
	var buf stringWriter
	p.Print(&buf)
	if buf.String() != "No operations proposed.\n" {
		t.Errorf("unexpected output: %q", buf.String())
	}
}

type stringWriter struct {
	b []byte
}

func (s *stringWriter) Write(p []byte) (int, error) { s.b = append(s.b, p...); return len(p), nil }
func (s *stringWriter) String() string               { return string(s.b) }
