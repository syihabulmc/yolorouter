package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yolorouter/yolorouter/internal/config"
	"github.com/yolorouter/yolorouter/pkg/database"
)

// TestBootstrapCommandRejectsExtraArgsBeforeInit guards the ordering fix:
// unexpected positional args (e.g. a misplaced --config caught as a
// positional arg by Go's flag package, which stops parsing flags at the
// first non-flag argument) must be rejected before bootstrap.Init ever
// runs — otherwise the command would already have connected to (and for
// SQLite, auto-created) the wrong default database by the time the error
// surfaces. We assert this by checking no config.yaml was generated as a
// side effect of the rejected call.
func TestBootstrapCommandRejectsExtraArgsBeforeInit(t *testing.T) {
	dir := t.TempDir()
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() {
		if err := os.Chdir(oldWd); err != nil {
			t.Fatalf("restore wd: %v", err)
		}
	}()

	err = runDBMigrate(context.Background(), []string{"unexpected-extra-arg"})
	if err == nil {
		t.Fatalf("expected error for unexpected positional argument")
	}
	if !strings.Contains(err.Error(), "unexpected extra arguments") {
		t.Fatalf("expected 'unexpected extra arguments' in error, got: %v", err)
	}

	// db:migrate still resolves through the generating loader, so an absent
	// config here really does mean bootstrap.Init never ran.
	if _, statErr := os.Stat(filepath.Join(dir, "configs", "config.yaml")); statErr == nil {
		t.Fatalf("bootstrap.Init must not have run — configs/config.yaml should not have been generated")
	}
}

// TestRunDBRollbackRejectsFlagAfterPositionalVersion is the exact scenario
// from a real invocation: "db:rollback 1 --config prod.yaml" — Go's flag
// package stops parsing at "1" and leaves "--config"/"prod.yaml" as
// unconsumed extra args. Silently ignoring them would roll back the
// default database instead of prod.yaml; this must error instead.
func TestRunDBRollbackRejectsFlagAfterPositionalVersion(t *testing.T) {
	dir := t.TempDir()
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() {
		if err := os.Chdir(oldWd); err != nil {
			t.Fatalf("restore wd: %v", err)
		}
	}()

	err = runDBRollback(context.Background(), []string{"1", "--config", "some-other-config.yaml"})
	if err == nil {
		t.Fatalf("expected error for flag appearing after the positional version argument")
	}
	if !strings.Contains(err.Error(), "unexpected extra arguments") {
		t.Fatalf("expected 'unexpected extra arguments' in error, got: %v", err)
	}
}

// TestRunDBRollbackRejectsInvalidVersionBeforeInit guards the ordering fix
// for version-format validation: "db:rollback abc" must fail immediately
// on the malformed version string, before bootstrap.Init ever runs — not
// after already generating a default config and connecting to (and for
// SQLite, creating) a database file. We assert this the same way as
// TestBootstrapCommandRejectsExtraArgsBeforeInit: no configs/config.yaml
// should exist afterward.
func TestRunDBRollbackRejectsInvalidVersionBeforeInit(t *testing.T) {
	dir := t.TempDir()
	sqlitePath := writeDeploymentConfig(t, dir)
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() {
		if err := os.Chdir(oldWd); err != nil {
			t.Fatalf("restore wd: %v", err)
		}
	}()

	err = runDBRollback(context.Background(), []string{"abc"})
	if err == nil {
		t.Fatalf("expected error for a non-numeric version argument")
	}
	if !strings.Contains(err.Error(), "invalid version argument") {
		t.Fatalf("expected 'invalid version argument' in error, got: %v", err)
	}

	// The database is the observable now: db:rollback resolves an existing
	// config rather than generating one, so "no config was written" would hold
	// even if the version guard were deleted. bootstrap.Init opening the
	// deployment is what has to not happen, and for SQLite that creates the
	// file.
	if _, statErr := os.Stat(sqlitePath); !os.IsNotExist(statErr) {
		t.Fatalf("bootstrap.Init must not have run — no database may be opened, stat err = %v", statErr)
	}
}

// TestRunDBRollbackRejectsNegativeVersionBeforeInit guards against a
// negative version reaching bootstrap.Init: strconv.ParseInt happily
// parses "-1" as a valid int64, so without an explicit >= 0 check, a
// negative version would sail past version-format validation and connect
// to (and for SQLite, create) a database before database.RollbackTo ever
// gets a chance to reject a version number that can't mean anything.
//
// The args use Go flag's "--" terminator ("end of flags") to get "-1"
// through as a positional argument at all — flag.FlagSet.Parse treats a
// bare "-1" as an attempt to use an unrecognized flag named "1", not as a
// positional value, so plain []string{"-1"} would be rejected by Parse
// itself before ever reaching this check (also a safe outcome, just via a
// different error message than the one this test is targeting).
func TestRunDBRollbackRejectsNegativeVersionBeforeInit(t *testing.T) {
	dir := t.TempDir()
	sqlitePath := writeDeploymentConfig(t, dir)
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() {
		if err := os.Chdir(oldWd); err != nil {
			t.Fatalf("restore wd: %v", err)
		}
	}()

	err = runDBRollback(context.Background(), []string{"--", "-1"})
	if err == nil {
		t.Fatalf("expected error for a negative version argument")
	}
	if !strings.Contains(err.Error(), "cannot be negative") {
		t.Fatalf("expected 'cannot be negative' in error, got: %v", err)
	}

	// The database is the observable now: db:rollback resolves an existing
	// config rather than generating one, so "no config was written" would hold
	// even if the version guard were deleted. bootstrap.Init opening the
	// deployment is what has to not happen, and for SQLite that creates the
	// file.
	if _, statErr := os.Stat(sqlitePath); !os.IsNotExist(statErr) {
		t.Fatalf("bootstrap.Init must not have run — no database may be opened, stat err = %v", statErr)
	}
}

// TestRunDBBackupFailsForMissingSQLiteSourceWithoutCreatingIt guards against
// db:backup silently fabricating an empty database: an earlier version
// routed through bootstrap.Init/database.Init first, which opens (and for
// SQLite, creates) the database file as a side effect — so backing up a
// database that doesn't exist yet would "succeed" with an empty backup
// instead of erroring. db:backup must load config only, check the source
// file, and fail without ever creating it.
func TestRunDBBackupFailsForMissingSQLiteSourceWithoutCreatingIt(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	dbPath := filepath.Join(dir, "does-not-exist.db")
	outDir := filepath.Join(dir, "backups")

	configYAML := "database:\n" +
		"  driver: sqlite\n" +
		"  sqlite_path: " + dbPath + "\n" +
		"security:\n" +
		"  provider_master_key: \"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\"\n"
	if err := os.WriteFile(configPath, []byte(configYAML), 0o600); err != nil {
		t.Fatalf("write test config: %v", err)
	}

	err := runDBBackup(context.Background(), []string{"--config", configPath, "--output-dir", outDir})
	if err == nil {
		t.Fatalf("expected error for missing source database")
	}
	if !strings.Contains(err.Error(), "source database not found") {
		t.Fatalf("expected 'source database not found' in error, got: %v", err)
	}

	if _, statErr := os.Stat(dbPath); statErr == nil {
		t.Fatalf("db:backup must not have created the missing source database file")
	}
}

// TestDescribeTargetNamesSQLiteDeployment: db:reset and db:rollback act on
// whichever deployment the config resolved to, and resolution now reaches
// beyond the process cwd (an installed deployment is found from any directory).
// The line they print therefore has to name the config and database files, not
// just the driver — "driver=sqlite" alone cannot tell a throwaway sandbox apart
// from the production install.
func TestDescribeTargetNamesSQLiteDeployment(t *testing.T) {
	cfg := &config.Config{}
	cfg.Database.Driver = "sqlite"
	cfg.Database.SQLitePath = "/opt/yolorouter/data/yolorouter.db"

	got := describeTarget(cfg, "/opt/yolorouter/configs/config.yaml")

	for _, want := range []string{"/opt/yolorouter/configs/config.yaml", "/opt/yolorouter/data/yolorouter.db", "sqlite"} {
		if !strings.Contains(got, want) {
			t.Errorf("describeTarget() = %q, want it to mention %q", got, want)
		}
	}
}

// TestDescribeTargetRedactsPostgresPassword: the description is printed to the
// terminal and routinely pasted into tickets, so it names the server and
// database but must never carry the password.
func TestDescribeTargetRedactsPostgresPassword(t *testing.T) {
	cfg := &config.Config{}
	cfg.Database.Driver = "postgres"
	cfg.Database.Host = "db.internal"
	cfg.Database.Port = 5432
	cfg.Database.User = "yolo"
	cfg.Database.Password = "sup3r-s3cret"
	cfg.Database.DBName = "yolorouter"

	got := describeTarget(cfg, "/etc/yolorouter/config.yaml")

	if strings.Contains(got, "sup3r-s3cret") {
		t.Fatalf("describeTarget() leaked the password: %q", got)
	}
	for _, want := range []string{"postgres", "db.internal", "5432", "yolorouter", "yolo"} {
		if !strings.Contains(got, want) {
			t.Errorf("describeTarget() = %q, want it to mention %q", got, want)
		}
	}
}

// TestRunDBRollbackRefusesWhileAnInstanceHoldsTheLock: rolling back runs the
// down migrations, which drop tables and columns. A running serve holds the
// instance lock for its whole lifetime precisely so destructive maintenance
// cannot land underneath it — db:reset has always honoured that, db:rollback
// did not, and would happily drop columns out from under a live server until
// the requests started failing on a schema that no longer matched.
//
// Failing before bootstrap.Init is part of the contract: no database file may
// be opened or created on the way to this error.
func TestRunDBRollbackRefusesWhileAnInstanceHoldsTheLock(t *testing.T) {
	dir := t.TempDir()
	sqlitePath := filepath.Join(dir, "data", "yolorouter.db")
	configPath := filepath.Join(dir, "configs", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir configs: %v", err)
	}
	body := "server:\n    port: 8080\ndatabase:\n    driver: sqlite\n    sqlite_path: " + sqlitePath +
		"\nsecurity:\n    provider_master_key: dGVzdC1rZXktZm9yLWxvYWQtZXhpc3RpbmctdGVzdHM=\n"
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Stand in for a running serve.
	unlock, err := database.AcquireInstanceLock(instanceLockPath(sqlitePath))
	if err != nil {
		t.Fatalf("test setup: acquire instance lock: %v", err)
	}
	defer func() { _ = unlock() }()

	err = runDBRollback(context.Background(), []string{"--config", configPath})
	if err == nil {
		t.Fatal("expected db:rollback to refuse while an instance holds the lock")
	}
	if !strings.Contains(err.Error(), "running") {
		t.Fatalf("error should say another instance is running, got: %v", err)
	}
	if _, statErr := os.Stat(sqlitePath); !os.IsNotExist(statErr) {
		t.Fatalf("no database may be opened before the lock check, stat err = %v", statErr)
	}
}

// writeDeploymentConfig lays a loadable config into dir and returns the sqlite
// path it names. Nothing creates the database itself: its absence afterwards is
// what proves a command stopped before opening the deployment.
func writeDeploymentConfig(t *testing.T, dir string) string {
	t.Helper()
	sqlitePath := filepath.Join(dir, "data", "yolorouter.db")
	if err := os.MkdirAll(filepath.Join(dir, "configs"), 0o755); err != nil {
		t.Fatalf("mkdir configs: %v", err)
	}
	body := "server:\n    port: 8080\ndatabase:\n    driver: sqlite\n    sqlite_path: " + sqlitePath +
		"\nsecurity:\n    provider_master_key: dGVzdC1rZXktZm9yLWxvYWQtZXhpc3RpbmctdGVzdHM=\n"
	if err := os.WriteFile(filepath.Join(dir, "configs", "config.yaml"), []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return sqlitePath
}
