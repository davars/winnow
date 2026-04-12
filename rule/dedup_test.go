package rule

import (
	"context"
	"testing"

	"github.com/davars/winnow/plan"
)

func TestDedupKeepsShortestPath(t *testing.T) {
	database, cfg := testDB(t)

	hash := "aabbccdd00112233445566778899aabbccdd00112233445566778899aabbccdd"

	shortID := insertFile(t, database, "raw", "a.jpg", hash)
	longID := insertFile(t, database, "raw", "sub/dir/a.jpg", hash)

	ops, err := Dedup{}.Evaluate(context.Background(), database, cfg, map[int64]bool{})
	if err != nil {
		t.Fatal(err)
	}

	if len(ops) != 1 {
		t.Fatalf("expected 1 op, got %d", len(ops))
	}
	op := ops[0]
	if op.FileID != longID {
		t.Errorf("expected longer path (id=%d) to be trashed, got id=%d", longID, op.FileID)
	}
	if op.Kind != plan.OpTrash {
		t.Errorf("expected OpTrash, got %v", op.Kind)
	}
	if op.Rule != "dedup" {
		t.Errorf("rule = %q, want dedup", op.Rule)
	}
	_ = shortID // keeper
}

func TestDedupLexicographicTiebreaker(t *testing.T) {
	database, cfg := testDB(t)

	hash := "1111111111111111111111111111111111111111111111111111111111111111"

	// Same length paths — lexicographic order picks "aaa.jpg" as keeper.
	keepID := insertFile(t, database, "raw", "aaa.jpg", hash)
	trashID := insertFile(t, database, "raw", "zzz.jpg", hash)

	ops, err := Dedup{}.Evaluate(context.Background(), database, cfg, map[int64]bool{})
	if err != nil {
		t.Fatal(err)
	}

	if len(ops) != 1 {
		t.Fatalf("expected 1 op, got %d", len(ops))
	}
	if ops[0].FileID != trashID {
		t.Errorf("expected zzz.jpg (id=%d) trashed, got id=%d", trashID, ops[0].FileID)
	}
	_ = keepID
}

func TestDedupIgnoresCleanAndTrash(t *testing.T) {
	database, cfg := testDB(t)

	hash := "2222222222222222222222222222222222222222222222222222222222222222"

	// One copy in raw, one in clean, one in trash — no dedup because only
	// one raw file has this hash.
	insertFile(t, database, "raw", "photo.jpg", hash)
	insertFile(t, database, "clean", "photo.jpg", hash)
	insertFile(t, database, "trash", "photo.jpg", hash)

	ops, err := Dedup{}.Evaluate(context.Background(), database, cfg, map[int64]bool{})
	if err != nil {
		t.Fatal(err)
	}

	if len(ops) != 0 {
		t.Errorf("expected 0 ops (only one raw copy), got %d", len(ops))
	}
}

func TestDedupHonorsClaimedSet(t *testing.T) {
	database, cfg := testDB(t)

	hash := "3333333333333333333333333333333333333333333333333333333333333333"

	id1 := insertFile(t, database, "raw", "a.jpg", hash)
	id2 := insertFile(t, database, "raw", "b.jpg", hash)
	id3 := insertFile(t, database, "raw", "c.jpg", hash)

	// Claim the would-be keeper (a.jpg) — dedup should pick b.jpg as the
	// new keeper and trash c.jpg.
	claimed := map[int64]bool{id1: true}
	ops, err := Dedup{}.Evaluate(context.Background(), database, cfg, claimed)
	if err != nil {
		t.Fatal(err)
	}

	if len(ops) != 1 {
		t.Fatalf("expected 1 op, got %d", len(ops))
	}
	if ops[0].FileID != id3 {
		t.Errorf("expected c.jpg (id=%d) trashed, got id=%d", id3, ops[0].FileID)
	}
	_ = id2 // new keeper
}

func TestDedupMultipleGroups(t *testing.T) {
	database, cfg := testDB(t)

	hashA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashB := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	insertFile(t, database, "raw", "a1.jpg", hashA)
	a2 := insertFile(t, database, "raw", "a2.jpg", hashA)
	a3 := insertFile(t, database, "raw", "a3.jpg", hashA)

	insertFile(t, database, "raw", "b1.jpg", hashB)
	b2 := insertFile(t, database, "raw", "b2.jpg", hashB)

	ops, err := Dedup{}.Evaluate(context.Background(), database, cfg, map[int64]bool{})
	if err != nil {
		t.Fatal(err)
	}

	trashed := map[int64]bool{}
	for _, op := range ops {
		trashed[op.FileID] = true
	}

	if len(ops) != 3 {
		t.Fatalf("expected 3 ops, got %d", len(ops))
	}
	for _, id := range []int64{a2, a3, b2} {
		if !trashed[id] {
			t.Errorf("expected file id=%d to be trashed", id)
		}
	}
}

func TestDedupBuildPlanIntegration(t *testing.T) {
	database, cfg := testDB(t)

	hash := "4444444444444444444444444444444444444444444444444444444444444444"

	// Insert a .DS_Store that is also a duplicate — junk should claim it
	// first, so dedup shouldn't see it.
	dsID := insertFile(t, database, "raw", ".DS_Store", hash)
	otherID := insertFile(t, database, "raw", "copy.jpg", hash)

	p, err := BuildPlan(context.Background(), database, cfg, All())
	if err != nil {
		t.Fatal(err)
	}

	var junkClaimedDS bool
	var dedupTouchedDS bool
	var dedupTouchedOther bool
	for _, op := range p.Ops {
		if op.FileID == dsID && op.Rule == "junk" {
			junkClaimedDS = true
		}
		if op.FileID == dsID && op.Rule == "dedup" {
			dedupTouchedDS = true
		}
		if op.FileID == otherID && op.Rule == "dedup" {
			dedupTouchedOther = true
		}
	}

	if !junkClaimedDS {
		t.Error("expected junk rule to claim .DS_Store")
	}
	if dedupTouchedDS {
		t.Error("dedup should not touch a file already claimed by junk")
	}
	// With the junk-claimed file removed from the dedup group, only one raw
	// copy remains → no dedup op needed.
	if dedupTouchedOther {
		t.Error("with .DS_Store claimed by junk, copy.jpg is the only raw copy and should not be trashed")
	}
}
