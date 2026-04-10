package mime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/davars/winnow/db"
	"github.com/davars/winnow/worker"
)

// Provider declares the mime_type and mime_checked_at columns on the files table.
type Provider struct{}

func (Provider) Name() string      { return "mime" }
func (Provider) TableName() string { return "files" }

func (Provider) Columns() []db.Column {
	return []db.Column{
		{Name: "mime_type", Type: "TEXT"},
		{Name: "mime_checked_at", Type: "TEXT"},
	}
}

func (Provider) Indexes() []db.Index { return nil }

// Source implements worker.WorkSource for MIME detection.
type Source struct {
	Stores map[string]string // store name → directory path
}

// FetchBatch returns files needing MIME detection: mime_checked_at is NULL or
// mime_checked_at < mod_time, and not missing.
func (s *Source) FetchBatch(ctx context.Context, database *sql.DB, limit int) ([]worker.WorkItem, error) {
	rows, err := database.QueryContext(ctx,
		`SELECT id, store, path FROM files
		 WHERE (mime_checked_at IS NULL OR mime_checked_at < mod_time) AND missing = 0
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

// WriteBatch writes MIME results and logs errors in a single transaction.
func (s *Source) WriteBatch(ctx context.Context, database *sql.DB, results []worker.WorkResult) error {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	updateStmt, err := tx.PrepareContext(ctx,
		`UPDATE files SET mime_type = ?, mime_checked_at = ? WHERE id = ?`)
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

	name := Provider{}.Name()
	now := time.Now().UTC().Format(time.RFC3339)
	for _, r := range results {
		if r.Err != nil {
			if _, err := errStmt.ExecContext(ctx, r.Item.FileID, name, r.Err.Error(), now); err != nil {
				return err
			}
			// Set mime_checked_at so the file isn't re-fetched endlessly;
			// retried only if walk updates mod_time.
			if _, err := updateStmt.ExecContext(ctx, nil, now, r.Item.FileID); err != nil {
				return err
			}
			continue
		}
		mimeType := r.Values["mime_type"]
		checkedAt := r.Values["mime_checked_at"]
		if _, err := updateStmt.ExecContext(ctx, mimeType, checkedAt, r.Item.FileID); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// Process detects MIME types for a batch of items with a single `file` invocation.
// `file --brief` emits exactly one line per input path, in order, so we zip the
// output lines with the input items. On any exec-level failure, all items in the
// batch are marked errored.
func Process(ctx context.Context, items []worker.WorkItem) []worker.WorkResult {
	results := make([]worker.WorkResult, len(items))
	now := time.Now().UTC().Format(time.RFC3339)

	lines, err := detectBatch(ctx, items)
	if err != nil {
		for i, item := range items {
			results[i] = worker.WorkResult{Item: item, Err: err}
		}
		return results
	}
	if len(lines) != len(items) {
		err := fmt.Errorf("file returned %d lines for %d inputs", len(lines), len(items))
		for i, item := range items {
			results[i] = worker.WorkResult{Item: item, Err: err}
		}
		return results
	}

	for i, item := range items {
		mimeType := strings.TrimSpace(lines[i])
		// `file` exits 0 even when it cannot read the input (e.g., "regular file,
		// no read permission"). Valid MIME types always contain a slash, so that's
		// our signal to treat the line as an error rather than a result.
		if !strings.Contains(mimeType, "/") {
			results[i] = worker.WorkResult{
				Item: item,
				Err:  fmt.Errorf("detecting mime for %s: %s", item.Path, mimeType),
			}
			continue
		}
		results[i] = worker.WorkResult{
			Item: item,
			Values: map[string]any{
				"mime_type":       mimeType,
				"mime_checked_at": now,
			},
		}
	}
	return results
}

// detectBatch invokes `file --mime-type --brief -- <paths...>` and returns one
// output line per input path.
func detectBatch(ctx context.Context, items []worker.WorkItem) ([]string, error) {
	args := make([]string, 0, len(items)+3)
	args = append(args, "--mime-type", "--brief", "--")
	for _, item := range items {
		args = append(args, item.Path)
	}
	cmd := exec.CommandContext(ctx, "file", args...)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("file exited %d: %s", exitErr.ExitCode(), strings.TrimSpace(string(exitErr.Stderr)))
		}
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("file binary not found on PATH: %w", err)
		}
		return nil, err
	}
	return strings.Split(strings.TrimRight(string(out), "\n"), "\n"), nil
}

// DefaultProcessBatch is the number of files passed to a single `file` invocation.
// Larger batches amortize fork+exec + magic-DB-load overhead; the upper bound is
// just ARG_MAX.
const DefaultProcessBatch = 64

// Run sets up schema, then runs the worker pool for MIME detection.
func Run(ctx context.Context, database *sql.DB, stores map[string]string, opts worker.Opts) (worker.Stats, error) {
	if err := db.EnsureSchema(database, Provider{}); err != nil {
		return worker.Stats{}, fmt.Errorf("ensuring mime schema: %w", err)
	}
	if opts.ProcessBatch == 0 {
		opts.ProcessBatch = DefaultProcessBatch
	}

	source := &Source{Stores: stores}
	return worker.Run(ctx, database, source, Process, opts)
}
