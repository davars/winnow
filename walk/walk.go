package walk

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/davars/winnow/config"
)

// Stats summarizes what a walk did.
type Stats struct {
	FilesFound   int
	DirsUpserted int
	DirsDeleted  int
}

// Run walks all configured stores and populates/updates the files and
// directories tables. It returns aggregate statistics across all stores.
func Run(ctx context.Context, db *sql.DB, cfg *config.Config) (Stats, error) {
	var total Stats
	for store, dir := range cfg.Stores() {
		s, err := walkStore(ctx, db, store, dir)
		if err != nil {
			return total, fmt.Errorf("walking store %q: %w", store, err)
		}
		total.FilesFound += s.FilesFound
		total.DirsUpserted += s.DirsUpserted
		total.DirsDeleted += s.DirsDeleted
	}
	return total, nil
}

type dirStats struct {
	fileCount int64
	totalSize int64
}

// isSkippable reports whether a filesystem error should be logged and
// walked past rather than aborting the whole walk. Permission errors
// (EACCES/EPERM) are common on backup drives — e.g. Synology @eaDir,
// macOS metadata, or cross-user directories — and should not stop a walk.
// Files that vanish mid-walk (ENOENT) are also harmless.
func isSkippable(err error) bool {
	return errors.Is(err, fs.ErrPermission) || errors.Is(err, fs.ErrNotExist)
}

func walkStore(ctx context.Context, db *sql.DB, store, baseDir string) (Stats, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	var stats Stats

	baseDir = filepath.Clean(baseDir)
	prefixLen := len(baseDir) + 1 // +1 for path separator

	dirs := make(map[string]*dirStats)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return stats, err
	}
	defer tx.Rollback()

	fileStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO files (store, path, size, mod_time, found_at, reconciled_at, missing)
		VALUES (?, ?, ?, ?, ?, ?, 0)
		ON CONFLICT(store, path) DO UPDATE SET
			size          = excluded.size,
			mod_time      = excluded.mod_time,
			reconciled_at = excluded.reconciled_at,
			missing       = 0
	`)
	if err != nil {
		return stats, err
	}
	defer fileStmt.Close()

	err = filepath.WalkDir(baseDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if isSkippable(err) {
				fmt.Fprintf(os.Stderr, "walk: skipping %s: %v\n", path, err)
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			return err
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}

		if d.IsDir() {
			// Register every directory visited so empty directories appear
			// in the table. The junk rule relies on this to propose removing
			// `file_count = 0` directories. File iteration below will
			// increment the counts for ancestor directories.
			var rel string
			if path == baseDir {
				rel = "."
			} else {
				rel = path[prefixLen:]
			}
			if _, ok := dirs[rel]; !ok {
				dirs[rel] = &dirStats{}
			}
			return nil
		}

		if !d.Type().IsRegular() {
			return nil
		}

		rel := path[prefixLen:]

		info, err := d.Info()
		if err != nil {
			if isSkippable(err) {
				fmt.Fprintf(os.Stderr, "walk: skipping %s: %v\n", path, err)
				return nil
			}
			return err
		}

		modTime := info.ModTime().UTC().Format(time.RFC3339)
		size := info.Size()

		if _, err := fileStmt.ExecContext(ctx, store, rel, size, modTime, now, now); err != nil {
			return fmt.Errorf("upserting file %s: %w", rel, err)
		}
		stats.FilesFound++

		// Accumulate directory stats for all ancestor directories.
		dir := filepath.Dir(rel)
		for {
			ds, ok := dirs[dir]
			if !ok {
				ds = &dirStats{}
				dirs[dir] = ds
			}
			ds.fileCount++
			ds.totalSize += size

			if dir == "." {
				break
			}
			dir = filepath.Dir(dir)
		}

		return nil
	})
	if err != nil {
		return stats, fmt.Errorf("walking %s: %w", baseDir, err)
	}

	dirStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO directories (store, path, file_count, total_size)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(store, path) DO UPDATE SET
			file_count = excluded.file_count,
			total_size = excluded.total_size
	`)
	if err != nil {
		return stats, err
	}
	defer dirStmt.Close()

	for dir, ds := range dirs {
		if _, err := dirStmt.ExecContext(ctx, store, dir, ds.fileCount, ds.totalSize); err != nil {
			return stats, fmt.Errorf("upserting directory %s: %w", dir, err)
		}
		stats.DirsUpserted++
	}

	rows, err := tx.QueryContext(ctx, `SELECT id, path FROM directories WHERE store = ?`, store)
	if err != nil {
		return stats, err
	}
	var staleIDs []int64
	for rows.Next() {
		var id int64
		var path string
		if err := rows.Scan(&id, &path); err != nil {
			rows.Close()
			return stats, err
		}
		if _, seen := dirs[path]; !seen {
			staleIDs = append(staleIDs, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return stats, err
	}

	for _, id := range staleIDs {
		if _, err := tx.ExecContext(ctx, `DELETE FROM directories WHERE id = ?`, id); err != nil {
			return stats, fmt.Errorf("deleting stale directory: %w", err)
		}
		stats.DirsDeleted++
	}

	return stats, tx.Commit()
}
