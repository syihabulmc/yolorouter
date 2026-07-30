// Package testutil provides shared test helpers, including ones that need a
// real migrated database connection. Only ever imported from _test.go files
// across internal/repository, internal/service, internal/handler, and
// internal/middleware — never from production code.
package testutil

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"gorm.io/gorm"

	"github.com/yolorouter/yolorouter/migrations"
	"github.com/yolorouter/yolorouter/pkg/database"
)

var (
	templateOnce  sync.Once
	templateBytes []byte
	templateErr   error
)

// migratedTemplate returns the bytes of a fully migrated, cleanly closed SQLite
// database, building it the first time it is asked and reusing it afterwards.
//
// The migrations run once per test binary rather than once per test. Applying
// the whole tree costs microseconds per statement on Linux and macOS but 130ms
// to 290ms on Windows, where the file I/O is far slower — at roughly 2.5s per
// database, packages that build one per test spent over eight minutes there and
// internal/handler crossed the 10-minute per-package test timeout outright.
// Copying a prepared file is a single write.
//
// Copying the raw bytes is sound because the connection is closed before they
// are read and the driver runs in SQLite's default rollback-journal mode, so a
// closed database is one self-contained file with no sidecar -wal or -shm.
func migratedTemplate() ([]byte, error) {
	templateOnce.Do(func() {
		dir, err := os.MkdirTemp("", "yolorouter-schema-template")
		if err != nil {
			templateErr = fmt.Errorf("create template dir: %w", err)
			return
		}
		defer func() { _ = os.RemoveAll(dir) }()

		path := filepath.Join(dir, "template.db")
		if err := database.Init(database.Config{Driver: "sqlite", SQLitePath: path}); err != nil {
			templateErr = fmt.Errorf("database.Init for the template: %w", err)
			return
		}
		sqlDB, err := database.DB.DB()
		if err != nil {
			templateErr = fmt.Errorf("get underlying *sql.DB for the template: %w", err)
			return
		}
		if err := database.RunMigrations(sqlDB, "sqlite", migrations.SQLiteFS, "sqlite"); err != nil {
			_ = sqlDB.Close()
			templateErr = fmt.Errorf("run migrations for the template: %w", err)
			return
		}
		// Closed before the file is read: an open handle can still be holding
		// pages that have not reached the file.
		if err := sqlDB.Close(); err != nil {
			templateErr = fmt.Errorf("close the template: %w", err)
			return
		}
		templateBytes, err = os.ReadFile(path) //nolint:gosec // path is this function's own temp file
		if err != nil {
			templateErr = fmt.Errorf("read the template: %w", err)
		}
	})
	return templateBytes, templateErr
}

// NewSQLiteDB opens a fresh temp-file SQLite database already carrying every
// embedded migration, and returns the resulting *gorm.DB. Each call gets its own
// temp file (t.TempDir()), so tests never share state.
//
// The schema comes from a snapshot taken by running the same
// database.RunMigrations call production startup uses (see
// cmd/yolorouter/serve.go) once per test binary — see migratedTemplate. What
// each test opens is therefore byte-identical to a freshly migrated database.
//
// database.Init sets the package-level database.DB variable rather than
// returning a connection directly (see pkg/database/database.go) — this
// helper captures that value into a local variable immediately after Init
// returns, so a later call from a different test (which reassigns the same
// global) can't retroactively affect an already-captured *gorm.DB. Do not
// mark tests using this helper t.Parallel(): concurrent calls would race on
// the database.DB global itself while it's being read here.
func NewSQLiteDB(t *testing.T) *gorm.DB {
	t.Helper()

	schema, err := migratedTemplate()
	if err != nil {
		t.Fatalf("prepare migrated schema template: %v", err)
	}

	dbPath := filepath.Join(t.TempDir(), "test.db")
	// Written before Init so the file SQLite opens is already the finished
	// schema. Init only ever opens an existing file (mode=rw) and tightens its
	// permissions, so seeding it here is exactly the state it expects.
	if err := os.WriteFile(dbPath, schema, 0o600); err != nil {
		t.Fatalf("write schema template to the test database: %v", err)
	}
	if err := database.Init(database.Config{Driver: "sqlite", SQLitePath: dbPath}); err != nil {
		t.Fatalf("database.Init failed: %v", err)
	}
	gormDB := database.DB

	sqlDB, err := gormDB.DB()
	if err != nil {
		t.Fatalf("get underlying *sql.DB: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	return gormDB
}

// CloseDB closes db's underlying *sql.DB connection, failing the test if
// that fails. Tests that need to force a "closed connection" error (to
// exercise a repository/handler function's DB-error branch) call this
// directly instead of relying on NewSQLiteDB's own t.Cleanup, which only
// closes at the very end of the test.
func CloseDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get underlying *sql.DB: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close underlying *sql.DB: %v", err)
	}
}
