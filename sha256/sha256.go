package sha256

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/davars/winnow/worker"
)

const enricherName = "sha256"

// Source implements worker.WorkSource for SHA-256 hashing.
type Source struct {
	Stores map[string]string // store name → directory path
}

// FetchBatch returns files needing hashing: hashed_at is NULL or hashed_at < mod_time, and not missing.
func (s *Source) FetchBatch(ctx context.Context, database *sql.DB, limit int) ([]worker.WorkItem, error) {
	rows, err := database.QueryContext(ctx,
		`SELECT id, store, path FROM files
		 WHERE (hashed_at IS NULL OR hashed_at < mod_time) AND missing = 0
		 LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []worker.WorkItem
	for rows.Next() {
		var fileID int64
		var store, relPath string
		if err := rows.Scan(&fileID, &store, &relPath); err != nil {
			return nil, err
		}
		baseDir, ok := s.Stores[store]
		if !ok {
			continue
		}
		items = append(items, worker.WorkItem{
			FileID: fileID,
			Path:   filepath.Join(baseDir, relPath),
		})
	}
	return items, rows.Err()
}

// WriteBatch writes hash results and logs errors in a single transaction.
func (s *Source) WriteBatch(ctx context.Context, database *sql.DB, results []worker.WorkResult) error {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	updateStmt, err := tx.PrepareContext(ctx,
		`UPDATE files SET sha256 = ?, hashed_at = ? WHERE id = ?`)
	if err != nil {
		return err
	}
	defer updateStmt.Close()

	errStmt, err := tx.PrepareContext(ctx,
		`INSERT INTO process_errors (file_id, enricher, error, occurred_at) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer errStmt.Close()

	now := time.Now().UTC().Format(time.RFC3339)
	for _, r := range results {
		if r.Err != nil {
			if _, err := errStmt.ExecContext(ctx, r.Item.FileID, enricherName, r.Err.Error(), now); err != nil {
				return err
			}
			// Set hashed_at so the file isn't re-fetched endlessly.
			// It will be retried if walk updates mod_time (file changed on disk).
			if _, err := updateStmt.ExecContext(ctx, nil, now, r.Item.FileID); err != nil {
				return err
			}
			continue
		}
		hash := r.Values["sha256"]
		hashedAt := r.Values["hashed_at"]
		if _, err := updateStmt.ExecContext(ctx, hash, hashedAt, r.Item.FileID); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// Process computes SHA-256 hashes for the given items.
func Process(ctx context.Context, items []worker.WorkItem) []worker.WorkResult {
	results := make([]worker.WorkResult, len(items))
	now := time.Now().UTC().Format(time.RFC3339)
	for i, item := range items {
		if ctx.Err() != nil {
			results[i] = worker.WorkResult{Item: item, Err: ctx.Err()}
			continue
		}
		hash, err := hashFile(item.Path)
		if err != nil {
			results[i] = worker.WorkResult{Item: item, Err: fmt.Errorf("hashing %s: %w", item.Path, err)}
			continue
		}
		results[i] = worker.WorkResult{
			Item: item,
			Values: map[string]any{
				"sha256":    hash,
				"hashed_at": now,
			},
		}
	}
	return results
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Run runs the worker pool for SHA-256 hashing. The files table schema is
// managed by db.Open.
func Run(ctx context.Context, database *sql.DB, stores map[string]string, opts worker.Opts) (worker.Stats, error) {
	source := &Source{Stores: stores}
	return worker.Run(ctx, database, source, Process, opts)
}
