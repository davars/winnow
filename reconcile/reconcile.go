package reconcile

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Stats summarizes what reconcile did.
type Stats struct {
	Marked int64
}

// Run marks files as missing if their reconciled_at is older than the
// staleness threshold. Only non-missing files are considered.
func Run(ctx context.Context, db *sql.DB, maxStaleness time.Duration) (Stats, error) {
	cutoff := time.Now().UTC().Add(-maxStaleness).Format(time.RFC3339)

	result, err := db.ExecContext(ctx,
		`UPDATE files SET missing = 1 WHERE reconciled_at < ? AND missing = 0`,
		cutoff,
	)
	if err != nil {
		return Stats{}, fmt.Errorf("marking stale files: %w", err)
	}

	n, err := result.RowsAffected()
	if err != nil {
		return Stats{}, fmt.Errorf("getting rows affected: %w", err)
	}

	return Stats{Marked: n}, nil
}
