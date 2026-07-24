package database

import (
	"database/sql"
	"testing"

	// database.go (same package) imports github.com/glebarez/sqlite, which
	// transitively imports github.com/glebarez/go-sqlite and registers the
	// "sqlite" database/sql driver via its init(). Blank-importing
	// modernc.org/sqlite here as well (as the plan's illustrative test does)
	// panics with "sql: Register called twice for driver sqlite" because
	// both packages register the exact same driver name — so we rely on the
	// registration already pulled in transitively instead of duplicating it.

	"github.com/yolorouter/yolorouter/migrations"
)

// newMemoryDB opens an in-memory SQLite database for a single test and
// registers its Close via t.Cleanup, so callers don't each repeat the
// open-or-fail-and-defer-close boilerplate.
func newMemoryDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestRunMigrationsAppliesBaselineOnSQLite(t *testing.T) {
	db := newMemoryDB(t)

	if err := RunMigrations(db, "sqlite", migrations.SQLiteFS, "sqlite"); err != nil {
		t.Fatalf("RunMigrations failed: %v", err)
	}

	// the goose metadata table must exist and the version must be at least 1
	// (baseline migration applied)
	var version int64
	row := db.QueryRow("SELECT version_id FROM goose_db_version ORDER BY id DESC LIMIT 1")
	if err := row.Scan(&version); err != nil {
		t.Fatalf("query goose_db_version: %v", err)
	}
	if version < 1 {
		t.Fatalf("expected version >= 1 after baseline migration, got %d", version)
	}
}

func TestRunMigrationsIsIdempotent(t *testing.T) {
	db := newMemoryDB(t)

	if err := RunMigrations(db, "sqlite", migrations.SQLiteFS, "sqlite"); err != nil {
		t.Fatalf("first RunMigrations failed: %v", err)
	}
	if err := RunMigrations(db, "sqlite", migrations.SQLiteFS, "sqlite"); err != nil {
		t.Fatalf("second RunMigrations (idempotency check) failed: %v", err)
	}
}

func TestGetCurrentVersionOnFreshSQLiteDB(t *testing.T) {
	db := newMemoryDB(t)

	if err := RunMigrations(db, "sqlite", migrations.SQLiteFS, "sqlite"); err != nil {
		t.Fatalf("RunMigrations failed: %v", err)
	}

	version, err := GetCurrentVersion(db, "sqlite")
	if err != nil {
		t.Fatalf("GetCurrentVersion failed: %v", err)
	}
	// This asserts the current highest migration version, not just the
	// baseline migration — it must be bumped whenever a new migration file
	// is added to migrations/sqlite (currently 00001_baseline.sql +
	// 00002_create_admin_auth.sql + 00003_add_admin_sessions_expires_at_index.sql +
	// 00004_create_providers.sql + 00005_create_models.sql +
	// 00006_create_api_keys.sql + 00007_create_request_logs.sql +
	// 00008_request_logs_status_index.sql + 00009_request_logs_request_id_index.sql +
	// 00010_request_logs_cache_tokens.sql + 00011_create_request_log_bodies.sql +
	// 00012_provider_protocol_endpoints.sql + 00013_request_logs_cache_savings.sql +
	// 00014_api_keys_allow_all_models.sql +
	// 00015_system_settings_and_custom_system_prompt.sql).
	if version != 15 {
		t.Fatalf("expected version 15 after all migrations, got %d", version)
	}
}

func TestRunMigrationsAppliesAdminAuthTablesOnSQLite(t *testing.T) {
	db := newMemoryDB(t)

	if err := RunMigrations(db, "sqlite", migrations.SQLiteFS, "sqlite"); err != nil {
		t.Fatalf("RunMigrations failed: %v", err)
	}

	version, err := GetCurrentVersion(db, "sqlite")
	if err != nil {
		t.Fatalf("GetCurrentVersion failed: %v", err)
	}
	if version < 2 {
		t.Fatalf("expected version >= 2 after admin_auth migration, got %d", version)
	}

	for _, table := range []string{"admins", "admin_sessions"} {
		var name string
		row := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table)
		if err := row.Scan(&name); err != nil {
			t.Fatalf("table %q not found after migration: %v", table, err)
		}
	}
}

func TestRunMigrationsAddsProviderProtocolEndpointsColumn(t *testing.T) {
	db := newMemoryDB(t)

	if err := RunMigrations(db, "sqlite", migrations.SQLiteFS, "sqlite"); err != nil {
		t.Fatalf("RunMigrations failed: %v", err)
	}

	var found bool
	rows, err := db.Query("PRAGMA table_info(providers)")
	if err != nil {
		t.Fatalf("PRAGMA table_info(providers): %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			cid        int
			name       string
			colType    string
			notNull    int
			defaultVal sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultVal, &pk); err != nil {
			t.Fatalf("scan table_info row: %v", err)
		}
		if name == "protocol_endpoints" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table_info rows: %v", err)
	}
	if !found {
		t.Fatal("expected providers.protocol_endpoints column after migration, not found")
	}

	// Rolling back the 00012 migration must drop the column again.
	if err := RollbackTo(db, "sqlite", migrations.SQLiteFS, "sqlite", 11); err != nil {
		t.Fatalf("RollbackTo(11) failed: %v", err)
	}
	rows2, err := db.Query("PRAGMA table_info(providers)")
	if err != nil {
		t.Fatalf("PRAGMA table_info(providers) after rollback: %v", err)
	}
	defer func() { _ = rows2.Close() }()
	for rows2.Next() {
		var (
			cid        int
			name       string
			colType    string
			notNull    int
			defaultVal sql.NullString
			pk         int
		)
		if err := rows2.Scan(&cid, &name, &colType, &notNull, &defaultVal, &pk); err != nil {
			t.Fatalf("scan table_info row after rollback: %v", err)
		}
		if name == "protocol_endpoints" {
			t.Fatal("expected providers.protocol_endpoints column to be dropped after rollback to version 11")
		}
	}
	if err := rows2.Err(); err != nil {
		t.Fatalf("iterate table_info rows after rollback: %v", err)
	}
}

func TestRollbackToVersionZero(t *testing.T) {
	db := newMemoryDB(t)

	if err := RunMigrations(db, "sqlite", migrations.SQLiteFS, "sqlite"); err != nil {
		t.Fatalf("RunMigrations failed: %v", err)
	}
	if err := RollbackTo(db, "sqlite", migrations.SQLiteFS, "sqlite", 0); err != nil {
		t.Fatalf("RollbackTo(0) failed: %v", err)
	}

	version, err := GetCurrentVersion(db, "sqlite")
	if err != nil {
		t.Fatalf("GetCurrentVersion after rollback failed: %v", err)
	}
	if version != 0 {
		t.Fatalf("expected version 0 after rollback, got %d", version)
	}
}

// TestMigration00015SystemSettingsAndCSPColumns verifies that migration 00015
// creates the system_settings table with both seed rows and adds the three
// custom-system-prompt columns to api_keys with the expected defaults.
func TestMigration00015SystemSettingsAndCSPColumns(t *testing.T) {
	db := newMemoryDB(t)
	if err := RunMigrations(db, "sqlite", migrations.SQLiteFS, "sqlite"); err != nil {
		t.Fatalf("RunMigrations failed: %v", err)
	}

	// system_settings table exists with both seed rows.
	var enabledValue string
	err := db.QueryRow("SELECT value FROM system_settings WHERE key = 'custom_system_prompt_enabled'").Scan(&enabledValue)
	if err != nil {
		t.Fatalf("seed row custom_system_prompt_enabled missing: %v", err)
	}
	if enabledValue != "false" {
		t.Fatalf("enabled seed = %q, want false", enabledValue)
	}

	var textValue string
	err = db.QueryRow("SELECT value FROM system_settings WHERE key = 'custom_system_prompt'").Scan(&textValue)
	if err != nil {
		t.Fatalf("seed row custom_system_prompt missing: %v", err)
	}
	if textValue != "" {
		t.Fatalf("text seed = %q, want empty", textValue)
	}

	// api_keys gained the three columns with safe defaults — insert a row
	// using only NOT NULL pre-existing columns and read the new columns back.
	res, err := db.Exec(`INSERT INTO api_keys (key_hash, key_prefix, status, budget_spent_micros, created_at, updated_at) VALUES ('h', 'sk-x', 1, 0, '2026-01-01 00:00:00', '2026-01-01 00:00:00')`)
	if err != nil {
		t.Fatalf("create api key: %v", err)
	}
	rowID, _ := res.LastInsertId()

	var cspEnabled int
	var cspOverride int
	var cspText string
	err = db.QueryRow("SELECT custom_system_prompt_enabled, custom_system_prompt_enabled_override, custom_system_prompt FROM api_keys WHERE id = ?", rowID).Scan(&cspEnabled, &cspOverride, &cspText)
	if err != nil {
		t.Fatalf("read api key csp columns: %v", err)
	}
	if cspEnabled != 0 || cspOverride != 0 || cspText != "" {
		t.Fatalf("csp columns default not false/false/empty: enabled=%d override=%d text=%q", cspEnabled, cspOverride, cspText)
	}
}
