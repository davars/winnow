// Package organize copies image and video files from raw/ to
// clean/media/YYYY/MM/<name>.<ext> based on their best-available capture
// timestamp. See ../PLAN.md for design.
package organize

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/davars/winnow/config"
	"github.com/davars/winnow/enricher"
)

// Opts configures a Run.
type Opts struct {
	RemoveOriginals bool
	DryRun          bool
	// MimeTypes restricts which candidates are considered. Defaults to
	// enricher.EXIFMimeTypes when empty.
	MimeTypes []string
	// Now is used for operations.executed_at so tests can pin timestamps.
	Now func() time.Time
	// Out receives progress / dry-run lines; defaults to os.Stdout.
	Out io.Writer
}

// Stats summarizes a Run.
type Stats struct {
	Considered int
	Organized  int
	Skipped    int
	Collided   int
	Errors     int
	Duration   time.Duration
}

// errDstCollision is returned by copyFile when the destination already exists.
var errDstCollision = errors.New("destination already exists")

// Run organizes raw-store media into clean/media/YYYY/MM/. Idempotent: files
// whose content hash already has a clean-store row are skipped.
func Run(ctx context.Context, db *sql.DB, cfg *config.Config, opts Opts) (Stats, error) {
	var stats Stats
	start := time.Now()
	defer func() { stats.Duration = time.Since(start) }()

	loc, err := cfg.Location()
	if err != nil {
		return stats, err
	}

	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	mimeTypes := opts.MimeTypes
	if len(mimeTypes) == 0 {
		mimeTypes = enricher.EXIFMimeTypes
	}

	// Fail early if the mime enricher hasn't run.
	var mimeCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mime`).Scan(&mimeCount); err != nil {
		return stats, fmt.Errorf("mime table not available — run `winnow mime` first: %w", err)
	}

	candidates, err := fetchCandidates(ctx, db, mimeTypes)
	if err != nil {
		return stats, err
	}
	stats.Considered = len(candidates)

	for _, c := range candidates {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		if err := organizeOne(ctx, db, cfg, loc, c, opts, &stats); err != nil {
			stats.Errors++
			logFileError(ctx, db, c.id, err, opts.Now())
		}
	}

	return stats, nil
}

type candidate struct {
	id      int64
	sha256  string
	path    string
	size    int64
	modTime string
	exif    sql.NullString
}

func fetchCandidates(ctx context.Context, db *sql.DB, mimeTypes []string) ([]candidate, error) {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(mimeTypes)), ",")
	query := fmt.Sprintf(`
		SELECT f.id, f.sha256, f.path, f.size, f.mod_time, e.data
		FROM files f
		JOIN mime m ON m.hash = f.sha256
		LEFT JOIN exif e ON e.hash = f.sha256
		WHERE f.store = 'raw' AND f.missing = 0 AND f.sha256 IS NOT NULL
		  AND m.mime_type IN (%s)
		ORDER BY f.path
	`, placeholders)

	args := make([]any, len(mimeTypes))
	for i, m := range mimeTypes {
		args[i] = m
	}

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("fetching candidates: %w", err)
	}
	defer rows.Close()

	var out []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.sha256, &c.path, &c.size, &c.modTime, &c.exif); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// organizeOne applies the full per-file flow (timestamp → dest → idempotency
// check → copy → DB tx). Returns a non-nil error for anything that should be
// logged to process_errors; nil for successful organize, already-clean skip,
// or destination collision (which are counted separately in stats).
func organizeOne(ctx context.Context, db *sql.DB, cfg *config.Config, loc *time.Location, c candidate, opts Opts, stats *Stats) error {
	exifJSON := ""
	if c.exif.Valid {
		exifJSON = c.exif.String
	}
	captureUTC, source, err := captureTime(exifJSON, c.modTime, loc)
	if err != nil {
		return err
	}

	relDst := destinationPath(captureUTC, c.sha256, c.path)
	reason := "source: " + source

	// Skip if another file with this hash is already in clean.
	var exists int
	err = db.QueryRowContext(ctx,
		`SELECT 1 FROM files WHERE store='clean' AND sha256=? AND missing=0 LIMIT 1`,
		c.sha256,
	).Scan(&exists)
	if err == nil {
		stats.Skipped++
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("checking clean store for hash: %w", err)
	}

	srcAbs := filepath.Join(cfg.RawDir, c.path)
	dstAbs := filepath.Join(cfg.CleanDir, relDst)

	if opts.DryRun {
		fmt.Fprintf(opts.Out, "ORGANIZE  raw/%s -> clean/%s  (%s)\n", c.path, relDst, reason)
		return nil
	}

	if err := copyFile(srcAbs, dstAbs); err != nil {
		if errors.Is(err, errDstCollision) {
			stats.Collided++
			return nil
		}
		return fmt.Errorf("copy: %w", err)
	}

	removed := false
	if opts.RemoveOriginals {
		if rmErr := os.Remove(srcAbs); rmErr == nil {
			removed = true
		} else {
			// Keep the clean copy; log and fall back to copy-only.
			logFileError(ctx, db, c.id, fmt.Errorf("remove raw original: %w", rmErr), opts.Now())
		}
	}

	if err := writeDBChanges(ctx, db, c, relDst, reason, removed, opts.Now()); err != nil {
		return fmt.Errorf("db update: %w", err)
	}
	stats.Organized++
	return nil
}

// writeDBChanges inserts the clean row, conditionally deletes the raw row, and
// logs an operations entry in a single transaction.
func writeDBChanges(ctx context.Context, db *sql.DB, c candidate, relDst, reason string, removed bool, now time.Time) error {
	nowStr := now.UTC().Format(time.RFC3339)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
		INSERT INTO files (store, path, size, mod_time, found_at, reconciled_at, missing, sha256, hashed_at)
		VALUES ('clean', ?, ?, ?, ?, ?, 0, ?, ?)
	`, relDst, c.size, c.modTime, nowStr, nowStr, c.sha256, nowStr)
	if err != nil {
		return err
	}
	cleanID, err := res.LastInsertId()
	if err != nil {
		return err
	}

	if removed {
		if _, err := tx.ExecContext(ctx, `DELETE FROM files WHERE id = ?`, c.id); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO operations (file_id, src_store, src_path, dst_store, dst_path, rule, reason, executed_at)
		VALUES (?, 'raw', ?, 'clean', ?, 'organize', ?, ?)
	`, cleanID, c.path, relDst, reason, nowStr); err != nil {
		return err
	}

	return tx.Commit()
}

// copyFile copies src to dst with O_EXCL on the destination (never overwrites)
// and preserves the source's mode. Creates intermediate directories. Returns
// errDstCollision if dst already exists.
func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	info, err := srcFile.Stat()
	if err != nil {
		return err
	}

	dstFile, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode())
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return errDstCollision
		}
		return err
	}

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		dstFile.Close()
		os.Remove(dst)
		return err
	}
	if err := dstFile.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	return nil
}

// logFileError records an error to process_errors without interrupting the
// run. Errors persisting the error (e.g. DB closed) are silently swallowed.
func logFileError(ctx context.Context, db *sql.DB, fileID int64, err error, now time.Time) {
	_, _ = db.ExecContext(ctx, `
		INSERT INTO process_errors (file_id, rule, error, occurred_at)
		VALUES (?, 'organize', ?, ?)
	`, fileID, err.Error(), now.UTC().Format(time.RFC3339))
}
