package rule

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/davars/winnow/config"
	"github.com/davars/winnow/db"
	"github.com/davars/winnow/plan"
)

func testDB(t *testing.T) (*sql.DB, *config.Config) {
	t.Helper()
	tmp := t.TempDir()
	cfg := &config.Config{
		RawDir:   filepath.Join(tmp, "raw"),
		CleanDir: filepath.Join(tmp, "clean"),
		TrashDir: filepath.Join(tmp, "trash"),
		DataDir:  filepath.Join(tmp, "data"),
	}
	database, err := db.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return database, cfg
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

func insertDir(t *testing.T, database *sql.DB, store, path string, fileCount int64) int64 {
	t.Helper()
	res, err := database.Exec(`
		INSERT INTO directories (store, path, file_count, total_size)
		VALUES (?, ?, ?, 0)
	`, store, path, fileCount)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestJunkFilePatterns(t *testing.T) {
	database, cfg := testDB(t)

	keepID := insertFile(t, database, "raw", "photos/vacation.jpg")
	dsID := insertFile(t, database, "raw", "photos/.DS_Store")
	thumbsID := insertFile(t, database, "raw", "documents/Thumbs.db")
	resourceID := insertFile(t, database, "raw", "photos/._IMG_1234.JPG")
	cleanDSID := insertFile(t, database, "clean", ".DS_Store")

	ops, err := Junk{}.Evaluate(context.Background(), database, cfg, map[int64]bool{})
	if err != nil {
		t.Fatal(err)
	}

	trashed := map[int64]plan.Op{}
	for _, op := range ops {
		if op.Kind == plan.OpTrash {
			trashed[op.FileID] = op
		}
	}

	for name, id := range map[string]int64{
		".DS_Store":  dsID,
		"Thumbs.db":  thumbsID,
		"._IMG_1234": resourceID,
	} {
		op, ok := trashed[id]
		if !ok {
			t.Errorf("%s: expected trash op, got none", name)
			continue
		}
		if op.DstStore != "trash" || op.DstPath != op.SrcPath {
			t.Errorf("%s: unexpected dst store=%q path=%q", name, op.DstStore, op.DstPath)
		}
		if op.Rule != "junk" {
			t.Errorf("%s: rule = %q, want junk", name, op.Rule)
		}
		if op.Reason == "" {
			t.Errorf("%s: missing reason", name)
		}
	}

	if _, ok := trashed[keepID]; ok {
		t.Errorf("unexpected op for non-junk file id=%d", keepID)
	}
	if _, ok := trashed[cleanDSID]; ok {
		t.Errorf("junk file in clean store should not be trashed (file rules only touch raw)")
	}
}

func TestJunkDirectoryPatterns(t *testing.T) {
	database, cfg := testDB(t)

	insideID := insertFile(t, database, "raw", "photos/@eaDir/thumb.jpg")
	keepID := insertFile(t, database, "raw", "photos/good.jpg")
	eaDirID := insertDir(t, database, "raw", "photos/@eaDir", 1)
	insertDir(t, database, "raw", "photos", 2)

	ops, err := Junk{}.Evaluate(context.Background(), database, cfg, map[int64]bool{})
	if err != nil {
		t.Fatal(err)
	}

	var trashedInside bool
	var rmdirEADir bool
	var touchedKeep bool
	for _, op := range ops {
		switch op.Kind {
		case plan.OpTrash:
			if op.FileID == insideID {
				trashedInside = true
			}
			if op.FileID == keepID {
				touchedKeep = true
			}
		case plan.OpRemoveDir:
			if op.DirID == eaDirID {
				rmdirEADir = true
			}
		}
	}

	if !trashedInside {
		t.Error("expected file under @eaDir to be trashed")
	}
	if !rmdirEADir {
		t.Error("expected @eaDir directory to be scheduled for removal")
	}
	if touchedKeep {
		t.Error("non-junk file should not be touched")
	}
}

func TestJunkEmptyDirectories(t *testing.T) {
	database, cfg := testDB(t)

	emptyRaw := insertDir(t, database, "raw", "sub/empty", 0)
	emptyClean := insertDir(t, database, "clean", "old", 0)
	emptyTrash := insertDir(t, database, "trash", "gone", 0)
	nonempty := insertDir(t, database, "raw", "photos", 5)
	root := insertDir(t, database, "raw", ".", 5)

	ops, err := Junk{}.Evaluate(context.Background(), database, cfg, map[int64]bool{})
	if err != nil {
		t.Fatal(err)
	}

	rmdirs := map[int64]plan.Op{}
	for _, op := range ops {
		if op.Kind == plan.OpRemoveDir {
			rmdirs[op.DirID] = op
		}
	}
	for _, id := range []int64{emptyRaw, emptyClean, emptyTrash} {
		if _, ok := rmdirs[id]; !ok {
			t.Errorf("expected OpRemoveDir for empty dir id=%d", id)
		}
	}
	if _, ok := rmdirs[nonempty]; ok {
		t.Errorf("non-empty directory should not be removed")
	}
	if _, ok := rmdirs[root]; ok {
		t.Errorf("store root should never be proposed for removal")
	}
}

func TestJunkHonorsClaimedSet(t *testing.T) {
	database, cfg := testDB(t)

	claimedID := insertFile(t, database, "raw", ".DS_Store")
	freeID := insertFile(t, database, "raw", "sub/.DS_Store")

	claimed := map[int64]bool{claimedID: true}
	ops, err := Junk{}.Evaluate(context.Background(), database, cfg, claimed)
	if err != nil {
		t.Fatal(err)
	}

	for _, op := range ops {
		if op.FileID == claimedID {
			t.Errorf("junk claimed an already-claimed file id=%d", claimedID)
		}
	}

	var sawFree bool
	for _, op := range ops {
		if op.FileID == freeID {
			sawFree = true
		}
	}
	if !sawFree {
		t.Errorf("expected free .DS_Store (id=%d) to be claimed", freeID)
	}
}

func TestBuildPlanPassesClaimedForward(t *testing.T) {
	database, cfg := testDB(t)
	insertFile(t, database, "raw", ".DS_Store")

	p, err := BuildPlan(context.Background(), database, cfg, All())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Ops) == 0 {
		t.Fatal("expected at least one op")
	}
}
