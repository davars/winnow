# Architecture

Winnow has three processing phases:

1. **Built-in steps** write directly to the core `files` table: walk (discover files), reconcile (mark missing files), and sha256 (content hashing).

2. **Enrichers** are two-pass: first identify candidates (pure DB operation), then process them in parallel using a worker pool. Each enricher owns a table keyed on content hash. Examples: the MIME enricher detects content types via libmagic; the EXIF enricher extracts camera metadata from images.

3. **Rules** query the enriched metadata and produce a plan of file operations (move to clean, move to trash, remove empty directory). Rules run in priority order; the first rule to claim a file wins. Plans can be reviewed before execution.
