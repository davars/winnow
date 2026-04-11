package db

import (
	"database/sql"
	"fmt"
	"log"
	"regexp"
	"strings"
)

// validTableName matches safe SQL identifiers for table names.
var validTableName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// Column describes a column that schema management should ensure exists.
// All columns added via schema management must be nullable (SQLite restriction:
// can't add NOT NULL without default to existing table).
type Column struct {
	Name string
	Type string // e.g. "TEXT", "INTEGER", "REAL"
}

// Index describes an index that schema management should ensure exists.
type Index struct {
	Name    string   // index name
	Table   string   // table the index is on
	Columns []string // column names
	Unique  bool
}

// SchemaProvider declares columns and indexes that schema management should ensure exist.
type SchemaProvider interface {
	Name() string      // human-readable identifier (for logs, errors)
	TableName() string // table to manage
	Columns() []Column // enricher-specific columns only (not base schema)
	Indexes() []Index  // can target any table
}

// CreateEnricherTable creates an enricher base table from the template.
// The table name is validated against the safe identifier pattern to prevent SQL injection.
func CreateEnricherTable(db *sql.DB, tableName string) error {
	if !validTableName.MatchString(tableName) {
		return fmt.Errorf("invalid table name %q: must match %s", tableName, validTableName.String())
	}

	ddl := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
    hash         TEXT PRIMARY KEY,
    file_id      INTEGER NOT NULL REFERENCES files(id),
    processed_at TEXT
)`, tableName)

	_, err := db.Exec(ddl)
	if err != nil {
		return fmt.Errorf("creating enricher table %s: %w", tableName, err)
	}
	return nil
}

// validateIdentifier checks that s is a safe SQL identifier.
func validateIdentifier(s, kind string) error {
	if !validTableName.MatchString(s) {
		return fmt.Errorf("invalid %s %q: must match %s", kind, s, validTableName.String())
	}
	return nil
}

// validColumnType matches safe SQLite type keywords.
var validColumnType = regexp.MustCompile(`^[a-zA-Z]+$`)

// EnsureSchema compares the declared schema from a SchemaProvider against
// the current database state and applies any necessary changes:
// - Adds missing columns via ALTER TABLE ADD COLUMN
// - Adds missing indexes via CREATE INDEX IF NOT EXISTS
// - Drops stale indexes (declared by this provider's namespace but no longer in Indexes())
// - Attempts to drop stale columns (logs warning on failure)
func EnsureSchema(db *sql.DB, provider SchemaProvider) error {
	table := provider.TableName()
	if err := validateIdentifier(table, "table name"); err != nil {
		return fmt.Errorf("provider %s: %w", provider.Name(), err)
	}

	// Validate all declared columns and indexes upfront.
	for _, col := range provider.Columns() {
		if err := validateIdentifier(col.Name, "column name"); err != nil {
			return fmt.Errorf("provider %s: %w", provider.Name(), err)
		}
		if !validColumnType.MatchString(col.Type) {
			return fmt.Errorf("provider %s: invalid column type %q for %s", provider.Name(), col.Type, col.Name)
		}
	}
	for _, idx := range provider.Indexes() {
		if err := validateIdentifier(idx.Name, "index name"); err != nil {
			return fmt.Errorf("provider %s: %w", provider.Name(), err)
		}
		if idx.Table != "" {
			if err := validateIdentifier(idx.Table, "index table"); err != nil {
				return fmt.Errorf("provider %s: %w", provider.Name(), err)
			}
		}
		for _, c := range idx.Columns {
			if err := validateIdentifier(c, "index column"); err != nil {
				return fmt.Errorf("provider %s: %w", provider.Name(), err)
			}
		}
	}

	// Add missing columns.
	existing, err := getColumns(db, table)
	if err != nil {
		return fmt.Errorf("reading columns for %s: %w", table, err)
	}

	declared := provider.Columns()
	declaredSet := make(map[string]bool)
	for _, col := range declared {
		declaredSet[col.Name] = true
		if !existing[col.Name] {
			stmt := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, col.Name, col.Type)
			if _, err := db.Exec(stmt); err != nil {
				return fmt.Errorf("adding column %s.%s: %w", table, col.Name, err)
			}
		}
	}

	// Drop stale columns (columns in DB but not declared, excluding base schema).
	baseColumns := getBaseColumns(table)
	for col := range existing {
		if baseColumns[col] || declaredSet[col] {
			continue
		}
		stmt := fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s", table, col)
		if _, err := db.Exec(stmt); err != nil {
			log.Printf("warning: could not drop stale column %s.%s: %v", table, col, err)
		}
	}

	// Manage indexes.
	prefix := provider.Name() + "_"
	existingIndexes, err := getIndexes(db, provider)
	if err != nil {
		return fmt.Errorf("reading indexes: %w", err)
	}

	declaredIndexes := make(map[string]bool)
	for _, idx := range provider.Indexes() {
		declaredIndexes[idx.Name] = true
		if !existingIndexes[idx.Name] {
			unique := ""
			if idx.Unique {
				unique = "UNIQUE "
			}
			idxTable := idx.Table
			if idxTable == "" {
				idxTable = table
			}
			stmt := fmt.Sprintf("CREATE %sINDEX IF NOT EXISTS %s ON %s (%s)",
				unique, idx.Name, idxTable, strings.Join(idx.Columns, ", "))
			if _, err := db.Exec(stmt); err != nil {
				return fmt.Errorf("creating index %s: %w", idx.Name, err)
			}
		}
	}

	// Drop stale indexes (those with this provider's prefix but no longer declared).
	for name := range existingIndexes {
		if strings.HasPrefix(name, prefix) && !declaredIndexes[name] {
			stmt := fmt.Sprintf("DROP INDEX IF EXISTS %s", name)
			if _, err := db.Exec(stmt); err != nil {
				return fmt.Errorf("dropping stale index %s: %w", name, err)
			}
		}
	}

	return nil
}

// getColumns returns a set of column names for a table.
func getColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull int
		var dfltValue *string
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dfltValue, &pk); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

// getIndexes returns a set of index names across all tables relevant to a provider.
func getIndexes(db *sql.DB, provider SchemaProvider) (map[string]bool, error) {
	// Collect all tables we need to check indexes on.
	tables := map[string]bool{provider.TableName(): true}
	for _, idx := range provider.Indexes() {
		if idx.Table != "" {
			tables[idx.Table] = true
		}
	}

	indexes := make(map[string]bool)
	for table := range tables {
		if err := func() error {
			rows, err := db.Query("PRAGMA index_list(" + table + ")")
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var seq int
				var name, origin string
				var unique, partial int
				if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
					return err
				}
				indexes[name] = true
			}
			return rows.Err()
		}(); err != nil {
			return nil, err
		}
	}
	return indexes, nil
}

// getBaseColumns returns the set of column names that EnsureSchema must never
// drop when managing a table. For the files table this includes both the
// hardcoded core columns AND the extension columns owned by filesProvider, so
// that any other provider that happens to be run against "files" (e.g. in
// tests) cannot drop them as "stale".
//
// Core tables are enumerated; unknown tables are assumed to be enricher base
// tables (hash, file_id, processed_at).
func getBaseColumns(table string) map[string]bool {
	switch table {
	case "files":
		base := map[string]bool{
			"id": true, "store": true, "path": true, "size": true,
			"mod_time": true, "found_at": true, "reconciled_at": true, "missing": true,
		}
		for _, c := range (filesProvider{}).Columns() {
			base[c.Name] = true
		}
		return base
	case "directories":
		return map[string]bool{
			"id": true, "store": true, "path": true, "file_count": true, "total_size": true,
		}
	case "operations":
		return map[string]bool{
			"id": true, "file_id": true, "dir_id": true, "src_store": true, "src_path": true,
			"dst_store": true, "dst_path": true, "rule": true, "reason": true, "executed_at": true,
		}
	case "process_errors":
		return map[string]bool{
			"id": true, "file_id": true, "dir_id": true, "enricher": true, "rule": true,
			"error": true, "occurred_at": true,
		}
	default:
		return map[string]bool{
			"hash": true, "file_id": true, "processed_at": true,
		}
	}
}
