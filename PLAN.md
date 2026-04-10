# Winnow — File Organization Tool

## Context

Cleaning up old backups on a Linux/ZFS server: duplicates, junk files (.DS_Store, Synology metadata), and media files needing metadata-based organization. The tool only moves/renames files — never deletes. A "trash dir" holds files staged for manual deletion. This is an ongoing process, not a one-shot operation — incremental processing is central to the design.

## Decisions

- **Go** (latest stable, 1.24), **SQLite** (pure-Go `modernc.org/sqlite`), **TOML** config
- **Nix flake** packages binary + runtime deps (exiftool, ffmpeg)
- **No FS abstraction.** Use `os` directly. ZFS-specific operations (snapshots) handled via a configurable pre-process hook.
- **Three direct deps:** `BurntSushi/toml`, `spf13/cobra`, `modernc.org/sqlite`
- **SQLite WAL mode** enabled at connection time for concurrent reads during writes.

## Data Model

### Two worlds: path-based and content-based

The `files` table is the core registry with a stable integer PK. sha256 is a column on `files` (added by schema management when the sha256 enricher registers). Content-based enrichers key on hash.

### Three layers of schema management

**Layer 1: Hardcoded CREATE TABLE** — core tables with known-at-compile-time schemas:
- `files` — filesystem registry
- `directories` — directory stats (recursive file count, cumulative size)
- `operations` — audit log of file movements
- `process_errors` — error log

These use standard `CREATE TABLE IF NOT EXISTS` statements in Go code.

**Layer 2: Templated CREATE TABLE** — enricher base tables with a fixed structure plus enricher-specific columns. The table name is interpolated into a template. **The table name must be validated against `^[a-zA-Z_][a-zA-Z0-9_]*$` at the interpolation site to prevent SQL injection.**

```sql
-- Template (table name interpolated):
CREATE TABLE IF NOT EXISTS {tableName} (
    hash         TEXT PRIMARY KEY,
    file_id      INTEGER NOT NULL REFERENCES files(id),
    processed_at TEXT
);
```

**Layer 3: Schema management (Columns + Indexes)** — handles the delta on top of base tables. Defined as a `SchemaProvider` interface in the `db` package:

```go
// SchemaProvider declares columns and indexes that schema management should ensure exist.
// Embedded by the Enricher interface; also implemented directly by built-in steps (sha256, MIME)
// that add columns to the files table.
type SchemaProvider interface {
    Name() string         // human-readable identifier (for logs, errors)
    TableName() string
    Columns() []Column    // enricher-specific only (not base schema)
    Indexes() []Index     // can target any table
}
```

- `Columns()` returns only columns beyond the base schema. Added via `ALTER TABLE ADD COLUMN`. All must be nullable (SQLite restriction: can't add NOT NULL without default to existing table).
- `Indexes()` manages indexes on **any** table via `CREATE INDEX IF NOT EXISTS` / `DROP INDEX IF EXISTS`. No ALTER TABLE restrictions. Can be used to add indexes to core tables too (e.g., a component that wants an index on `operations`).

Schema management compares declared state vs `PRAGMA table_info` / `PRAGMA index_list`, adds missing columns/indexes, and can drop stale indexes. Stale columns are attempted to be dropped via `ALTER TABLE DROP COLUMN`. If the drop fails (e.g., column is part of an index, or other SQLite restrictions), log a warning. The user decides whether to tolerate the warning or re-add the column to `Columns()`.

### DB Schema

```sql
PRAGMA journal_mode=WAL;

-- Layer 1: hardcoded core tables

CREATE TABLE IF NOT EXISTS files (
    id            INTEGER PRIMARY KEY,
    store         TEXT NOT NULL,        -- 'raw', 'clean', 'trash'
    path          TEXT NOT NULL,        -- relative to store's base dir
    size          INTEGER NOT NULL,
    mod_time      TEXT NOT NULL,        -- RFC3339
    found_at      TEXT NOT NULL,        -- RFC3339, when first walked
    reconciled_at TEXT NOT NULL,        -- RFC3339, updated on every walk that sees this file
    missing       INTEGER NOT NULL DEFAULT 0,  -- 1 if reconcile determined file is gone
    UNIQUE(store, path)
);

CREATE TABLE IF NOT EXISTS directories (
    id            INTEGER PRIMARY KEY,
    store         TEXT NOT NULL,        -- 'raw', 'clean', 'trash'
    path          TEXT NOT NULL,        -- relative to store's base dir
    file_count    INTEGER NOT NULL,     -- recursive count of files
    total_size    INTEGER NOT NULL,     -- cumulative size of files in bytes
    UNIQUE(store, path)
);
-- Walk uses UPSERT (INSERT ... ON CONFLICT(store,path) DO UPDATE) to keep IDs stable.
-- Directories no longer on disk are deleted. IDs in operations remain valid for audit.

-- Layer 3: columns added by schema management when enrichers register:
--   SHA-256 (Phase 6): sha256 TEXT, hashed_at TEXT
--   MIME (Phase 8): mime_type TEXT

CREATE TABLE IF NOT EXISTS operations (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    file_id     INTEGER REFERENCES files(id),       -- set for file ops
    dir_id      INTEGER REFERENCES directories(id), -- set for directory ops
    src_store   TEXT NOT NULL,
    src_path    TEXT NOT NULL,
    dst_store   TEXT,                               -- NULL for OpRemoveDir
    dst_path    TEXT,                               -- NULL for OpRemoveDir
    rule        TEXT NOT NULL,
    reason      TEXT,
    executed_at TEXT NOT NULL     -- RFC3339
);

CREATE TABLE IF NOT EXISTS process_errors (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    file_id     INTEGER,          -- NULL if error is not file-specific
    dir_id      INTEGER,          -- NULL if error is not directory-specific
    enricher    TEXT,             -- NULL if error is from a rule
    rule        TEXT,             -- NULL if error is from an enricher
    error       TEXT NOT NULL,
    occurred_at TEXT NOT NULL     -- RFC3339
);

-- Layer 2: enricher base tables (created from template when enricher registers)
-- Example: exif enricher (Phase 9)
--   Base: hash TEXT PRIMARY KEY, file_id, processed_at
--   Columns() adds: create_date TEXT, make TEXT, model TEXT
```

**Key properties:**
- `files.missing` is a boolean flag, separate from `store`. A file in store `'raw'` with `missing = 1` means "was in raw_dir, but the reconcile step couldn't find it on disk."
- `files.reconciled_at` is updated every time walk sees the file on disk. A separate reconcile step marks files as missing if `reconciled_at` is older than a configurable staleness threshold.
- Walk does **not** know about sha256. The sha256 enricher adds `sha256 TEXT` and `hashed_at TEXT` columns via schema management. It detects stale hashes by comparing `hashed_at` vs `mod_time` — if `mod_time` is newer than `hashed_at`, re-hash. Content-based enricher results keyed on the old hash become orphaned (no files reference that hash anymore) — a future cleanup step can prune them.
- `file_id` on enricher tables is historical/audit: "which file did we read to produce this data." It may point to a file that has since moved or gone missing. It is not a live join key for current state.
- `hash` is the PRIMARY KEY on enricher tables (not an implicit rowid).
- Moving a file (via rule execution) UPDATEs the `files` row (store + path). The stable `id` means no cascading updates needed.

**Progress queries:**
- Files needing sha256: `SELECT COUNT(*) FROM files WHERE (sha256 IS NULL OR hashed_at < mod_time) AND missing = 0`
- Enricher pending: `SELECT COUNT(*) FROM exif WHERE processed_at IS NULL`
- Enricher complete: `SELECT COUNT(*) FROM exif WHERE processed_at IS NOT NULL`
- Enricher candidates not yet identified: `SELECT COUNT(DISTINCT f.sha256) FROM files f WHERE f.sha256 IS NOT NULL AND f.sha256 NOT IN (SELECT hash FROM exif)`
- Duplicates: `SELECT sha256, COUNT(*) as n FROM files WHERE sha256 IS NOT NULL GROUP BY sha256 HAVING n > 1`
- Stale files needing reconciliation: `SELECT COUNT(*) FROM files WHERE reconciled_at < ? AND missing = 0`

## Processing Model: Enrichment Passes + Rules

### Worker Pool (core utility: `worker` package)

A generic batch-processing pool used by sha256, all content-based enrichers, and potentially rules. Processes one read-batch at a time with concurrent workers within the batch.

```
Coordinator loop:
  items := FetchBatch(db, batchSize)       ─── quick read
  if empty: done
  chunks := split(items, processBatch)
  fan out chunks to N workers via work chan
  for each chunk result from results chan:
    verify len(chunkResult) == len(chunk)  ─── panic if not
    append to allResults
  WriteBatch(db, allResults)               ─── single txn
  print progress if >7s since last
  repeat
```

Two goroutine roles: **coordinator** (fetch, distribute, collect, verify, write) and **N workers** (process chunks, send `[]WorkResult` per chunk). No separate writer goroutine — the coordinator does reads and writes since they never overlap within a batch.

```go
package worker

// WorkItem is a unit of work.
type WorkItem struct {
    Hash   string         // content hash (empty for sha256 step itself)
    FileID int64          // files.id
    Path   string         // absolute path to a readable file
}

// WorkResult is what a worker produces for one item.
type WorkResult struct {
    Item   WorkItem
    Values map[string]any // column values to write back
    Err    error          // non-nil → log to process_errors, skip this item
}

// WorkSource abstracts DB reads and writes for a specific job type.
type WorkSource interface {
    // FetchBatch returns the next batch of pending items.
    // Quick read, no long-lived transaction. Returns empty slice when done.
    FetchBatch(ctx context.Context, db *sql.DB, limit int) ([]WorkItem, error)

    // WriteBatch writes completed results in a single transaction.
    // Panics if any result's Values keys don't match declared columns.
    WriteBatch(ctx context.Context, db *sql.DB, results []WorkResult) error
}

// ProcessFunc processes a batch of items. Called by each worker goroutine.
// Must return exactly one WorkResult per input item.
// Runtime panics if len(results) != len(items).
type ProcessFunc func(ctx context.Context, items []WorkItem) []WorkResult

// Opts configures the worker pool.
type Opts struct {
    Workers      int // concurrent worker goroutines (default: runtime.NumCPU())
    BatchSize    int // items per DB fetch (default: Workers * ProcessBatch)
    ProcessBatch int // items per ProcessFunc call (default: 1)
}

// Stats is returned when the pool completes.
type Stats struct {
    Processed int
    Errors    int
    Duration  time.Duration
}

// Run executes the worker pool to completion or context cancellation.
// Prints progress to stdout if >7s since last update.
// Panics if ProcessFunc returns wrong number of results.
// Panics if WorkResult.Values keys don't match declared schema.
// On context cancellation: finishes in-flight chunk, writes completed results, returns.
func Run(ctx context.Context, db *sql.DB, source WorkSource, process ProcessFunc, opts Opts) (Stats, error)
```

**Channels:**
- Work channel: `chan []WorkItem`, buffered to `Workers`. Coordinator sends chunks, workers receive.
- Results channel: `chan []WorkResult`, buffered to `Workers`. Workers send one `[]WorkResult` per chunk (same length as input chunk — coordinator verifies count match before flattening).

**WorkSource implementations receive a store→dir map (from config) at construction time**, so `FetchBatch` can resolve relative paths to absolute paths for `WorkItem.Path`. Each `WriteBatch` implementation also knows its own identity, so it logs errors to `process_errors` with the correct `enricher` or `rule` column.

**How sha256 uses it:**
- `FetchBatch`: `SELECT id as file_id, '' as hash, store, path FROM files WHERE (sha256 IS NULL OR hashed_at < mod_time) AND missing = 0 LIMIT ?`
- `WriteBatch`: `UPDATE files SET sha256 = ?, hashed_at = ? WHERE id = ?`
- `ProcessFunc`: open file, stream through `crypto/sha256`, return `{"sha256": hexdigest, "hashed_at": now}`
- `Opts.ProcessBatch = 1`

**How exif enricher uses it:**
- `Identify`: uses `IdentifyByMimeType` with image MIME types (image/jpeg, image/heic, image/tiff, etc.)
- `FetchBatch`: `SELECT e.hash, e.file_id, f.store, f.path FROM exif e JOIN files f ON e.file_id = f.id WHERE e.processed_at IS NULL LIMIT ?`
- `WriteBatch`: `UPDATE exif SET processed_at = ?, create_date = ?, make = ?, model = ? WHERE hash = ?`
- `ProcessFunc`: run `exiftool -json file1 file2 ...`, parse JSON array, match results to items, return error results for any items exiftool skipped
- `Opts.ProcessBatch = 100`

**Path resolution for content-based enrichers:** FetchBatch picks the first non-missing file for each hash (natural query order — the choice is arbitrary since same hash = same content). If the file is unreadable at process time, the ProcessFunc queries the DB for other paths with the same hash and tries them. If all fail, return an error WorkResult.

### Phase A: Built-in steps

These write directly to the `files` table:

**walk** — Scans all configured stores (raw_dir, clean_dir, trash_dir), inserts new files, and updates `reconciled_at`:
- New files on disk but not in DB → INSERT with store + path + size + mod_time + reconciled_at
- Files already in DB → UPDATE reconciled_at (and size/mod_time if changed)
- Walk does not know about sha256 or any enricher columns. It only manages the hardcoded `files` columns.
- Walking all stores ensures that files moved by rules are reflected in the DB regardless of which store they land in.

**reconcile** — Marks files as missing based on staleness:
- `UPDATE files SET missing = 1 WHERE reconciled_at < ? AND missing = 0`
- Takes a configurable staleness threshold (default from `[reconcile] max_staleness` in config)

**sha256** — Uses the worker pool with `ProcessBatch = 1`. Declares `sha256 TEXT` and `hashed_at TEXT` columns on `files` via schema management. Processes files where `sha256 IS NULL OR hashed_at < mod_time` and `missing = 0`. Updates both `sha256` and `hashed_at` on completion.

### Phase B: Enrichment (two-pass per enricher)

Each enricher owns a table (PK on hash). Enrichment is two-pass:

1. **Identify** — Pure DB operation. Insert candidate rows with common columns populated (hash, file_id — first non-missing file for that hash) but `processed_at = NULL` and enricher-specific columns NULL. This materializes the work queue.
2. **Process** — The runtime builds a `WorkSource` from the enricher's schema declaration and runs the worker pool with the enricher's `ProcessFunc`.

```go
package enricher

// Column and Index types are defined in the db package (alongside SchemaProvider).
// Aliased here for convenience:
//   type Column = db.Column
//   type Index = db.Index

// Enricher processes files and stores content-derived metadata.
type Enricher interface {
    db.SchemaProvider  // embeds Name(), TableName(), Columns(), Indexes()

    // Common columns (hash PK, file_id, processed_at) are added automatically.

    // Identify inserts candidate rows for content that should be processed.
    // Pure DB operation. Returns count of new candidates added.
    // A helper function IdentifyByMimeType(db, table, mimeTypes) handles the common case.
    Identify(ctx context.Context, db *sql.DB) (int, error)

    // Process handles a batch of items. Batch size controlled by ProcessBatch().
    // Must return exactly len(items) results (runtime panics otherwise).
    Process(ctx context.Context, items []worker.WorkItem) []worker.WorkResult

    // ProcessBatch returns the preferred batch size for Process calls.
    ProcessBatch() int
}

// IdentifyByMimeType is a helper for the common case: insert candidates for all
// unique hashes of non-missing files matching the given MIME types that aren't already
// in the table. Picks the first non-missing file_id for each hash.
func IdentifyByMimeType(ctx context.Context, db *sql.DB, table string, mimeTypes []string) (int, error)
```

The runtime auto-generates the `WorkSource` (FetchBatch / WriteBatch) from `TableName()` and `Columns()`, so enricher authors only implement `Identify`, `Process`, `ProcessBatch`, and schema declaration.

### Phase C: Rules / Organization

**Rules** query accumulated metadata and produce a plan of file operations. Rules run one at a time in priority order. File rules only operate on files in `'raw'` store that are not already claimed by a prior rule. The junk rule also removes empty directories from all stores.

```go
type Rule interface {
    Name() string
    // Evaluate returns proposed operations. The runtime passes the set of file IDs
    // already claimed by prior rules so this rule can exclude them.
    Evaluate(ctx context.Context, db *sql.DB, cfg *config.Config, claimed map[int64]bool) ([]plan.Op, error)
}
```

Rule ordering (hardcoded for now): junk → dedup → exif-organize. First rule to claim a file wins.

### Plan / Operations

```go
type OpKind int
const (
    OpClean     OpKind = iota  // Move file from raw to clean_dir
    OpTrash                    // Move file from raw to trash_dir
    OpRemoveDir                // Delete empty directory (dir ops only), logged but no move
)

type Op struct {
    Kind     OpKind
    FileID   int64   // files.id (0 for directory ops)
    DirID    int64   // directories.id (0 for file ops)
    SrcStore string  // source store name ('raw', 'clean', 'trash')
    SrcPath  string  // current relative path in source store
    DstStore string  // destination store name (empty for OpRemoveDir)
    DstPath  string  // destination relative path (empty for OpRemoveDir)
    Rule     string
    Reason   string
}
```

Execution:
1. Run `pre_process_hook` if configured (e.g. ZFS snapshot)
2. For each op: move file (rename, fallback to copy+remove on EXDEV), UPDATE files (store + path), INSERT into operations
3. On individual failure: log to process_errors, skip, continue
4. Empty directories are handled by the junk rule (queries `directories` table for `file_count = 0`)

**Cross-filesystem moves:** `os.Rename` fails across mount points. If raw/clean/trash are on different ZFS datasets, detect `EXDEV` error and fall back to copy + remove-source.

**Empty directory cleanup:** The `directories` table (populated by walk) tracks recursive file counts. The junk rule queries for directories with `file_count = 0` and proposes removing them (`OpRemoveDir`). Empty directories are deleted on disk (`os.Remove`) and the operation is logged. No move to trash — empty dirs have no content to preserve. The user can review the plan before execution.

## Project Structure

```
winnow/
├── flake.nix
├── main.go
├── winnow.toml.example
├── cmd/           # CLI commands (root, init, walk, reconcile, sha256, mime, exif, plan, process, status)
├── config/        # Config struct + TOML loader with search-path
├── db/            # SQLite open/migrate, core files table, schema management
├── worker/        # Generic batch-processing worker pool
├── enricher/      # Enricher interface + helper + implementations (exif, mime, embedding)
├── rule/          # Rule interface + implementations (junk, dedup, exif-organize)
├── plan/          # Op/Plan types, dry-run printing, execution
└── testdata/
```

## Config

**Note:** The config and CLI shown below represent the full program. Config fields and CLI commands/flags are only added when their corresponding feature is implemented — no stubs.

```toml
# Phase 1: core paths
raw_dir   = "/mnt/backup/raw"
clean_dir = "/mnt/backup/clean"
trash_dir = "/mnt/backup/trash"
data_dir  = "/mnt/backup/.winnow"

# Phase 5: reconcile
[reconcile]
max_staleness = "48h"

# Phase 10: plan/process
pre_process_hook = "/usr/local/bin/winnow-snapshot.sh"

# Future
[embedding]
endpoint = "http://gpu-box:8080/embed"
timeout  = "30s"
```

Search order: `-c` flag → `$WINNOW_CONFIG` → `$XDG_CONFIG_HOME/winnow/winnow.toml` → `./winnow.toml`

## CLI

**Note:** Commands and flags are added in the phase that implements them, not before.

```
# Phase 1
winnow init                    # interactive setup: prompts for paths, writes config
winnow status                  # DB stats (grows richer as phases add data)
  -c, --config PATH
  -v, --verbose

# Phase 4
winnow walk             # walk all stores, populate/reconcile files table

# Phase 5
winnow reconcile        # mark stale files as missing

# Phase 6
winnow sha256           # compute hashes for files missing them
  --workers N                  # parallel workers (default: num CPUs)

# Phase 8
winnow mime             # detect MIME types

# Phase 9
winnow exif             # exif enricher (identify + process)
winnow exif --identify  # just the identify pass

# Phase 10
winnow plan                    # run all rules in order, print proposed operations (dry-run)
winnow plan junk               # run just the junk rule
winnow process                 # run all rules in order, execute the plan
winnow process junk            # run just the junk rule and execute

# Phase 11
winnow plan dedup              # run just the dedup rule
winnow process dedup           # run just the dedup rule and execute
```

### `winnow init`

Interactive only (personal tool, one box). Prompts for the four required paths, validates they exist (or offers to create them), writes `$XDG_CONFIG_HOME/winnow/winnow.toml`. Refuses to overwrite unless `--force`.

## Nix Flake

- `buildGoModule` for the binary
- `symlinkJoin` + `wrapProgram` to prefix PATH with runtime deps: exiftool, file (libmagic), ffmpeg
- `devShell` with go, gopls, gotools, exiftool, file, ffmpeg, sqlite
- Pin nixpkgs to nixos-25.05

## Phased Implementation

### Phase 0: README + Plan Commit ✅
Create `README.md` covering motivation and high-level design (what the tool does, why it exists, the enrichment/rules pipeline concept). No installation or usage instructions yet — just context. Commit the README and the plan file (`PLAN.md`, copied from the Claude plans dir) as the initial commit. Delete `manifest-rhodium.txt` before committing.

The README is a living document: each subsequent phase updates it to cover what is actually implemented and any caveats (e.g., "only walk is implemented so far; enrichers coming soon"). Only document what exists — no aspirational feature lists.

### Phase 1: Skeleton + Config + CLI + DB ✅
`main.go`, `cmd/{root,init,status}.go`, `config/config.go`, `db/db.go`, `go.mod`. Minimal config (just the four paths for now). `winnow init` prompts + writes config. `winnow status` loads config, opens/creates DB with WAL mode, creates hardcoded core tables (`files`, `directories`, `operations`, `process_errors`), prints stats (all zeros). Status handles missing columns gracefully (columns added by later phases may not exist yet).

Tests: config load from file, config search order, DB creation, WAL mode enabled, core tables exist.

### Phase 2: Schema Management ✅
`db/schema.go` — manages the delta on top of base tables. `Columns()` adds nullable columns via `ALTER TABLE ADD COLUMN`. `Indexes()` manages indexes via `CREATE INDEX IF NOT EXISTS` / `DROP INDEX IF EXISTS`. Compares declared state vs `PRAGMA table_info` / `PRAGMA index_list`. Idempotent. Stale columns are attempted to be dropped via `ALTER TABLE DROP COLUMN` (warn on failure). Stale indexes are dropped.

Core tables (`files`, `operations`, `process_errors`) are created by hardcoded `CREATE TABLE IF NOT EXISTS` in the `db` package. Enricher base tables use a templated `CREATE TABLE` with validated table name (`^[a-zA-Z_][a-zA-Z0-9_]*$`). Schema management only handles columns and indexes beyond these base schemas.

This defers full migrations (renames, type changes, data restructuring) until we actually need them. Add columns + add/remove indexes + attempt to drop stale columns is all we need for now.

Tests (in-memory DB): core tables created by hardcoded DDL, add column to existing table, add/drop index, idempotent re-run, enricher base table created from template, table name validation rejects bad names, stale column drop attempted (success case + warning on failure).

**Deviation:** Tests use temp-dir on-disk databases (via `Open()`) rather than in-memory DBs, since `Open()` calls `os.MkdirAll` on the path and doesn't support `":memory:"`.

### Phase 3: Worker Pool ✅
`worker/worker.go` — the batch-processing pool with coordinator loop + N workers. Tests use an **in-memory SQLite database** with a real WorkSource that uses a table, inserts pending rows, and processes them. Validates: correct result count enforcement (panic on mismatch), schema validation in WriteBatch (panic on bad keys), error results (logged to process_errors, not fatal), progress output, graceful shutdown on context cancellation, stats tracking.

**Deviation:** Worker goroutine panics (from result count mismatch) are recovered and re-panicked on the coordinator goroutine so they are observable by callers and tests. Schema validation in WriteBatch is the responsibility of each WorkSource implementation rather than being enforced generically by the pool. The coordinator sends chunks directly (no separate sender goroutine) and uses `sync.WaitGroup` to ensure clean worker shutdown before re-panicking, avoiding goroutine leaks.

### Phase 4: Walk ✅
`winnow walk` — scans all configured stores, populates `files` (using the hardcoded base schema), updates `reconciled_at`. Also populates/updates the `directories` table via UPSERT with recursive file counts and cumulative sizes. Directories no longer on disk are deleted from the table. Walk does not know about sha256, mime_type, or any enricher columns.

Tests (temp dirs on disk): walk inserts new files, walk updates reconciled_at on re-walk, walk updates size/mod_time when file changes, walk handles files across multiple stores, directories table populated with correct file_count and total_size, stale directory rows deleted.

**Deviation:** Stats report `FilesFound` (total files seen on disk) rather than separate insert/update counts, since SQLite UPSERT doesn't cleanly distinguish the two cases. An additional test verifies that previously-missing files are rediscovered (missing flag reset to 0).

### Phase 5: Reconcile ✅
`winnow reconcile` — marks files as missing based on staleness. Adds `[reconcile] max_staleness` to config.

Tests: files with old reconciled_at get marked missing, recently-walked files are untouched, already-missing files are skipped (idempotent), multiple stores handled, config parsing with default and custom values.

### Phase 6: SHA-256
`winnow sha256` — first real consumer of the worker pool. Declares `sha256 TEXT` and `hashed_at TEXT` columns on `files` via schema management. Processes files where `sha256 IS NULL OR hashed_at < mod_time` and `missing = 0`. Re-hashing is automatic: when walk updates mod_time (because the file changed), hashed_at becomes stale and sha256 gets recomputed on next run.

Tests: hashes computed correctly, missing files skipped, stale hash detected when mod_time > hashed_at, progress output works.

### Phase 7: Nix Flake
`flake.nix` — packages the Go binary + external tools (exiftool, file/libmagic, ffmpeg). `devShell` with go, gopls, gotools, exiftool, file, ffmpeg, sqlite. Prerequisite for MIME detection and EXIF enricher which shell out to `file` and `exiftool`.

### Phase 8: MIME Type Detection
Built-in enricher. Declares `mime_type TEXT` column on `files` via schema management. Detects MIME type by shelling out to `file --mime-type --brief` (libmagic — accurate for media formats including HEIC, RAW, video codecs). `winnow mime`. Uses worker pool.

Tests: correct MIME detection for common types (JPEG, PNG, HEIC, PDF, plain text), NULL for unreadable files.

### Phase 9: Two-Pass Enricher Framework + EXIF Enricher
`enricher/{enricher,exif}.go`, `IdentifyByMimeType` helper, auto-generated WorkSource from enricher schema. The exif enricher declares its table name; runtime creates the base table from the template, then schema management adds enricher-specific columns. Identification uses MIME types (image/jpeg, image/heic, image/tiff, etc.) rather than file extensions. Exercises batch processing (`ProcessBatch = 100`, one exiftool invocation per batch).

Tests: enricher base table created from template, enricher-specific columns added by schema management, identify populates candidates for image MIME types, process fills results via exiftool, result schema validated against declared columns, non-image MIME types not identified.

### Phase 10: Junk Rule + Plan/Execute
`rule/{rule,junk}.go`, `plan/plan.go`, `cmd/{plan,process}.go`. Junk patterns are hardcoded in the rule. Two pattern types: (1) **file name matches** — files whose name matches exactly (e.g., `.DS_Store`, `Thumbs.db`, `._.DS_Store`); (2) **directory name matches** — all files whose path contains the named directory as a component are junk, plus the directory itself is proposed for `OpRemoveDir` (e.g., `@eaDir`, `.Spotlight-V100`, `.Trashes`). The junk rule also proposes removing empty directories from all stores (queries `directories` table for `file_count = 0`, uses `OpRemoveDir`). Rules run one at a time, first-claims-wins via `claimed` set. Plan shows proposed ops, pre_process_hook runs, process executes + updates files + logs to operations. Errors logged to process_errors.

Tests (temp dirs): plan produces correct ops for junk files, plan proposes trashing empty directories, process moves files and updates DB, pre_process_hook invoked, errors logged and skipped.

### Phase 11: Dedup Rule
`rule/dedup.go`. Queries files for duplicate sha256. Two cases: (1) if any copy already exists in clean or trash, all raw copies are trashed; (2) if duplicates are only in raw, keeps the copy with the shortest path (tiebreaker: lexicographic — `ORDER BY length(path), path`) and trashes the rest.

Tests: correct duplicate detection, raw copies trashed when clean copy exists, shortest path kept among raw-only duplicates, lexicographic tiebreaker on equal length, plan output correct, integration with claimed set from prior rules.

### Future phases
Photo organize rule (needs more design thinking), embeddings, clustering, cross-filesystem copy fallback, orphaned enricher data cleanup.

## Verification

Every phase is validated with automated tests (`go test ./...`). Tests use in-memory SQLite databases and/or temp directories on disk. After manual validation of a phase, the equivalent assertions should be captured in tests so the validation is repeatable.

After each phase:
- `go build ./...` compiles
- `go test ./...` passes (all phases' tests, not just the current one)
- Update `README.md` to reflect what is now implemented (commands, caveats, known limitations)
- Update `PLAN.md`: mark the phase as complete (✅ on heading), and note any deviations from the original plan in the phase description
- Commit the phase. Committed artifacts should only contain content relevant to the code as it stands — no plan details, implementation journey notes, or conversation context. The commit message summarizes what was added/changed.
