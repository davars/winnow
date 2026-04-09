# Winnow

A file organization tool for cleaning up old backups on a Linux/ZFS server.

## Motivation

Over time, backup drives accumulate duplicates, filesystem junk (`.DS_Store`, Synology `@eaDir` metadata, Spotlight indexes), and media files that need metadata-based organization. Winnow addresses this by building a SQLite database of file metadata, enriching it incrementally, and then applying rules to organize files.

Key principles:

- **Never deletes files.** Files are moved to a trash directory for manual review before permanent deletion. Empty directories are the sole exception (removed in place and logged).
- **Incremental processing.** Each step (walking, hashing, enrichment, rule evaluation) can be run independently and picks up where it left off.
- **Enrichment pipeline.** Walk the filesystem, compute hashes, detect MIME types, extract EXIF data -- each pass adds metadata to the database. Rules then query the accumulated metadata to propose file movements.
- **Auditable.** Every file movement is logged. Proposed operations can be reviewed before execution.

## Architecture

Winnow has three processing phases:

1. **Built-in steps** write directly to the core `files` table: walk (discover files), reconcile (mark missing files), sha256 (content hashing), and MIME detection.

2. **Enrichers** are two-pass: first identify candidates (pure DB operation), then process them in parallel using a worker pool. Each enricher owns a table keyed on content hash. Example: the EXIF enricher extracts camera metadata from images.

3. **Rules** query the enriched metadata and produce a plan of file operations (move to clean, move to trash, remove empty directory). Rules run in priority order; the first rule to claim a file wins. Plans can be reviewed before execution.

## Usage

### Setup

```
winnow init
```

Interactive setup that prompts for four directory paths (raw, clean, trash, data) and writes a config file to `$XDG_CONFIG_HOME/winnow/winnow.toml`.

### Status

```
winnow status [-v]
```

Shows database statistics (file counts, operations, errors). Use `-v` for verbose output including config paths.

### Config

Config is located via search order: `-c` flag, `$WINNOW_CONFIG`, `$XDG_CONFIG_HOME/winnow/winnow.toml`, `./winnow.toml`.

```toml
raw_dir   = "/mnt/backup/raw"
clean_dir = "/mnt/backup/clean"
trash_dir = "/mnt/backup/trash"
data_dir  = "/mnt/backup/.winnow"
```

## Status

Early development. The `init` and `status` commands are implemented. The database is created with core tables, and schema management is in place for enrichers to declare additional columns and indexes. A generic batch-processing worker pool (`worker` package) provides the foundation for parallel enrichment passes. No enrichment or rules are available yet. See `PLAN.md` for the full design and phased implementation plan.
