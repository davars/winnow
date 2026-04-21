# Winnow

A file-organization tool for cleaning up large, disorganized backup drives — the kind that have accumulated duplicates and OS junk (`.DS_Store`, Synology `@eaDir`, Spotlight indexes) over years of use, alongside media that needs metadata-based organization.

Winnow catalogs files in SQLite, enriches the catalog with content hashes and metadata, then applies rules to propose moves — never deleting, always auditable. Two rules ship today: **junk** (trash OS metadata and empty directories) and **dedup** (trash duplicate copies in the raw store, keeping the shortest path). EXIF data is extracted but no rule consumes it yet.

## Safety

- **No deletes.** Files move to a configured trash directory; the only thing removed in place is empty directories (logged).
- **Dry-run first.** `winnow plan` previews every operation; nothing touches the filesystem until `winnow process`.
- **Pre-process hook.** Set `pre_process_hook` in the database-backed settings to take a snapshot (e.g. ZFS) before `process` runs; non-zero exit aborts.
- **No clobber on move.** Cross-filesystem moves use `O_EXCL`; an existing file at the destination is never overwritten.
- **Audit log.** Every move is recorded in the `operations` table.

## Installation

Winnow is packaged as a Nix flake. The flake bundles `exiftool`, `file`/libmagic, and `ffmpeg` so they're on `PATH` at runtime — no system packages needed.

```sh
nix profile add github:davars/winnow
winnow --help
```

Targets `{x86_64,aarch64}-{linux,darwin}`; pins `nixpkgs` to `nixos-25.11`.

If `nix` rejects the URL with an experimental-features error, enable flakes once in `~/.config/nix/nix.conf`:

```
experimental-features = nix-command flakes
```

Other install paths:

- **One-off run:** `nix run github:davars/winnow -- --help`, or `nix build github:davars/winnow` → `./result/bin/winnow`.
- **NixOS:** add the flake as an input and install via `environment.systemPackages`.
- **Offline server:** `nix copy --to ssh://user@server github:davars/winnow`, then `nix profile add` on the server using the printed store path.
- **Upgrade:** `nix profile upgrade winnow`.

## Workflow

Winnow operates on three file stores plus a data directory:

- **raw** — the messy source; rules read from here.
- **clean** — organized output; rules don't touch existing files here, only `RMDIR` empty dirs.
- **trash** — where rules send files for review before you delete them yourself.
- **data** — SQLite database and winnow's bookkeeping.

Configure once, then run the pipeline:

```sh
winnow init                # interactive: prompts for data dir, then DB-backed settings
winnow walk                # discover files on disk
winnow reconcile           # mark files that have disappeared as missing
winnow sha256              # content hashes
winnow mime                # MIME type detection
winnow exif                # EXIF extraction (images + video)
winnow plan                # preview proposed moves
winnow process             # execute the plan
```

Each step is incremental and resumable; rerunning picks up where the last run stopped. `plan` is a pure dry-run and safe to repeat.

The TOML file is now just a locator for `data_dir`; operational settings live in the SQLite `settings` table.

## Commands

### init

```
winnow init
```

Interactive setup that prompts for the data directory first, then loads or creates `winnow.db` there and edits the DB-backed settings (`raw_dir`, `clean_dir`, `trash_dir`, `organize.timezone`, `pre_process_hook`, `reconcile.max_staleness`). If a locator file or DB settings row already exists, prompts are pre-filled with the current values. Enter `-` to clear optional values like the hook or timezone.

Writes a minimal locator file to `$XDG_CONFIG_HOME/winnow/winnow.toml` (or the existing config's location when editing):

```toml
data_dir = "/mnt/backup/.winnow"
```

`--data-dir` overrides config-file lookup for the current run and is also used as the initial default in `winnow init`.

### import-config

```
winnow import-config [--force]
```

Temporary migration helper for older full TOML configs. It reads the legacy TOML, imports the runtime settings into the database, then rewrites that TOML in place to the new one-field locator format. If the database already has a settings row, pass `--force` to replace it.

### status

```
winnow status [-v]
```

Database statistics (file counts, operations, errors). `-v` adds the locator path plus the DB-backed settings.

`status` works as soon as the database exists. Operational commands such as `walk`, `reconcile`, `sha256`, `mime`, `exif`, `plan`, `process`, and `organize` require the settings row and will tell you to run `winnow init` or `winnow import-config` if it is missing.

### walk

```
winnow walk
```

Scans every configured store and reconciles the database with disk: new files inserted, existing files updated, previously-missing files rediscovered if they reappear, vanished directories removed. Maintains the `directories` table (recursive file count and cumulative size).

### reconcile

```
winnow reconcile
```

Marks files as missing if they haven't been seen by a walk within `[reconcile] max_staleness` (default `48h`). Typical workflow: `walk` then `reconcile`.

### sha256

```
winnow sha256 [--workers N]
```

Computes SHA-256 hashes for unhashed files and re-hashes any whose `mod_time` is newer than `hashed_at`. Workers default to `runtime.NumCPU()`. Failures land in `process_errors` and retry only when the file changes on disk.

### mime

```
winnow mime [--workers N]
```

Detects MIME types via `file --mime-type --brief` (libmagic). Writes `mime.mime_type` keyed on content hash, so duplicate content is detected once. Requires `walk` and `sha256`.

### exif

```
winnow exif [--workers N] [--identify]
```

Extracts EXIF tags from images (JPEG, HEIC, TIFF, PNG, WebP, common RAW) and video (MP4, MOV, AVI, MKV, WebM, M4V, MPEG, 3GPP) by batching files through `exiftool`. Tags are stored as JSON in `exif.data`, keyed on content hash. `--identify` populates candidates without running exiftool. Requires `walk`, `sha256`, `mime`.

The extraction policy is fingerprinted in `exif.tags_version`; editing the policy invalidates affected rows so they re-process on the next pass. `data IS NULL` means processing failed; `data = '{}'` means the file had no extractable tags.

### plan / process

```
winnow plan    [rule]
winnow process [rule]
```

`plan` runs every rule in priority order and prints the proposed operations without touching disk. `process` runs the same plan and then executes it (after running `pre_process_hook` if configured in the settings row). Pass a rule name to scope to that rule alone.

Files trashed by a rule are moved to the trash store preserving their relative path; the move is recorded in `operations`. Files claimed by an earlier-priority rule are skipped by later ones.

Rules in priority order:

- **junk** — trashes OS-metadata files (`.DS_Store`, `Thumbs.db`, `desktop.ini`, `._*`, …) and files inside vendor metadata directories (`@eaDir`, `.Spotlight-V100`, `__MACOSX`, …); proposes `RMDIR` for empty directories in any store.
- **dedup** — for each sha256 hash with multiple raw-store copies, keeps the shortest path (lexicographic tiebreaker) and trashes the rest. Clean and trash stores are not scanned, so manually-organized copies in clean are preserved.

### query

```
winnow query [SQL] [--format {table,tsv,csv}] [--no-header]
```

Ad-hoc SQL against the winnow database; reads SQL from stdin if no argument is passed. Two helpers are registered: `human_bytes(n)` (SI) and `human_ibytes(n)` (IEC), both via `go-humanize`.

`query` only needs the data directory locator; it does not require the settings row to exist yet.

```
winnow query "SELECT path, human_ibytes(total_size) FROM directories ORDER BY total_size DESC LIMIT 30"
```

### exec

```
winnow exec COMMAND [args...]
```

Runs an arbitrary command with winnow's `PATH` — useful on a server where only `winnow` is installed but you want ad-hoc access to the bundled `exiftool`, `file`, or `ffmpeg`. Flag parsing is disabled; all args after `COMMAND` are forwarded verbatim. Stdio is connected to the terminal; the child's exit code propagates.

```
winnow exec exiftool -json photo.jpg
```

## Config

Located in this order: `--data-dir`, `-c` flag, `$WINNOW_CONFIG`, `$XDG_CONFIG_HOME/winnow/winnow.toml`, `./winnow.toml`.

The steady-state TOML is only a locator:

```toml
data_dir = "/mnt/backup/.winnow"
```

The database `settings` table stores:

- `raw_dir`
- `clean_dir`
- `trash_dir`
- `pre_process_hook`
- `reconcile_max_staleness`
- `organize_timezone`

Edit those values with `winnow init`. Migrate old full TOMLs once with `winnow import-config`.

## Status

Early development. All planned commands are implemented; the rule set is intentionally minimal (`junk`, `dedup`). EXIF data is collected but no rule consumes it yet. See [`docs/architecture.md`](docs/architecture.md) for internals, [`docs/development.md`](docs/development.md) for dev setup, and [`docs/original_implementation_plan.md`](docs/original_implementation_plan.md) for the historical phased plan.
