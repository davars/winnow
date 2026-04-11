// Package enricher defines the two-pass enrichment framework: Identify
// populates candidate rows, Process fills them in via the worker pool.
package enricher

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/davars/winnow/db"
	"github.com/davars/winnow/worker"
)

// runTool executes an external binary and returns its stdout, classifying
// exec.ErrNotFound and *exec.ExitError into wrapped errors with a consistent
// format. Used by enrichers that shell out to e.g. `file` or `exiftool`.
func runTool(ctx context.Context, bin string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, bin, args...).Output()
	if err == nil {
		return out, nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		return out, fmt.Errorf("%s binary not found on PATH: %w", bin, err)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return out, fmt.Errorf("%s exited %d: %s",
			bin, exitErr.ExitCode(), strings.TrimSpace(string(exitErr.Stderr)))
	}
	return out, err
}

type (
	Column = db.Column
	Index  = db.Index
)

// Enricher processes files and stores content-derived metadata. Each enricher
// owns its own table keyed on content hash. Base columns (hash, file_id,
// processed_at) are managed by the framework; Columns() declares only the
// enricher-specific ones.
type Enricher interface {
	db.SchemaProvider

	Identify(ctx context.Context, database *sql.DB) (int, error)
	Process(ctx context.Context, items []worker.WorkItem) []worker.WorkResult
	ProcessBatch() int
}

// IdentifyAllHashes inserts one candidate row per unique content hash of
// non-missing files that aren't already in the enricher table. Used by
// enrichers that apply to every file regardless of type (e.g. MIME).
func IdentifyAllHashes(ctx context.Context, database *sql.DB, table string) (int, error) {
	query := fmt.Sprintf(`
		INSERT OR IGNORE INTO %s (hash, file_id, processed_at)
		SELECT f.sha256, f.id, NULL
		FROM files f
		LEFT JOIN %s e ON e.hash = f.sha256
		WHERE f.sha256 IS NOT NULL
		  AND f.missing = 0
		  AND e.hash IS NULL
	`, table, table)

	res, err := database.ExecContext(ctx, query)
	if err != nil {
		return 0, fmt.Errorf("identify %s: %w", table, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// IdentifyByMimeType inserts one candidate row per unique hash whose mime_type
// (from the mime enricher table) matches any of the given types and that
// isn't already in the target table. Prerequisites: walk, sha256, and the
// mime enricher must have run first.
func IdentifyByMimeType(ctx context.Context, database *sql.DB, table string, mimeTypes []string) (int, error) {
	if len(mimeTypes) == 0 {
		return 0, nil
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(mimeTypes)), ",")
	query := fmt.Sprintf(`
		INSERT OR IGNORE INTO %s (hash, file_id, processed_at)
		SELECT m.hash, f.id, NULL
		FROM mime m
		JOIN files f ON f.sha256 = m.hash AND f.missing = 0
		LEFT JOIN %s e ON e.hash = m.hash
		WHERE m.mime_type IN (%s)
		  AND e.hash IS NULL
	`, table, table, placeholders)

	args := make([]any, len(mimeTypes))
	for i, m := range mimeTypes {
		args[i] = m
	}

	res, err := database.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("identify %s: %w", table, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

type source struct {
	name    string
	table   string
	columns []string
	stores  map[string]string
}

// FetchBatch ignores the stored file_id (historical/audit only — it may point
// to a file that has since moved or gone missing) and resolves the path via
// any current non-missing file with the same content hash.
func (s *source) FetchBatch(ctx context.Context, database *sql.DB, limit int) ([]worker.WorkItem, error) {
	query := fmt.Sprintf(`
		SELECT e.hash, f.id, f.store, f.path
		FROM %s e
		JOIN files f ON f.id = (
			SELECT f2.id FROM files f2
			WHERE f2.sha256 = e.hash AND f2.missing = 0
			ORDER BY f2.id LIMIT 1
		)
		WHERE e.processed_at IS NULL
		LIMIT ?
	`, s.table)

	rows, err := database.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []worker.WorkItem
	for rows.Next() {
		var hash, store, relPath string
		var fileID int64
		if err := rows.Scan(&hash, &fileID, &store, &relPath); err != nil {
			return nil, err
		}
		baseDir, ok := s.stores[store]
		if !ok {
			continue
		}
		items = append(items, worker.WorkItem{
			Hash:   hash,
			FileID: fileID,
			Path:   filepath.Join(baseDir, relPath),
		})
	}
	return items, rows.Err()
}

// WriteBatch writes results in a single transaction. processed_at is always
// set — even on per-item error — so failed rows are not re-fetched on the next
// run; they will only retry if the file content changes (which invalidates the
// hash and drops the row from the candidate set naturally).
func (s *source) WriteBatch(ctx context.Context, database *sql.DB, results []worker.WorkResult) error {
	for _, r := range results {
		for k := range r.Values {
			if !slices.Contains(s.columns, k) {
				return fmt.Errorf("enricher %s: unknown column %q in result", s.name, k)
			}
		}
	}

	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	setCols := make([]string, 0, len(s.columns)+1)
	setCols = append(setCols, "processed_at = ?")
	for _, c := range s.columns {
		setCols = append(setCols, c+" = ?")
	}
	updateSQL := fmt.Sprintf("UPDATE %s SET %s WHERE hash = ?", s.table, strings.Join(setCols, ", "))

	updateStmt, err := tx.PrepareContext(ctx, updateSQL)
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
		args := make([]any, 0, len(s.columns)+2)
		args = append(args, now)
		if r.Err != nil {
			if _, err := errStmt.ExecContext(ctx, r.Item.FileID, s.name, r.Err.Error(), now); err != nil {
				return err
			}
			for range s.columns {
				args = append(args, nil)
			}
		} else {
			for _, c := range s.columns {
				args = append(args, r.Values[c])
			}
		}
		args = append(args, r.Item.Hash)
		if _, err := updateStmt.ExecContext(ctx, args...); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func SetupSchema(database *sql.DB, e Enricher) error {
	if err := db.CreateEnricherTable(database, e.TableName()); err != nil {
		return err
	}
	if err := db.EnsureSchema(database, e); err != nil {
		return fmt.Errorf("ensuring %s schema: %w", e.Name(), err)
	}
	return nil
}

func RunIdentify(ctx context.Context, database *sql.DB, e Enricher) (int, error) {
	if err := SetupSchema(database, e); err != nil {
		return 0, err
	}
	return e.Identify(ctx, database)
}

// RunProcess assumes the enricher schema already exists; call SetupSchema or
// RunIdentify first.
func RunProcess(ctx context.Context, database *sql.DB, e Enricher, stores map[string]string, opts worker.Opts) (worker.Stats, error) {
	cols := e.Columns()
	colNames := make([]string, len(cols))
	for i, c := range cols {
		colNames[i] = c.Name
	}

	src := &source{
		name:    e.Name(),
		table:   e.TableName(),
		columns: colNames,
		stores:  stores,
	}
	if opts.ProcessBatch == 0 {
		opts.ProcessBatch = e.ProcessBatch()
	}
	return worker.Run(ctx, database, src, e.Process, opts)
}

func Run(ctx context.Context, database *sql.DB, e Enricher, stores map[string]string, opts worker.Opts) (int, worker.Stats, error) {
	identified, err := RunIdentify(ctx, database, e)
	if err != nil {
		return 0, worker.Stats{}, err
	}
	stats, err := RunProcess(ctx, database, e, stores, opts)
	return identified, stats, err
}
