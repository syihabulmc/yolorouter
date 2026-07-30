package database

import (
	"database/sql"
	"os"
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
	// — the two trees are mirrors and must not drift apart in length.
	if version != 19 {
		t.Fatalf("expected version 19 after all migrations, got %d", version)
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
