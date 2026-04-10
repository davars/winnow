# Winnow

A file organization tool for cleaning up disorganized messes of files

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

## Installation

Winnow is packaged as a Nix flake. The flake builds the Go binary and wraps it so external tools (`exiftool`, `file`/libmagic, `ffmpeg`) are on `PATH` at runtime.

```
nix build github:davars/winnow       # build the wrapped binary → ./result/bin/winnow
nix run  github:davars/winnow -- --help
nix develop                          # dev shell with go, gopls, exiftool, file, ffmpeg, sqlite
```

The flake targets `{x86_64,aarch64}-{linux,darwin}` and pins `nixpkgs` to `nixos-25.11`.

### Installing on a server

On any machine with Nix installed and flakes enabled:

```sh
nix profile add github:davars/winnow
winnow --help
```

This builds a closure containing the binary and every runtime dependency (`exiftool`, `file`, `ffmpeg`), drops a wrapped `winnow` into the user profile, and puts it on `PATH`. No system package manager required -- the server does not need `exiftool`/`file`/`ffmpeg` installed separately.

To upgrade after a new release:

```sh
nix profile upgrade winnow
```

On NixOS, prefer adding the flake as an input in `configuration.nix` and installing via `environment.systemPackages` so the binary is managed alongside the rest of the system.

If the server does not have internet access but you have SSH, build locally and copy the closure over:

```sh
nix copy --to ssh://user@server github:davars/winnow
```

then run `nix profile add` on the server using the store path printed by `nix build`.

### Enable flakes

The `nix` CLI and flakes are gated behind experimental feature flags. Enable them once in `~/.config/nix/nix.conf` (create the file if it doesn't exist):

```
experimental-features = nix-command flakes
```

## Development

The repository ships an `.envrc` that loads the flake's devShell via [`direnv`](https://direnv.net/). With direnv installed and hooked into your shell, `cd`ing into the repo puts `go`, `gopls`, `exiftool`, `file`, `ffmpeg`, and `sqlite` on `PATH` automatically — editors and terminals launched from that directory inherit the env with no per-tool configuration.

One-time setup:

```sh
nix profile add nixpkgs#direnv          # or: brew install direnv

# hook into zsh (or bash/fish equivalent)
echo 'eval "$(direnv hook zsh)"' >> ~/.zshrc
```

Then from the repo:

```sh
direnv allow
```

`cd`ing into the directory loads the devShell; `cd`ing out restores the previous environment. Flake evaluation can take a few seconds on first entry — installing [`nix-direnv`](https://github.com/nix-community/nix-direnv) on top (it caches the evaluation) makes subsequent loads near-instant:

```sh
nix profile add nixpkgs#nix-direnv
mkdir -p ~/.config/direnv
echo 'source $HOME/.nix-profile/share/nix-direnv/direnvrc' >> ~/.config/direnv/direnvrc
```

For VS Code, install the `mkhl.direnv` extension so the editor picks up the same environment — `gopls`, debuggers, and integrated terminals will resolve binaries through the flake instead of whatever happens to be on the system `PATH`.

Without direnv, the fallback is `nix develop` to drop into an interactive shell, or `nix develop --command <cmd>` to run a single command in the devShell env.

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

### Walk

```
winnow walk
```

Scans all configured stores (raw, clean, trash) and populates the database. New files are inserted; existing files have their `reconciled_at`, `size`, and `mod_time` updated. Files previously marked missing are rediscovered if they reappear on disk. The `directories` table is maintained with recursive file counts and cumulative sizes; directories no longer on disk are removed.

### Reconcile

```
winnow reconcile
```

Marks files as missing if they haven't been seen by a walk within the staleness threshold. Files already marked missing are skipped. The threshold is configurable via `[reconcile] max_staleness` in the config (default: 48h). A typical workflow is to run `walk` first, then `reconcile` to flag files that have disappeared from disk.

### SHA-256

```
winnow sha256 [--workers N]
```

Computes SHA-256 content hashes for files that haven't been hashed yet, or whose content has changed since the last hash (detected by comparing `mod_time` against `hashed_at`). Missing files are skipped. Uses a worker pool for parallel hashing; `--workers` controls concurrency (default: number of CPUs). Files that fail to hash are logged to `process_errors` and skipped; they will be retried if the file is modified on disk.

### MIME

```
winnow mime [--workers N]
```

Detects MIME types by shelling out to `file --mime-type --brief` (libmagic, bundled by the flake). Stores the result in `files.mime_type`, with a companion `mime_checked_at` column used for staleness tracking against `mod_time` (same pattern as sha256). Files that fail detection (including unreadable files) are logged to `process_errors` and skipped; they will be retried if the file is modified on disk.

### Config

Config is located via search order: `-c` flag, `$WINNOW_CONFIG`, `$XDG_CONFIG_HOME/winnow/winnow.toml`, `./winnow.toml`.

```toml
raw_dir   = "/mnt/backup/raw"
clean_dir = "/mnt/backup/clean"
trash_dir = "/mnt/backup/trash"
data_dir  = "/mnt/backup/.winnow"

[reconcile]
max_staleness = "48h"  # default; files not seen within this window are marked missing
```

## Status

Early development. The `init`, `status`, `walk`, `reconcile`, `sha256`, and `mime` commands are implemented. The database is created with core tables, and schema management is in place for enrichers to declare additional columns and indexes. A generic batch-processing worker pool (`worker` package) provides the foundation for parallel enrichment passes. Walking populates the `files` and `directories` tables from the filesystem; reconcile marks stale files as missing; sha256 computes content hashes using the worker pool; mime detection populates `mime_type` via libmagic. The Nix flake packages the binary with its runtime dependencies (`exiftool`, `file`, `ffmpeg`). No two-pass enrichers or rules are available yet. See `PLAN.md` for the full design and phased implementation plan.
