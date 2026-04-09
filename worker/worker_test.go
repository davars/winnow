package worker

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// testSource is a WorkSource backed by a simple SQLite table.
type testSource struct {
	identity string // for process_errors logging
}

func (s *testSource) FetchBatch(ctx context.Context, db *sql.DB, limit int) ([]WorkItem, error) {
	rows, err := db.QueryContext(ctx,
		"SELECT id, path FROM work_items WHERE done = 0 LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []WorkItem
	for rows.Next() {
		var item WorkItem
		if err := rows.Scan(&item.FileID, &item.Path); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *testSource) WriteBatch(ctx context.Context, db *sql.DB, results []WorkResult) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, r := range results {
		if r.Err != nil {
			_, err := tx.ExecContext(ctx,
				"INSERT INTO process_errors (file_id, enricher, error, occurred_at) VALUES (?, ?, ?, ?)",
				r.Item.FileID, s.identity, r.Err.Error(), time.Now().UTC().Format(time.RFC3339))
			if err != nil {
				return err
			}
			// Mark as done so it's not re-fetched.
			_, err = tx.ExecContext(ctx, "UPDATE work_items SET done = 1 WHERE id = ?", r.Item.FileID)
			if err != nil {
				return err
			}
			continue
		}
		result, ok := r.Values["result"]
		if !ok {
			panic(fmt.Sprintf("WriteBatch: unexpected keys in Values: %v", keysOf(r.Values)))
		}
		_, err := tx.ExecContext(ctx,
			"UPDATE work_items SET done = 1, result = ? WHERE id = ?",
			result, r.Item.FileID)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

func keysOf(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func openInMemoryDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec(`
		CREATE TABLE work_items (
			id INTEGER PRIMARY KEY,
			path TEXT NOT NULL,
			done INTEGER NOT NULL DEFAULT 0,
			result TEXT
		);
		CREATE TABLE process_errors (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			file_id     INTEGER,
			dir_id      INTEGER,
			enricher    TEXT,
			rule        TEXT,
			error       TEXT NOT NULL,
			occurred_at TEXT NOT NULL
		);
	`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func insertWorkItems(t *testing.T, db *sql.DB, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		_, err := db.Exec("INSERT INTO work_items (path) VALUES (?)",
			fmt.Sprintf("/tmp/file%d.txt", i))
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestRunBasic(t *testing.T) {
	db := openInMemoryDB(t)
	insertWorkItems(t, db, 10)

	process := func(ctx context.Context, items []WorkItem) []WorkResult {
		results := make([]WorkResult, len(items))
		for i, item := range items {
			results[i] = WorkResult{
				Item:   item,
				Values: map[string]any{"result": "processed:" + item.Path},
			}
		}
		return results
	}

	stats, err := Run(context.Background(), db, &testSource{identity: "test"}, process, Opts{
		Workers:      2,
		ProcessBatch: 3,
	})
	if err != nil {
		t.Fatal(err)
	}

	if stats.Processed != 10 {
		t.Errorf("processed = %d, want 10", stats.Processed)
	}
	if stats.Errors != 0 {
		t.Errorf("errors = %d, want 0", stats.Errors)
	}
	if stats.Duration <= 0 {
		t.Error("duration should be positive")
	}

	// Verify all items are done.
	var remaining int
	db.QueryRow("SELECT COUNT(*) FROM work_items WHERE done = 0").Scan(&remaining)
	if remaining != 0 {
		t.Errorf("remaining undone = %d, want 0", remaining)
	}
}

func TestRunErrorResults(t *testing.T) {
	db := openInMemoryDB(t)
	insertWorkItems(t, db, 5)

	process := func(ctx context.Context, items []WorkItem) []WorkResult {
		results := make([]WorkResult, len(items))
		for i, item := range items {
			if item.FileID%2 == 0 {
				results[i] = WorkResult{
					Item: item,
					Err:  fmt.Errorf("failed to process %s", item.Path),
				}
			} else {
				results[i] = WorkResult{
					Item:   item,
					Values: map[string]any{"result": "ok"},
				}
			}
		}
		return results
	}

	stats, err := Run(context.Background(), db, &testSource{identity: "test_enricher"}, process, Opts{
		Workers: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	if stats.Errors == 0 {
		t.Error("expected some errors")
	}
	if stats.Processed == 0 {
		t.Error("expected some processed")
	}
	if stats.Processed+stats.Errors != 5 {
		t.Errorf("processed(%d) + errors(%d) != 5", stats.Processed, stats.Errors)
	}

	// Verify errors were logged to process_errors.
	var errCount int
	db.QueryRow("SELECT COUNT(*) FROM process_errors").Scan(&errCount)
	if errCount != stats.Errors {
		t.Errorf("process_errors count = %d, want %d", errCount, stats.Errors)
	}

	// Verify the enricher identity is recorded.
	var enricher string
	db.QueryRow("SELECT enricher FROM process_errors LIMIT 1").Scan(&enricher)
	if enricher != "test_enricher" {
		t.Errorf("enricher = %q, want %q", enricher, "test_enricher")
	}
}

func TestRunResultCountPanic(t *testing.T) {
	db := openInMemoryDB(t)
	insertWorkItems(t, db, 3)

	process := func(ctx context.Context, items []WorkItem) []WorkResult {
		// Return wrong number of results.
		return []WorkResult{{Item: items[0], Values: map[string]any{"result": "ok"}}}
	}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for wrong result count")
		}
		msg := fmt.Sprint(r)
		if !strings.Contains(msg, "results") {
			t.Errorf("panic message %q should mention results", msg)
		}
	}()

	Run(context.Background(), db, &testSource{identity: "test"}, process, Opts{
		Workers:      1,
		ProcessBatch: 3,
	})
}

func TestRunContextCancellation(t *testing.T) {
	db := openInMemoryDB(t)
	insertWorkItems(t, db, 100)

	ctx, cancel := context.WithCancel(context.Background())

	callCount := 0
	process := func(ctx context.Context, items []WorkItem) []WorkResult {
		callCount++
		if callCount >= 2 {
			cancel()
		}
		results := make([]WorkResult, len(items))
		for i, item := range items {
			results[i] = WorkResult{
				Item:   item,
				Values: map[string]any{"result": "ok"},
			}
		}
		return results
	}

	stats, err := Run(ctx, db, &testSource{identity: "test"}, process, Opts{
		Workers:      1,
		ProcessBatch: 1,
		BatchSize:    5,
	})

	if err == nil {
		// Might complete if all items processed before cancellation detected.
		if stats.Processed < 5 {
			t.Errorf("expected at least 5 processed before cancellation, got %d", stats.Processed)
		}
	} else if !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Errorf("expected error containing %q, got: %v", context.Canceled, err)
	}
}

func TestRunEmptySource(t *testing.T) {
	db := openInMemoryDB(t)
	// No items inserted.

	process := func(ctx context.Context, items []WorkItem) []WorkResult {
		t.Fatal("process should not be called with empty source")
		return nil
	}

	stats, err := Run(context.Background(), db, &testSource{identity: "test"}, process, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Processed != 0 {
		t.Errorf("processed = %d, want 0", stats.Processed)
	}
}

func TestRunStatsTracking(t *testing.T) {
	db := openInMemoryDB(t)
	insertWorkItems(t, db, 20)

	process := func(ctx context.Context, items []WorkItem) []WorkResult {
		results := make([]WorkResult, len(items))
		for i, item := range items {
			if item.FileID <= 5 {
				results[i] = WorkResult{Item: item, Err: fmt.Errorf("fail")}
			} else {
				results[i] = WorkResult{
					Item:   item,
					Values: map[string]any{"result": "ok"},
				}
			}
		}
		return results
	}

	stats, err := Run(context.Background(), db, &testSource{identity: "test"}, process, Opts{
		Workers:      2,
		ProcessBatch: 5,
	})
	if err != nil {
		t.Fatal(err)
	}

	if stats.Processed != 15 {
		t.Errorf("processed = %d, want 15", stats.Processed)
	}
	if stats.Errors != 5 {
		t.Errorf("errors = %d, want 5", stats.Errors)
	}
}

func TestRunDefaultOpts(t *testing.T) {
	db := openInMemoryDB(t)
	insertWorkItems(t, db, 3)

	process := func(ctx context.Context, items []WorkItem) []WorkResult {
		results := make([]WorkResult, len(items))
		for i, item := range items {
			results[i] = WorkResult{
				Item:   item,
				Values: map[string]any{"result": "ok"},
			}
		}
		return results
	}

	// Use zero-value opts — defaults should apply.
	stats, err := Run(context.Background(), db, &testSource{identity: "test"}, process, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Processed != 3 {
		t.Errorf("processed = %d, want 3", stats.Processed)
	}
}
