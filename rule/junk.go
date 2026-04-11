package rule

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/davars/winnow/config"
	"github.com/davars/winnow/plan"
)

// Junk identifies filesystem junk (OS/indexing metadata, resource forks) in
// the raw store and trashes it. It also proposes removing empty directories
// from every store.
//
// Two kinds of file patterns are recognized:
//
//  1. Exact filename matches (`.DS_Store`, `Thumbs.db`, `desktop.ini`, …)
//     and the macOS "._" resource-fork prefix.
//  2. Directory-component matches (`@eaDir`, `.Spotlight-V100`, `.Trashes`,
//     …): any file whose path contains a junk component, and the
//     containing directory itself.
type Junk struct{}

func (Junk) Name() string { return "junk" }

// junkFileNames are exact basename matches that mean "OS metadata, always trash".
var junkFileNames = map[string]bool{
	".DS_Store":             true,
	".AppleDouble":          true,
	".AppleDesktop":         true,
	".LSOverride":           true,
	".localized":            true,
	"Thumbs.db":             true,
	"Thumbs.db:encryptable": true,
	"ehthumbs.db":           true,
	"ehthumbs_vista.db":     true,
	"desktop.ini":           true,
}

// junkDirNames are exact directory-name matches. Any path containing one of
// these as a component (or a directory with this name) is junk.
var junkDirNames = map[string]bool{
	"@eaDir":                  true, // Synology thumbnails/indexing
	".Spotlight-V100":         true, // macOS Spotlight index
	".Trashes":                true, // macOS removable-volume trash
	".TemporaryItems":         true, // macOS temp
	".fseventsd":              true, // macOS fsevents journal
	".DocumentRevisions-V100": true, // macOS document revisions
	"__MACOSX":                true, // unzipped macOS archive metadata
}

// junkFileReason returns a non-empty reason if the file at the given relative
// path should be treated as junk, either by filename or by containing
// directory component.
func junkFileReason(path string) string {
	base := filepath.Base(path)
	if junkFileNames[base] {
		return "junk file: " + base
	}
	if strings.HasPrefix(base, "._") {
		return "macOS resource fork"
	}
	if reason := matchJunkComponent(filepath.Dir(path)); reason != "" {
		return reason
	}
	return ""
}

// junkDirReason returns a non-empty reason if a directory at the given
// relative path is (or is under) a junk directory name.
func junkDirReason(path string) string {
	return matchJunkComponent(path)
}

func matchJunkComponent(path string) string {
	if path == "" || path == "." {
		return ""
	}
	for part := range strings.SplitSeq(filepath.ToSlash(path), "/") {
		if junkDirNames[part] {
			return "junk directory: " + part
		}
	}
	return ""
}

// Evaluate scans the raw store for junk files and junk/empty directories and
// returns the proposed ops. File patterns apply only to the raw store (junk
// is a file rule and file rules only operate on raw). Empty-directory cleanup
// applies to all stores.
func (j Junk) Evaluate(ctx context.Context, db *sql.DB, cfg *config.Config, claimed map[int64]bool) ([]plan.Op, error) {
	var ops []plan.Op

	fileOps, err := j.evaluateFiles(ctx, db, claimed)
	if err != nil {
		return nil, err
	}
	ops = append(ops, fileOps...)

	dirOps, err := j.evaluateDirs(ctx, db)
	if err != nil {
		return nil, err
	}
	ops = append(ops, dirOps...)

	return ops, nil
}

func (Junk) evaluateFiles(ctx context.Context, db *sql.DB, claimed map[int64]bool) ([]plan.Op, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, path FROM files WHERE store = 'raw' AND missing = 0`)
	if err != nil {
		return nil, fmt.Errorf("query raw files: %w", err)
	}
	defer rows.Close()

	var ops []plan.Op
	for rows.Next() {
		var id int64
		var path string
		if err := rows.Scan(&id, &path); err != nil {
			return nil, err
		}
		if claimed[id] {
			continue
		}
		reason := junkFileReason(path)
		if reason == "" {
			continue
		}
		ops = append(ops, plan.Op{
			Kind:     plan.OpTrash,
			FileID:   id,
			SrcStore: "raw",
			SrcPath:  path,
			DstStore: "trash",
			DstPath:  path,
			Rule:     "junk",
			Reason:   reason,
		})
	}
	return ops, rows.Err()
}

// evaluateDirs proposes OpRemoveDir for:
//   - directories in the raw store whose name (or a path component) matches a
//     junk pattern — even if not currently empty, since the file ops we just
//     proposed will empty them before execution reaches the dir op;
//   - directories in any store with file_count = 0 (already empty on disk).
//
// A single directory matching both conditions yields one op with the
// more-specific "junk directory" reason.
func (Junk) evaluateDirs(ctx context.Context, db *sql.DB) ([]plan.Op, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, store, path, file_count FROM directories`)
	if err != nil {
		return nil, fmt.Errorf("query directories: %w", err)
	}
	defer rows.Close()

	var ops []plan.Op
	for rows.Next() {
		var id int64
		var store, path string
		var count int64
		if err := rows.Scan(&id, &store, &path, &count); err != nil {
			return nil, err
		}
		// Never propose removing the root of a store — that's the store itself.
		if path == "." || path == "" {
			continue
		}
		var reason string
		if store == "raw" {
			reason = junkDirReason(path)
		}
		if reason == "" && count == 0 {
			reason = "empty directory"
		}
		if reason == "" {
			continue
		}
		ops = append(ops, plan.Op{
			Kind:     plan.OpRemoveDir,
			DirID:    id,
			SrcStore: store,
			SrcPath:  path,
			Rule:     "junk",
			Reason:   reason,
		})
	}
	return ops, rows.Err()
}
