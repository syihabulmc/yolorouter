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
	// 00015_system_settings_and_custom_system_prompt.sql +
	// 00016_input_compression.sql +
	// 00017_request_endpoints.sql +
	// 00018_model_candidate_capability_tristate.sql +
	// 00019_model_candidates_price_history.sql).
	if version != 19 {
		t.Fatalf("expected version 19 after all migrations, got %d", version)
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

// TestMigration00016InputCompression verifies that migration 00016 seeds the
// input_compression_enabled row in system_settings, adds the two per-key
// override columns to api_keys, the four savings columns to request_logs,
// and the debug body column to request_log_bodies — all with safe defaults.
// Rolling back to version 15 must drop every new column and delete the seed row.
func TestMigration00016InputCompression(t *testing.T) {
	db := newMemoryDB(t)
	if err := RunMigrations(db, "sqlite", migrations.SQLiteFS, "sqlite"); err != nil {
		t.Fatalf("RunMigrations failed: %v", err)
	}

	// system_settings seed row exists with the off default.
	var enabledValue string
	err := db.QueryRow("SELECT value FROM system_settings WHERE key = 'input_compression_enabled'").Scan(&enabledValue)
	if err != nil {
		t.Fatalf("seed row input_compression_enabled missing: %v", err)
	}
	if enabledValue != "false" {
		t.Fatalf("input_compression_enabled seed = %q, want false", enabledValue)
	}

	// api_keys compress columns default to 0 (inherit global default).
	res, err := db.Exec(`INSERT INTO api_keys (key_hash, key_prefix, status, budget_spent_micros, created_at, updated_at) VALUES ('h2', 'sk-y', 1, 0, '2026-01-01 00:00:00', '2026-01-01 00:00:00')`)
	if err != nil {
		t.Fatalf("create api key: %v", err)
	}
	apiKeyID, _ := res.LastInsertId()

	var compressEnabled int
	var compressOverride int
	err = db.QueryRow("SELECT compress_enabled, compress_enabled_override FROM api_keys WHERE id = ?", apiKeyID).Scan(&compressEnabled, &compressOverride)
	if err != nil {
		t.Fatalf("read api key compress columns: %v", err)
	}
	if compressEnabled != 0 || compressOverride != 0 {
		t.Fatalf("api_keys compress defaults not 0/0: enabled=%d override=%d", compressEnabled, compressOverride)
	}

	// request_logs compress columns default to 0/0/empty/empty.
	rlRes, err := db.Exec(`INSERT INTO request_logs (request_id, model_name, status_code, created_at) VALUES ('req-1', 'm', 200, '2026-01-01 00:00:00')`)
	if err != nil {
		t.Fatalf("create request log: %v", err)
	}
	rlID, _ := rlRes.LastInsertId()

	var tokensSaved int
	var costSaved int64
	var skipReason string
	var compressors string
	err = db.QueryRow("SELECT compress_estimated_tokens_saved, compress_estimated_cost_saved_micros, compress_skip_reason, compressors_applied FROM request_logs WHERE id = ?", rlID).Scan(&tokensSaved, &costSaved, &skipReason, &compressors)
	if err != nil {
		t.Fatalf("read request_logs compress columns: %v", err)
	}
	if tokensSaved != 0 || costSaved != 0 || skipReason != "" || compressors != "" {
		t.Fatalf("request_logs compress defaults not 0/0/empty/empty: tokens=%d cost=%d skip=%q compressors=%q", tokensSaved, costSaved, skipReason, compressors)
	}

	// request_log_bodies compressed_request_body defaults to empty string.
	rlbRes, err := db.Exec(`INSERT INTO request_log_bodies (request_id, created_at) VALUES ('req-1', '2026-01-01 00:00:00')`)
	if err != nil {
		t.Fatalf("create request log body: %v", err)
	}
	rlbID, _ := rlbRes.LastInsertId()

	var compressedBody string
	err = db.QueryRow("SELECT compressed_request_body FROM request_log_bodies WHERE id = ?", rlbID).Scan(&compressedBody)
	if err != nil {
		t.Fatalf("read request_log_bodies compress column: %v", err)
	}
	if compressedBody != "" {
		t.Fatalf("compressed_request_body default = %q, want empty", compressedBody)
	}

	// Rolling back migration 00016 must drop every new column and remove the
	// seed row.
	if err := RollbackTo(db, "sqlite", migrations.SQLiteFS, "sqlite", 15); err != nil {
		t.Fatalf("RollbackTo(15) failed: %v", err)
	}

	// Seed row must be gone.
	var remaining string
	err = db.QueryRow("SELECT value FROM system_settings WHERE key = 'input_compression_enabled'").Scan(&remaining)
	if err == nil {
		t.Fatalf("input_compression_enabled seed row still present after rollback: value=%q", remaining)
	}

	// Helper: scan one column name from a table and return whether it exists.
	hasColumn := func(table, col string) bool {
		t.Helper()
		rows, err := db.Query("PRAGMA table_info(" + table + ")")
		if err != nil {
			t.Fatalf("PRAGMA table_info(%s): %v", table, err)
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
				t.Fatalf("scan table_info(%s) row: %v", table, err)
			}
			if name == col {
				return true
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate table_info(%s) rows: %v", table, err)
		}
		return false
	}

	for _, c := range []struct{ table, col string }{
		{"api_keys", "compress_enabled"},
		{"api_keys", "compress_enabled_override"},
		{"request_logs", "compress_estimated_tokens_saved"},
		{"request_logs", "compress_estimated_cost_saved_micros"},
		{"request_logs", "compress_skip_reason"},
		{"request_logs", "compressors_applied"},
		{"request_log_bodies", "compressed_request_body"},
	} {
		if hasColumn(c.table, c.col) {
			t.Fatalf("expected %s.%s to be dropped after rollback to 15", c.table, c.col)
		}
	}
}

// TestMigration00018ModelCandidateCapabilityTristate verifies that migration
// 00018 makes the two capability columns nullable so they can express "unknown"
// alongside supported/unsupported, and that rolling back restores the old
// two-state shape by collapsing unknown to false.
//
// Nullability is the whole point of the migration: the gateway drops candidates
// whose capability flag reads false from streaming and tool-calling rotation, so
// an inconclusive probe needs somewhere to go other than false.
func TestMigration00018ModelCandidateCapabilityTristate(t *testing.T) {
	db := newMemoryDB(t)
	if err := RunMigrations(db, "sqlite", migrations.SQLiteFS, "sqlite"); err != nil {
		t.Fatalf("RunMigrations failed: %v", err)
	}
	// Roll back so the up-migration's value handling is exercised against a
	// database holding the two-state data the old schema actually stored.
	if err := RollbackTo(db, "sqlite", migrations.SQLiteFS, "sqlite", 17); err != nil {
		t.Fatalf("RollbackTo(17) to stage pre-migration data failed: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO models (id, name, management_status, created_at, updated_at)
		VALUES (1, 'm', 1, '2026-01-01 00:00:00', '2026-01-01 00:00:00')`); err != nil {
		t.Fatalf("seed model: %v", err)
	}
	// Two providers, because model_candidates carries UNIQUE(model_id, provider_id).
	for id, name := range map[int]string{1: "p1", 2: "p2", 3: "p3"} {
		if _, err := db.Exec(`INSERT INTO providers (id, name, provider_type, base_url, management_status, destination_version, created_at, updated_at)
			VALUES (?, ?, 'openai', 'https://example.invalid', 1, 1, '2026-01-01 00:00:00', '2026-01-01 00:00:00')`, id, name); err != nil {
			t.Fatalf("seed provider %d: %v", id, err)
		}
	}
	// Candidate 1 has a PROVEN streaming capability and a stored false for tools;
	// candidate 2 has false for both.
	seedCandidate := func(t *testing.T, id, providerID, sortOrder int, streaming, functionCalling int) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO model_candidates
			(id, model_id, provider_id, provider_model_name, input_price, output_price, max_output,
			 management_status, sort_order, verification_status, created_at, updated_at,
			 supports_streaming, supports_function_calling)
			VALUES (?, 1, ?, 'gpt-4o', 0, 0, 0, 1, ?, 1, '2026-01-01 00:00:00', '2026-01-01 00:00:00', ?, ?)`,
			id, providerID, sortOrder, streaming, functionCalling); err != nil {
			t.Fatalf("seed candidate %d: %v", id, err)
		}
	}
	seedCandidate(t, 1, 1, 1, 1, 0)
	seedCandidate(t, 2, 2, 2, 0, 0)

	// The up-migration under test.
	if err := RunMigrations(db, "sqlite", migrations.SQLiteFS, "sqlite"); err != nil {
		t.Fatalf("re-applying migrations failed: %v", err)
	}

	readCapabilities := func(t *testing.T, id int) (streaming, functionCalling sql.NullBool) {
		t.Helper()
		if err := db.QueryRow(`SELECT supports_streaming, supports_function_calling FROM model_candidates WHERE id = ?`, id).
			Scan(&streaming, &functionCalling); err != nil {
			t.Fatalf("read capability columns for candidate %d: %v", id, err)
		}
		return streaming, functionCalling
	}

	// A stored false was written by the old "anything not proven is false" rule
	// and cannot be told apart from a misclassified probe, so it must become
	// unknown. A stored true came from a probe that actually succeeded and must
	// survive — discarding it would make every verified candidate demand a retest.
	streaming, functionCalling := readCapabilities(t, 1)
	if !streaming.Valid || !streaming.Bool {
		t.Fatalf("expected a proven supports_streaming=true to survive the migration, got %+v", streaming)
	}
	if functionCalling.Valid {
		t.Fatalf("expected a stored supports_function_calling=false to become unknown, got %+v", functionCalling)
	}
	if streaming, functionCalling = readCapabilities(t, 2); streaming.Valid || functionCalling.Valid {
		t.Fatalf("expected both stored falses to become unknown, got streaming=%+v function_calling=%+v", streaming, functionCalling)
	}

	// Nullability is the point of the migration: before it, these columns were
	// NOT NULL and this insert would fail.
	if _, err := db.Exec(`INSERT INTO model_candidates
		(id, model_id, provider_id, provider_model_name, input_price, output_price, max_output,
		 management_status, sort_order, verification_status, created_at, updated_at,
		 supports_streaming, supports_function_calling)
		VALUES (3, 1, 3, 'gpt-4o-mini', 0, 0, 0, 2, 3, 0, '2026-01-01 00:00:00', '2026-01-01 00:00:00', NULL, NULL)`); err != nil {
		t.Fatalf("inserting NULL capabilities failed — columns are still NOT NULL: %v", err)
	}

	// Rolling back collapses unknown to false, restoring the two-state
	// convention, while still carrying proven trues over.
	if err := RollbackTo(db, "sqlite", migrations.SQLiteFS, "sqlite", 17); err != nil {
		t.Fatalf("RollbackTo(17) failed: %v", err)
	}
	var downStreaming, downFunctionCalling bool
	if err := db.QueryRow(`SELECT supports_streaming, supports_function_calling FROM model_candidates WHERE id = 1`).
		Scan(&downStreaming, &downFunctionCalling); err != nil {
		t.Fatalf("read capability columns after rollback: %v", err)
	}
	if !downStreaming {
		t.Fatal("expected rollback to preserve a proven supports_streaming=true, got false")
	}
	if downFunctionCalling {
		t.Fatal("expected rollback to collapse an unknown supports_function_calling to false, got true")
	}

	// Re-applying must work, so an operator who rolls back is not stuck there.
	if err := RunMigrations(db, "sqlite", migrations.SQLiteFS, "sqlite"); err != nil {
		t.Fatalf("re-applying migrations after rollback failed: %v", err)
	}
	version, err := GetCurrentVersion(db, "sqlite")
	if err != nil {
		t.Fatalf("GetCurrentVersion failed: %v", err)
	}
	if version != 19 {
		t.Fatalf("expected version 19 after re-apply, got %d", version)
	}
}
