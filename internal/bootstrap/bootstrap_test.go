package bootstrap

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yolorouter/yolorouter/internal/config"
)

func TestInitLoadsConfigAndConnectsDatabase(t *testing.T) {
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

	app, err := Init("")
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer func() { _ = app.Close() }()

	if app.Config == nil {
		t.Fatalf("expected non-nil Config")
	}
	if app.DB == nil {
		t.Fatalf("expected non-nil DB")
	}
}

// TestInitCreatesSQLiteFileWithRestrictivePermissions guards against the
// main database file being exposed with SQLite's own default create mode
// (0644 & umask, typically world/group-readable) — database.Init must
// pre-create it at 0600 before ever handing the path to SQLite.
//
// Pinned to umask 022 (not left as whatever the environment happens to
// have) so this test can't pass by coincidence: a CI container running
// with umask 077 would mask even a regressed 0644 create mode down to
// 0600. syscall.Umask is process-global state, so this test must not be
// marked t.Parallel().
func TestInitCreatesSQLiteFileWithRestrictivePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("asserts unix file-permission bits")
	}
	defer withUmask(0o022)()

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

	app, err := Init("")
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer func() { _ = app.Close() }()

	info, statErr := os.Stat(app.Config.Database.SQLitePath)
	if statErr != nil {
		t.Fatalf("stat sqlite database file: %v", statErr)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected sqlite database file to be created with 0600 permissions, got %04o", info.Mode().Perm())
	}
}

func TestInitFailsForInvalidExplicitConfig(t *testing.T) {
	dir := t.TempDir()
	_, err := Init(dir + "/does-not-exist.yaml")
	if err == nil {
		t.Fatalf("expected error for missing explicit config path")
	}
}

func TestBuildPostgresDSNEscapesSpecialCharacters(t *testing.T) {
	dsn := buildPostgresDSN(config.DatabaseConfig{
		Host:     "localhost",
		Port:     5432,
		User:     "app user", // contains a space
		Password: `p'\ss`,    // contains a single quote and a backslash
		DBName:   "yolorouter",
		SSLMode:  "require",
	})

	if !strings.Contains(dsn, `user='app user'`) {
		t.Fatalf("expected quoted user with space, got: %s", dsn)
	}
	if !strings.Contains(dsn, `password='p\'\\ss'`) {
		t.Fatalf("expected escaped quote and backslash in password, got: %s", dsn)
	}
	if !strings.Contains(dsn, "sslmode=require") {
		t.Fatalf("expected explicit sslmode to be respected, got: %s", dsn)
	}
}

func TestBuildPostgresDSNDefaultsSSLModeToDisable(t *testing.T) {
	dsn := buildPostgresDSN(config.DatabaseConfig{Host: "localhost", Port: 5432, User: "u", Password: "p", DBName: "d"})
	if !strings.Contains(dsn, "sslmode=disable") {
		t.Fatalf("expected default sslmode=disable when unset, got: %s", dsn)
	}
}

func TestEscapeDSNValueHandlesEmptyAndPlainValues(t *testing.T) {
	if got := escapeDSNValue(""); got != "''" {
		t.Fatalf("expected '' for empty value, got: %s", got)
	}
	if got := escapeDSNValue("plainvalue"); got != "plainvalue" {
		t.Fatalf("plain value should not be quoted, got: %s", got)
	}
}

// TestEscapeDSNValueQuotesVerticalTabAndFormFeed guards against a narrower
// whitespace character class than libpq's own isspace()-based DSN parser
// uses — an earlier version only checked " \t\n\r" and would pass \v/\f
// through unquoted, letting them corrupt the keyword/value boundary.
func TestEscapeDSNValueQuotesVerticalTabAndFormFeed(t *testing.T) {
	if got, want := escapeDSNValue("a\vb"), "'a\vb'"; got != want {
		t.Fatalf("expected vertical tab to be wrapped in quotes, got %q, want %q", got, want)
	}
	if got, want := escapeDSNValue("a\fb"), "'a\fb'"; got != want {
		t.Fatalf("expected form feed to be wrapped in quotes, got %q, want %q", got, want)
	}
}

// TestInitRecordsAbsoluteConfigPath: commands that name the deployment they are
// about to act on (db:reset's confirmation) read App.ConfigPath, so it must be
// the absolute path Init actually loaded — a relative one would read as the
// caller's cwd, which for a service-launched process is nowhere near the
// install directory.
func TestInitRecordsAbsoluteConfigPath(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	app, err := Init("")
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer func() { _ = app.Close() }()

	if !filepath.IsAbs(app.ConfigPath) {
		t.Fatalf("ConfigPath = %q, want an absolute path", app.ConfigPath)
	}
	want := filepath.Join("configs", "config.yaml")
	if filepath.Base(app.ConfigPath) != "config.yaml" || !strings.HasSuffix(app.ConfigPath, want) {
		t.Fatalf("ConfigPath = %q, want it to end in %q", app.ConfigPath, want)
	}
	// "absolute and readable" alone would also pass for some other config file
	// on disk. The loaded master key is unique to the file Init actually read,
	// so finding it in the named file is what proves the two are the same one.
	body, readErr := os.ReadFile(app.ConfigPath)
	if readErr != nil {
		t.Fatalf("ConfigPath does not point at a readable file: %v", readErr)
	}
	if !strings.Contains(string(body), app.Config.Security.ProviderMasterKey) {
		t.Fatalf("ConfigPath %q is not the file the loaded config came from", app.ConfigPath)
	}
}

// TestInitWithConfigRejectsAnUnusableConfig: this entry point skips the loaders
// and so skips their validation, which is the point — db:rollback locks a
// deployment and then drops schema from it, and re-reading the file in between
// could name a different one. What it must not do is accept a value no loader
// would have produced, and then fail somewhere far from the cause.
func TestInitWithConfigRejectsAnUnusableConfig(t *testing.T) {
	t.Run("no config", func(t *testing.T) {
		if _, err := InitWithConfig(nil, "/somewhere/configs/config.yaml"); err == nil {
			t.Fatal("expected an error for a nil config")
		}
	})
	t.Run("relative path", func(t *testing.T) {
		if _, err := InitWithConfig(&config.Config{}, "configs/config.yaml"); err == nil {
			t.Fatal("expected an error for a relative config path")
		}
	})
	t.Run("empty path", func(t *testing.T) {
		if _, err := InitWithConfig(&config.Config{}, ""); err == nil {
			t.Fatal("expected an error for an empty config path")
		}
	})
}
