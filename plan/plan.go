// Package plan defines file-organization operations produced by rules and
// executes them against the filesystem and database.
package plan

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// OpKind identifies the type of an Op.
type OpKind int

const (
	OpClean     OpKind = iota // move file from src store to clean
	OpTrash                   // move file from src store to trash
	OpRemoveDir               // remove an empty (or soon-to-be-empty) directory
)

func (k OpKind) String() string {
	switch k {
	case OpClean:
		return "CLEAN"
	case OpTrash:
		return "TRASH"
	case OpRemoveDir:
		return "RMDIR"
	default:
		return fmt.Sprintf("OpKind(%d)", int(k))
	}
}

// Op is a single proposed file operation.
type Op struct {
	Kind     OpKind
	FileID   int64 // files.id — 0 for OpRemoveDir
	DirID    int64 // directories.id — 0 for file ops
	SrcStore string
	SrcPath  string
	DstStore string // empty for OpRemoveDir
	DstPath  string // empty for OpRemoveDir
	Rule     string
	Reason   string
}

// Plan is a sequence of proposed ops.
type Plan struct {
	Ops []Op
}

// Print writes a human-readable plan listing to w, grouped by rule.
func (p *Plan) Print(w io.Writer) {
	if len(p.Ops) == 0 {
		fmt.Fprintln(w, "No operations proposed.")
		return
	}

	byRule := make(map[string][]Op)
	var order []string
	for _, op := range p.Ops {
		if _, ok := byRule[op.Rule]; !ok {
			order = append(order, op.Rule)
		}
		byRule[op.Rule] = append(byRule[op.Rule], op)
	}

	for _, r := range order {
		ops := byRule[r]
		fmt.Fprintf(w, "Rule: %s (%d ops)\n", r, len(ops))
		for _, op := range ops {
			switch op.Kind {
			case OpClean, OpTrash:
				fmt.Fprintf(w, "  %s  %s/%s -> %s/%s  (%s)\n",
					op.Kind, op.SrcStore, op.SrcPath, op.DstStore, op.DstPath, op.Reason)
			case OpRemoveDir:
				fmt.Fprintf(w, "  %s  %s/%s  (%s)\n",
					op.Kind, op.SrcStore, op.SrcPath, op.Reason)
			}
		}
	}
}

// ExecuteOpts configures Execute.
type ExecuteOpts struct {
	// Stores maps store name ("raw", "clean", "trash") to base directory path.
	Stores map[string]string
	// PreProcessHook, if non-empty, is executed once before any ops run.
	// A non-zero exit aborts execution.
	PreProcessHook string
}

// Stats summarizes what Execute did.
type Stats struct {
	Succeeded int
	Failed    int
}

// dbBatchSize is the number of ops whose DB updates are committed in a single
// transaction. Filesystem work within a batch still happens one op at a time;
// batching just avoids one fsync-per-op on the DB side, which otherwise
// dominates wall time on million-file runs.
const dbBatchSize = 500

// Execute runs the plan. Ops are ordered so file moves happen before directory
// removals, and directories are removed deepest-first. Individual op failures
// are logged to process_errors and skipped. A failing pre_process_hook aborts
// execution before any ops run.
func Execute(ctx context.Context, db *sql.DB, p *Plan, opts ExecuteOpts) (Stats, error) {
	var stats Stats

	if opts.PreProcessHook != "" {
		if err := runHook(ctx, opts.PreProcessHook, opts.Stores); err != nil {
			return stats, err
		}
	}

	ordered := sortOps(p.Ops)

	for start := 0; start < len(ordered); start += dbBatchSize {
		end := min(start+dbBatchSize, len(ordered))
		batchStats, err := executeBatch(ctx, db, ordered[start:end], opts.Stores)
		stats.Succeeded += batchStats.Succeeded
		stats.Failed += batchStats.Failed
		if err != nil {
			return stats, err
		}
	}

	return stats, nil
}

// opResult pairs an op with the filesystem-side error from trying to apply
// it. A nil Err means the FS side succeeded and the op's DB updates should be
// applied as part of the batch's commit.
type opResult struct {
	Op  Op
	Err error
}

// executeBatch applies a slice of already-ordered ops. Filesystem work happens
// one op at a time (so partial progress is preserved across errors), then all
// resulting DB writes — successes and process_errors entries alike — are
// flushed in a single transaction. A failure preparing or committing the
// transaction aborts Execute since the FS work is already done and has no
// corresponding audit trail.
func executeBatch(ctx context.Context, db *sql.DB, ops []Op, stores map[string]string) (Stats, error) {
	results := make([]opResult, len(ops))
	for i, op := range ops {
		results[i] = opResult{Op: op, Err: performFS(op, stores)}
	}

	var stats Stats
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return stats, err
	}
	defer tx.Rollback()

	now := time.Now().UTC().Format(time.RFC3339)
	for _, r := range results {
		if r.Err != nil {
			if err := logProcessErrorTx(ctx, tx, r.Op, r.Err, now); err != nil {
				return stats, fmt.Errorf("logging error for %s %s/%s: %w (original: %v)",
					r.Op.Kind, r.Op.SrcStore, r.Op.SrcPath, err, r.Err)
			}
			stats.Failed++
			continue
		}
		if err := recordOpSuccessTx(ctx, tx, r.Op, now); err != nil {
			return stats, fmt.Errorf("recording success for %s %s/%s: %w",
				r.Op.Kind, r.Op.SrcStore, r.Op.SrcPath, err)
		}
		stats.Succeeded++
	}

	if err := tx.Commit(); err != nil {
		return stats, err
	}
	return stats, nil
}

// sortOps returns ops ordered for safe execution: all file ops first, then
// directory removals deepest-first. Stable so tests are deterministic.
func sortOps(ops []Op) []Op {
	out := make([]Op, len(ops))
	copy(out, ops)
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := rank(out[i]), rank(out[j])
		if ri != rj {
			return ri < rj
		}
		if out[i].Kind == OpRemoveDir && out[j].Kind == OpRemoveDir {
			di := pathDepth(out[i].SrcPath)
			dj := pathDepth(out[j].SrcPath)
			if di != dj {
				return di > dj
			}
		}
		return false
	})
	return out
}

func rank(op Op) int {
	if op.Kind == OpRemoveDir {
		return 1
	}
	return 0
}

func pathDepth(p string) int {
	if p == "." || p == "" {
		return 0
	}
	return strings.Count(p, "/") + 1
}

// performFS applies the filesystem side of an op. DB updates happen later in
// the batch's shared transaction.
func performFS(op Op, stores map[string]string) error {
	switch op.Kind {
	case OpClean, OpTrash:
		srcDir, ok := stores[op.SrcStore]
		if !ok {
			return fmt.Errorf("unknown src store %q", op.SrcStore)
		}
		dstDir, ok := stores[op.DstStore]
		if !ok {
			return fmt.Errorf("unknown dst store %q", op.DstStore)
		}
		return moveFile(filepath.Join(srcDir, op.SrcPath), filepath.Join(dstDir, op.DstPath))

	case OpRemoveDir:
		srcDir, ok := stores[op.SrcStore]
		if !ok {
			return fmt.Errorf("unknown src store %q", op.SrcStore)
		}
		// os.Remove only succeeds on empty directories, which is exactly what
		// we want — never rm -rf something the user didn't explicitly plan.
		// A missing target means the DB was stale; treat as success so the
		// row gets cleaned up below.
		if err := os.Remove(filepath.Join(srcDir, op.SrcPath)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil

	default:
		return fmt.Errorf("unknown op kind %v", op.Kind)
	}
}

// recordOpSuccessTx writes the DB-side effects of a successfully-applied op.
func recordOpSuccessTx(ctx context.Context, tx *sql.Tx, op Op, now string) error {
	switch op.Kind {
	case OpClean, OpTrash:
		if _, err := tx.ExecContext(ctx,
			`UPDATE files SET store = ?, path = ? WHERE id = ?`,
			op.DstStore, op.DstPath, op.FileID,
		); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO operations
			  (file_id, src_store, src_path, dst_store, dst_path, rule, reason, executed_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, op.FileID, op.SrcStore, op.SrcPath, op.DstStore, op.DstPath, op.Rule, op.Reason, now)
		return err

	case OpRemoveDir:
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO operations
			  (dir_id, src_store, src_path, rule, reason, executed_at)
			VALUES (?, ?, ?, ?, ?, ?)
		`, op.DirID, op.SrcStore, op.SrcPath, op.Rule, op.Reason, now,
		); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`DELETE FROM directories WHERE id = ?`, op.DirID,
		)
		return err
	}
	return fmt.Errorf("unknown op kind %v", op.Kind)
}

// moveFile renames src to dst, creating the destination directory tree as
// needed. On EXDEV (cross-filesystem), falls back to copy+remove.
func moveFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	} else if !isCrossDevice(err) {
		return err
	}
	return copyAndRemove(src, dst)
}

func isCrossDevice(err error) bool {
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		return errors.Is(linkErr.Err, syscall.EXDEV)
	}
	return errors.Is(err, syscall.EXDEV)
}

func copyAndRemove(src, dst string) error {
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

	return os.Remove(src)
}

func runHook(ctx context.Context, hook string, stores map[string]string) error {
	cmd := exec.CommandContext(ctx, hook)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), storeEnvVars(stores)...)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pre_process_hook %q failed: %w", hook, err)
	}
	return nil
}

// storeEnvVars returns environment variables for each store directory,
// e.g. WINNOW_RAW_DIR=/path/to/raw.
func storeEnvVars(stores map[string]string) []string {
	env := make([]string, 0, len(stores))
	for name, dir := range stores {
		env = append(env, fmt.Sprintf("WINNOW_%s_DIR=%s", strings.ToUpper(name), dir))
	}
	return env
}

func logProcessErrorTx(ctx context.Context, tx *sql.Tx, op Op, execErr error, now string) error {
	var fileID, dirID any
	if op.FileID != 0 {
		fileID = op.FileID
	}
	if op.DirID != 0 {
		dirID = op.DirID
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO process_errors (file_id, dir_id, rule, error, occurred_at)
		VALUES (?, ?, ?, ?, ?)
	`, fileID, dirID, op.Rule, execErr.Error(), now)
	return err
}
