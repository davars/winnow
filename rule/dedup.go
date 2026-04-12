package rule

import (
	"context"
	"database/sql"

	"github.com/davars/winnow/config"
	"github.com/davars/winnow/plan"
)

// Dedup identifies duplicate files in the raw store by sha256 hash. For each
// group of raw files sharing the same hash, the copy with the shortest path is
// kept (tiebreaker: lexicographic order). All other copies are trashed.
//
// Clean and trash stores are not scanned — raw is the canonical source, and
// the user may intentionally place multiple copies in clean under different
// organization schemes.
type Dedup struct{}

func (Dedup) Name() string { return "dedup" }

func (Dedup) Evaluate(ctx context.Context, db *sql.DB, cfg *config.Config, claimed map[int64]bool) ([]plan.Op, error) {
	rows, err := db.QueryContext(ctx, `
		WITH dupes AS (
		    SELECT sha256
		    FROM files
		    WHERE store = 'raw' AND missing = 0 AND sha256 IS NOT NULL
		    GROUP BY sha256
		    HAVING COUNT(*) > 1
		)
		SELECT f.id, f.path, f.sha256
		FROM files f
		JOIN dupes d ON f.sha256 = d.sha256
		WHERE f.store = 'raw' AND f.missing = 0
		ORDER BY f.sha256, length(f.path), f.path
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ops []plan.Op
	var currentHash string
	keeperChosen := false

	for rows.Next() {
		var id int64
		var path, hash string
		if err := rows.Scan(&id, &path, &hash); err != nil {
			return nil, err
		}
		if hash != currentHash {
			currentHash = hash
			keeperChosen = false
		}
		if claimed[id] {
			continue
		}
		if !keeperChosen {
			// First unclaimed file for this hash is the keeper.
			keeperChosen = true
			continue
		}
		ops = append(ops, plan.Op{
			Kind:     plan.OpTrash,
			FileID:   id,
			SrcStore: "raw",
			SrcPath:  path,
			DstStore: "trash",
			DstPath:  path,
			Rule:     "dedup",
			Reason:   "duplicate of " + currentHash[:min(12, len(currentHash))],
		})
	}
	return ops, rows.Err()
}
