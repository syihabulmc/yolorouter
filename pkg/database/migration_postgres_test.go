package database

import (
	"database/sql"
	"os"
	"strings"
	"testing"

	_ "github.com/lib/pq"

	"github.com/yolorouter/yolorouter/migrations"
)

// newTestPostgresDB opens the database named by TEST_POSTGRES_DSN and drops
// every object in the public schema, so each test starts from nothing. The CI
// job that supplies the DSN points it at a throwaway service container; the
// wipe is what makes the suite re-runnable locally against a scratch database
// too. Skips when the variable is unset, matching backup_postgres_test.go.
func newTestPostgresDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set, skipping Postgres migration test")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open test postgres: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("reset public schema: %v", err)
	}
	return db
}

// The Postgres migration tree is the half of the schema that no unit test
// reaches: everything else in the suite runs on SQLite, so a typo, an
// unsupported clause, or a broken Down in migrations/postgres would otherwise
// first surface on a user's live database during an upgrade. This applies every
// migration, rolls the whole tree back, and re-applies it.
func TestPostgresMigrationsApplyRollBackAndReapply(t *testing.T) {
	db := newTestPostgresDB(t)

	if err := RunMigrations(db, "postgres", migrations.PostgresFS, "postgres"); err != nil {
		t.Fatalf("RunMigrations failed: %v", err)
	}
	version, err := GetCurrentVersion(db, "postgres")
	if err != nil {
		t.Fatalf("GetCurrentVersion failed: %v", err)
	}
	// Kept in step with the SQLite assertion in TestGetCurrentVersionOnFreshSQLiteDB
	// — the two trees are mirrors and must not drift apart in length. The
	// expected value is derived from the embedded filenames so this can
	// never go stale when a migration is added.
	want := maxEmbeddedMigrationVersion(t, migrations.PostgresFS, "postgres")
	if version != want {
		t.Fatalf("expected version %d after all migrations, got %d", want, version)
	}
	if sqliteMax := maxEmbeddedMigrationVersion(t, migrations.SQLiteFS, "sqlite"); sqliteMax != want {
		t.Fatalf("migration trees drifted apart: sqlite max %d vs postgres max %d", sqliteMax, want)
	}

	// Rolling all the way back exercises every Down. An operator who has to
	// downgrade must not be stranded, and a Down that only appears to work is
	// indistinguishable from one that does until it is run.
	if err := RollbackTo(db, "postgres", migrations.PostgresFS, "postgres", 0); err != nil {
		t.Fatalf("RollbackTo(0) failed: %v", err)
	}
	if err := RunMigrations(db, "postgres", migrations.PostgresFS, "postgres"); err != nil {
		t.Fatalf("re-applying migrations after rollback failed: %v", err)
	}
}

// TestPostgresMigration00023CarriesAdminAndSessionsOverToUsers is the
// Postgres twin of the SQLite upgrade-replay test: the multi-account
// migration's data carry-over AND its Postgres-only sequence handling
// (an INSERT with an explicit id does not advance a BIGSERIAL sequence,
// so the migration must setval past the copied ids) both need to run on
// the real backend at least once — the empty-tree up/down test above
// never inserts a row, so it cannot catch a broken setval.
func TestPostgresMigration00023CarriesAdminAndSessionsOverToUsers(t *testing.T) {
	db := newTestPostgresDB(t)
	if err := RunMigrations(db, "postgres", migrations.PostgresFS, "postgres"); err != nil {
		t.Fatalf("RunMigrations failed: %v", err)
	}
	if err := RollbackTo(db, "postgres", migrations.PostgresFS, "postgres", 22); err != nil {
		t.Fatalf("RollbackTo(22) failed: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO admins (id, username, password_hash, failed_login_count, created_at, updated_at)
		VALUES (7, 'boss', 'bcrypt-hash', 2, now(), now())`); err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO admin_sessions (id, admin_id, expires_at, created_at)
		VALUES ('session-hash-value', 7, now() + interval '1 year', now())`); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	if err := RunMigrations(db, "postgres", migrations.PostgresFS, "postgres"); err != nil {
		t.Fatalf("re-running migrations failed: %v", err)
	}

	var (
		username, passwordHash, role string
		status, failedCount          int
		isLocal                      bool
	)
	row := db.QueryRow(`SELECT username, password_hash, role, status, is_local, failed_login_count FROM users WHERE id = 7`)
	if err := row.Scan(&username, &passwordHash, &role, &status, &isLocal, &failedCount); err != nil {
		t.Fatalf("migrated user row not found: %v", err)
	}
	if username != "boss" || passwordHash != "bcrypt-hash" || failedCount != 2 {
		t.Fatalf("carried-over fields wrong: username=%q hash=%q failed=%d", username, passwordHash, failedCount)
	}
	if role != "admin" || status != 1 || !isLocal {
		t.Fatalf("expected enabled local admin, got role=%q status=%d is_local=%v", role, status, isLocal)
	}

	var sessionUserID int64
	if err := db.QueryRow(`SELECT user_id FROM user_sessions WHERE id = 'session-hash-value'`).Scan(&sessionUserID); err != nil {
		t.Fatalf("migrated session row not found: %v", err)
	}
	if sessionUserID != 7 {
		t.Fatalf("expected session to stay attached to user 7, got %d", sessionUserID)
	}

	// The sequence must have been advanced past the copied explicit id —
	// without the migration's setval, this INSERT would generate id 1 and
	// eventually collide with copied rows.
	var nextID int64
	if err := db.QueryRow(`INSERT INTO users (username, role, status, created_at, updated_at)
		VALUES ('next-user', 'member', 1, now(), now()) RETURNING id`).Scan(&nextID); err != nil {
		t.Fatalf("insert after migration failed: %v", err)
	}
	if nextID <= 7 {
		t.Fatalf("expected the next generated id to be > 7, got %d", nextID)
	}

	// The runtime binds is_local as a Go bool (model.User.IsLocal), so the
	// column must be a real BOOLEAN — an integer-typed column would make
	// every `is_local = $1` predicate fail with a type error on this
	// backend only. This probe uses the exact parameter shape the
	// repository layer produces.
	var localCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE is_local = $1`, true).Scan(&localCount); err != nil {
		t.Fatalf("boolean-bound is_local predicate failed (column type mismatch?): %v", err)
	}
	if localCount != 1 {
		t.Fatalf("expected 1 local user via boolean predicate, got %d", localCount)
	}

	// The partial unique index must survive the trip through goose on this
	// backend too: a second local account is impossible. The error must be
	// the unique violation itself — asserting a bare non-nil error here
	// once let a column-type error impersonate the constraint.
	_, err := db.Exec(`INSERT INTO users (username, password_hash, role, status, is_local, created_at, updated_at)
		VALUES ('local-two', 'hash', 'admin', 1, true, now(), now())`)
	if err == nil {
		t.Fatalf("expected the second local user to violate the partial unique index")
	}
	if !strings.Contains(err.Error(), "idx_users_single_local") {
		t.Fatalf("expected a unique violation on idx_users_single_local, got: %v", err)
	}
}

// TestPostgresMigration00024BackfillsOwnership is the Postgres twin of the
// SQLite ownership-backfill replay — the UPDATE ... (SELECT id FROM users
// WHERE is_local) shape must resolve the same way on this backend.
func TestPostgresMigration00024BackfillsOwnership(t *testing.T) {
	db := newTestPostgresDB(t)
	if err := RunMigrations(db, "postgres", migrations.PostgresFS, "postgres"); err != nil {
		t.Fatalf("RunMigrations failed: %v", err)
	}
	if err := RollbackTo(db, "postgres", migrations.PostgresFS, "postgres", 23); err != nil {
		t.Fatalf("RollbackTo(23) failed: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO users (id, username, password_hash, role, status, is_local, created_at, updated_at)
		VALUES (5, 'boss', 'hash', 'admin', 1, TRUE, now(), now())`); err != nil {
		t.Fatalf("seed local admin: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO api_keys (id, key_hash, key_prefix, status, created_at, updated_at)
		VALUES (11, 'kh-1', 'sk-yr-a', 1, now(), now())`); err != nil {
		t.Fatalf("seed api key: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO request_logs (request_id, api_key_id, model_name, status_code, created_at)
		VALUES ('req-1', 11, 'm', 200, now())`); err != nil {
		t.Fatalf("seed request log: %v", err)
	}

	if err := RunMigrations(db, "postgres", migrations.PostgresFS, "postgres"); err != nil {
		t.Fatalf("re-running migrations failed: %v", err)
	}

	var keyOwner, logOwner int64
	if err := db.QueryRow(`SELECT user_id FROM api_keys WHERE id = 11`).Scan(&keyOwner); err != nil {
		t.Fatalf("read backfilled key owner: %v", err)
	}
	if err := db.QueryRow(`SELECT user_id FROM request_logs WHERE request_id = 'req-1'`).Scan(&logOwner); err != nil {
		t.Fatalf("read backfilled log owner: %v", err)
	}
	if keyOwner != 5 || logOwner != 5 {
		t.Fatalf("expected ownership backfilled to local admin 5, got key=%d log=%d", keyOwner, logOwner)
	}
}

// The price clock, the folded name and their index carry the auto-suggest
// look-up, and their Postgres definitions differ from the SQLite ones
// (TIMESTAMPTZ, now() as the default, a backfill against existing rows). This
// pins the parts specific to this backend, including that the backfill really
// runs against rows written under the old schema and that an older binary can
// still insert once the columns exist.
func TestPostgresMigration00019PriceHistory(t *testing.T) {
	db := newTestPostgresDB(t)

	if err := RunMigrations(db, "postgres", migrations.PostgresFS, "postgres"); err != nil {
		t.Fatalf("RunMigrations failed: %v", err)
	}
	// Go back before the price migration so its Up runs against a row that
	// predates the column, which is the only way the backfill is exercised.
	if err := RollbackTo(db, "postgres", migrations.PostgresFS, "postgres", 18); err != nil {
		t.Fatalf("RollbackTo(18) failed: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO models (id, name, management_status, created_at, updated_at)
		VALUES (1, 'smart', 1, '2026-01-01 00:00:00+00', '2026-01-01 00:00:00+00')`); err != nil {
		t.Fatalf("seed model: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO providers (id, name, base_url, management_status, created_at, updated_at)
		VALUES (1, 'p', 'https://api.example.com', 1, '2026-01-01 00:00:00+00', '2026-01-01 00:00:00+00')`); err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO model_candidates
		(id, model_id, provider_id, provider_model_name, input_price, output_price,
		 max_output, management_status, sort_order, verification_status, created_at, updated_at)
		VALUES (1, 1, 1, 'gpt-4o', 1, 2, 0, 2, 1, 0, '2026-01-01 00:00:00+00', '2026-02-03 04:05:06+00')`); err != nil {
		t.Fatalf("seed candidate: %v", err)
	}

	if err := RunMigrations(db, "postgres", migrations.PostgresFS, "postgres"); err != nil {
		t.Fatalf("re-applying 00019 failed: %v", err)
	}

	// The pre-existing row must carry updated_at forward, not the now() the
	// NOT NULL default stamped on it during the ALTER.
	var backfilled string
	if err := db.QueryRow(`SELECT to_char(price_updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS')
		FROM model_candidates WHERE id = 1`).Scan(&backfilled); err != nil {
		t.Fatalf("read price_updated_at: %v", err)
	}
	if backfilled != "2026-02-03 04:05:06" {
		t.Fatalf("expected price_updated_at backfilled from updated_at, got %q", backfilled)
	}

	// The folded copy is backfilled too, or every pre-existing candidate becomes
	// invisible to price suggestions.
	var folded string
	if err := db.QueryRow(`SELECT provider_model_name_folded FROM model_candidates WHERE id = 1`).Scan(&folded); err != nil {
		t.Fatalf("read provider_model_name_folded: %v", err)
	}
	if folded != "gpt-4o" {
		t.Fatalf("expected the folded name backfilled, got %q", folded)
	}

	// Both defaults must survive: during a rolling upgrade an older binary that
	// does not know these columns is still inserting candidates, and without a
	// default every one of those inserts fails the NOT NULL constraint.
	assertInsertableByAnOlderBinary(t, db)

	var indexed bool
	if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_indexes
		WHERE tablename = 'model_candidates' AND indexname = 'idx_model_candidates_provider_model_price')`).Scan(&indexed); err != nil {
		t.Fatalf("read pg_indexes: %v", err)
	}
	if !indexed {
		t.Fatal("expected idx_model_candidates_provider_model_price to exist")
	}

	// Down drops both, and re-applying must still work.
	if err := RollbackTo(db, "postgres", migrations.PostgresFS, "postgres", 18); err != nil {
		t.Fatalf("RollbackTo(18) after up failed: %v", err)
	}
	for _, col := range []string{"price_updated_at", "provider_model_name_folded"} {
		var stillThere bool
		if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.columns
			WHERE table_name = 'model_candidates' AND column_name = $1)`, col).Scan(&stillThere); err != nil {
			t.Fatalf("read information_schema after rollback: %v", err)
		}
		if stillThere {
			t.Fatalf("expected the Down migration to drop %s", col)
		}
	}
	if err := RunMigrations(db, "postgres", migrations.PostgresFS, "postgres"); err != nil {
		t.Fatalf("re-applying after rollback failed: %v", err)
	}
}

// assertInsertableByAnOlderBinary writes a candidate the way a binary predating
// this migration would: naming only the columns that existed before it. If the
// new columns have no default, this is exactly the statement that starts failing
// on every still-running old instance the moment the first new one migrates.
func assertInsertableByAnOlderBinary(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO models (id, name, management_status, created_at, updated_at)
		VALUES (2, 'legacy', 1, now(), now())`); err != nil {
		t.Fatalf("seed model for the old-binary insert: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO model_candidates
		(id, model_id, provider_id, provider_model_name, input_price, output_price,
		 max_output, management_status, sort_order, verification_status, created_at, updated_at)
		VALUES (2, 2, 1, 'gpt-4o', 1, 2, 0, 2, 1, 0, now(), now())`); err != nil {
		t.Fatalf("an insert omitting the new columns failed — a rolling upgrade would take the old instances down: %v", err)
	}
	var priced bool
	if err := db.QueryRow(`SELECT price_updated_at > '2000-01-01'::timestamptz FROM model_candidates WHERE id = 2`).Scan(&priced); err != nil {
		t.Fatalf("read price_updated_at of the old-binary row: %v", err)
	}
	if !priced {
		t.Fatal("expected the default to stamp a current price clock, not a sentinel")
	}
}
