package db

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// testProvider implements SchemaProvider for testing.
type testProvider struct {
	name    string
	table   string
	columns []Column
	indexes []Index
}

func (p *testProvider) Name() string      { return p.name }
func (p *testProvider) TableName() string  { return p.table }
func (p *testProvider) Columns() []Column  { return p.columns }
func (p *testProvider) Indexes() []Index   { return p.indexes }

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func TestCreateEnricherTable(t *testing.T) {
	db := openTestDB(t)

	if err := CreateEnricherTable(db, "exif"); err != nil {
		t.Fatal(err)
	}

	cols := tableColumns(t, db, "exif")
	want := []string{"hash", "file_id", "processed_at"}
	if len(cols) != len(want) {
		t.Fatalf("got columns %v, want %v", cols, want)
	}
	for i, c := range want {
		if cols[i] != c {
			t.Errorf("column %d = %q, want %q", i, cols[i], c)
		}
	}
}

func TestCreateEnricherTableIdempotent(t *testing.T) {
	db := openTestDB(t)

	if err := CreateEnricherTable(db, "exif"); err != nil {
		t.Fatal(err)
	}
	if err := CreateEnricherTable(db, "exif"); err != nil {
		t.Fatal("second create should be idempotent:", err)
	}
}

func TestCreateEnricherTableBadName(t *testing.T) {
	db := openTestDB(t)

	badNames := []string{
		"1bad",
		"drop table",
		"foo;bar",
		"test-table",
		"",
		"foo bar",
		"table; DROP TABLE files--",
	}
	for _, name := range badNames {
		t.Run(name, func(t *testing.T) {
			err := CreateEnricherTable(db, name)
			if err == nil {
				t.Errorf("expected error for table name %q", name)
			}
		})
	}
}

func TestEnsureSchemaAddColumns(t *testing.T) {
	db := openTestDB(t)

	provider := &testProvider{
		name:  "sha256",
		table: "files",
		columns: []Column{
			{Name: "sha256", Type: "TEXT"},
			{Name: "hashed_at", Type: "TEXT"},
		},
	}

	if err := EnsureSchema(db, provider); err != nil {
		t.Fatal(err)
	}

	cols := tableColumns(t, db, "files")
	// Should have all base columns plus sha256 and hashed_at.
	found := make(map[string]bool)
	for _, c := range cols {
		found[c] = true
	}
	if !found["sha256"] {
		t.Error("sha256 column not added")
	}
	if !found["hashed_at"] {
		t.Error("hashed_at column not added")
	}
}

func TestEnsureSchemaIdempotent(t *testing.T) {
	db := openTestDB(t)

	provider := &testProvider{
		name:  "sha256",
		table: "files",
		columns: []Column{
			{Name: "sha256", Type: "TEXT"},
		},
	}

	if err := EnsureSchema(db, provider); err != nil {
		t.Fatal(err)
	}
	// Second run should be a no-op.
	if err := EnsureSchema(db, provider); err != nil {
		t.Fatal("idempotent re-run failed:", err)
	}
}

func TestEnsureSchemaAddIndex(t *testing.T) {
	db := openTestDB(t)

	provider := &testProvider{
		name:  "sha256",
		table: "files",
		columns: []Column{
			{Name: "sha256", Type: "TEXT"},
		},
		indexes: []Index{
			{Name: "sha256_idx_sha256", Table: "files", Columns: []string{"sha256"}},
		},
	}

	if err := EnsureSchema(db, provider); err != nil {
		t.Fatal(err)
	}

	if !indexExists(t, db, "files", "sha256_idx_sha256") {
		t.Error("index sha256_idx_sha256 not created")
	}
}

func TestEnsureSchemaDropStaleIndex(t *testing.T) {
	db := openTestDB(t)

	// First, create an index.
	provider := &testProvider{
		name:  "test",
		table: "files",
		indexes: []Index{
			{Name: "test_idx_old", Table: "files", Columns: []string{"store"}},
		},
	}
	if err := EnsureSchema(db, provider); err != nil {
		t.Fatal(err)
	}
	if !indexExists(t, db, "files", "test_idx_old") {
		t.Fatal("index should exist after creation")
	}

	// Now remove it from the provider — it should be dropped.
	provider.indexes = nil
	if err := EnsureSchema(db, provider); err != nil {
		t.Fatal(err)
	}
	if indexExists(t, db, "files", "test_idx_old") {
		t.Error("stale index test_idx_old should have been dropped")
	}
}

func TestEnsureSchemaEnricherTable(t *testing.T) {
	db := openTestDB(t)

	if err := CreateEnricherTable(db, "exif"); err != nil {
		t.Fatal(err)
	}

	provider := &testProvider{
		name:  "exif",
		table: "exif",
		columns: []Column{
			{Name: "create_date", Type: "TEXT"},
			{Name: "make", Type: "TEXT"},
			{Name: "model", Type: "TEXT"},
		},
	}

	if err := EnsureSchema(db, provider); err != nil {
		t.Fatal(err)
	}

	cols := tableColumns(t, db, "exif")
	want := map[string]bool{
		"hash": true, "file_id": true, "processed_at": true,
		"create_date": true, "make": true, "model": true,
	}
	for _, c := range cols {
		delete(want, c)
	}
	if len(want) > 0 {
		missing := make([]string, 0, len(want))
		for k := range want {
			missing = append(missing, k)
		}
		t.Errorf("missing columns: %v", missing)
	}
}

func TestEnsureSchemaDropStaleColumn(t *testing.T) {
	db := openTestDB(t)

	// Add a column, then remove it from the provider.
	provider := &testProvider{
		name:  "test",
		table: "files",
		columns: []Column{
			{Name: "temp_col", Type: "TEXT"},
		},
	}
	if err := EnsureSchema(db, provider); err != nil {
		t.Fatal(err)
	}

	cols := tableColumns(t, db, "files")
	found := false
	for _, c := range cols {
		if c == "temp_col" {
			found = true
		}
	}
	if !found {
		t.Fatal("temp_col should have been added")
	}

	// Remove from provider — should attempt to drop.
	provider.columns = nil
	if err := EnsureSchema(db, provider); err != nil {
		t.Fatal(err)
	}

	cols = tableColumns(t, db, "files")
	for _, c := range cols {
		if c == "temp_col" {
			t.Error("stale column temp_col should have been dropped")
		}
	}
}

func TestEnsureSchemaDropColumnWarning(t *testing.T) {
	db := openTestDB(t)

	// Add a column and create an index on it, then try to drop the column.
	// SQLite won't drop a column that's part of an index — should warn, not error.
	provider := &testProvider{
		name:  "test",
		table: "files",
		columns: []Column{
			{Name: "indexed_col", Type: "TEXT"},
		},
		indexes: []Index{
			{Name: "test_idx_icol", Table: "files", Columns: []string{"indexed_col"}},
		},
	}
	if err := EnsureSchema(db, provider); err != nil {
		t.Fatal(err)
	}

	// Remove the column from provider but keep the index referencing it.
	// This simulates a scenario where the column can't be dropped.
	// We need the index to stay so DROP COLUMN fails.
	provider.columns = nil
	// Keep index so column drop would fail.
	err := EnsureSchema(db, provider)
	// Should not return an error — stale column drop failure is a warning.
	if err != nil {
		t.Errorf("expected no error (just a warning), got: %v", err)
	}
}

func TestEnsureSchemaUniqueIndex(t *testing.T) {
	db := openTestDB(t)

	provider := &testProvider{
		name:  "test",
		table: "files",
		indexes: []Index{
			{Name: "test_idx_unique", Table: "files", Columns: []string{"store", "path"}, Unique: true},
		},
	}

	if err := EnsureSchema(db, provider); err != nil {
		t.Fatal(err)
	}

	if !indexExists(t, db, "files", "test_idx_unique") {
		t.Error("unique index not created")
	}
}

func TestEnsureSchemaInvalidTableName(t *testing.T) {
	db := openTestDB(t)

	provider := &testProvider{
		name:  "bad",
		table: "drop table files--",
	}

	err := EnsureSchema(db, provider)
	if err == nil {
		t.Error("expected error for invalid table name")
	}
	if !strings.Contains(err.Error(), "invalid table name") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestEnsureSchemaCrossTableIndex(t *testing.T) {
	db := openTestDB(t)

	// Provider for enricher table, but index on operations table.
	if err := CreateEnricherTable(db, "exif"); err != nil {
		t.Fatal(err)
	}

	provider := &testProvider{
		name:  "exif",
		table: "exif",
		indexes: []Index{
			{Name: "exif_idx_ops_rule", Table: "operations", Columns: []string{"rule"}},
		},
	}

	if err := EnsureSchema(db, provider); err != nil {
		t.Fatal(err)
	}

	if !indexExists(t, db, "operations", "exif_idx_ops_rule") {
		t.Error("cross-table index not created")
	}
}

func indexExists(t *testing.T, db *sql.DB, table, indexName string) bool {
	t.Helper()
	rows, err := db.Query("PRAGMA index_list(" + table + ")")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	for rows.Next() {
		var seq int
		var name, origin string
		var unique, partial int
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			t.Fatal(err)
		}
		if name == indexName {
			return true
		}
	}
	return false
}
