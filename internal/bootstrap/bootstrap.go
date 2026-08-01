// Package bootstrap provides the shared initialization sequence (config
// load/generate + validate -> logger -> database connection) used by every
// resource-initializing subcommand (serve, db:migrate, db:rollback, db:status,
// ...). help/version/unknown-command paths never call Init.
package bootstrap

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/yolorouter/yolorouter/internal/config"
	"github.com/yolorouter/yolorouter/pkg/database"
	"github.com/yolorouter/yolorouter/pkg/logger"
)

// App bundles the shared dependencies every resource-initializing subcommand
// (serve, db:migrate, db:rollback, ...) needs. help/version/unknown-command
// paths never call Init.
type App struct {
	Config *config.Config
	DB     *gorm.DB
	// ConfigPath is the absolute path of the config file Init loaded. Commands
	// that have to name the deployment they act on (db:reset's confirmation)
	// print it: config resolution reaches beyond the process cwd, so the driver
	// name alone can't tell a throwaway sandbox apart from a real install.
	ConfigPath string
}

func Init(explicitConfigPath string) (*App, error) {
	// The path comes back from the same call that read the file, so a caller
	// that prints it names the file that was actually loaded.
	cfg, cfgPath, err := config.LoadWithPath(explicitConfigPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return InitWithConfig(cfg, cfgPath)
}

// InitWithConfig is Init for a caller that has already loaded a config and
// committed to what it said — db:rollback, which names a deployment, locks it,
// and then drops schema from it. Passing the same path to Init instead would
// read the file a second time, and the answer need not match: an atomic
// replacement or a retargeted symlink in between leaves the lock held on one
// database while the migrations run against another, which is precisely the
// mutual exclusion the lock exists to provide.
//
// cfg must have come from the config package's own loaders. Nothing here
// re-validates it, so a hand-built value would skip the checks Load performs —
// zero-value gateway timeouts, an unset log level — and fail much later, in the
// relay.
func InitWithConfig(cfg *config.Config, cfgPath string) (*App, error) {
	if cfg == nil {
		return nil, fmt.Errorf("init: no config given")
	}
	// Absolute, because every consumer either prints it for a human to act on
	// or pastes it into a command; an empty or relative one would name whatever
	// the caller's working directory happens to be. Callers get this from the
	// loaders, which already return an absolute path, so a violation is a
	// programming error rather than something an operator can cause.
	if !filepath.IsAbs(cfgPath) {
		return nil, fmt.Errorf("init: config path %q is not absolute", cfgPath)
	}

	// logger.Init has no failure mode (it never returns an error).
	logger.Init(logger.Config{Level: cfg.Log.Level})

	// Surface the platforms where the config file's permissions cannot be
	// checked (currently Windows — see config.PermEnforcementSupported). This
	// has to happen here rather than inside config.Load, which runs before the
	// logger exists because it is what supplies the log level. Warning once at
	// startup keeps a known gap in the operator's view instead of degrading
	// silently.
	if !config.PermEnforcementSupported {
		// The hints below are meant to be pasted verbatim, which is why
		// cfgPath is absolute: a relative path would either fail outright or,
		// worse, restrict some unrelated file of the same name while the real
		// master-key file stays exposed.
		logger.Warn("config file permissions are not enforced on this platform; the file stores the master key that encrypts upstream credentials, so restrict access to it yourself",
			zap.String("path", cfgPath),
			zap.String("restrict", restrictFileHint(cfgPath)),
			// The restrict command clears the broad principals a file is
			// realistically exposed through, but it cannot guarantee an empty
			// DACL for an arbitrary one, so the result is worth confirming
			// rather than assuming.
			zap.String("then_verify", verifyFileACLHint(cfgPath)))
	}

	dbCfg := database.Config{
		Driver:     cfg.Database.Driver,
		SQLitePath: cfg.Database.SQLitePath,
	}
	if cfg.Database.Driver == "postgres" {
		dbCfg.PostgresDSN = buildPostgresDSN(cfg.Database)
	}

	// database.Init sets the package-level database.DB variable rather than
	// returning the *gorm.DB directly (see pkg/database/database.go).
	if err := database.Init(dbCfg); err != nil {
		return nil, fmt.Errorf("init database: %w", err)
	}

	return &App{Config: cfg, DB: database.DB, ConfigPath: cfgPath}, nil
}

// Close releases the database connection and flushes buffered log output.
// Failures here are logged rather than escalated as an overall command
// failure (the same "log and continue" convention gracefulShutdown uses for
// its own cleanup phases) — but they must still be logged, not silently
// discarded, or a real close/flush failure (e.g. a pending transaction that
// wouldn't commit, or a full disk dropping the last buffered log lines)
// becomes completely invisible.
func (a *App) Close() error {
	sqlDB, err := a.DB.DB()
	if err == nil {
		if closeErr := sqlDB.Close(); closeErr != nil {
			logger.Error("close database connection", zap.Error(closeErr))
		}
	}
	if syncErr := logger.Sync(); syncErr != nil {
		logger.Error("flush log output", zap.Error(syncErr))
	}
	return nil
}

// buildPostgresDSN builds a libpq keyword/value connection string, properly
// quoting and escaping each value — a plain fmt.Sprintf (as an earlier
// version did) breaks as soon as any value contains whitespace, a single
// quote, or a backslash (all valid in a Postgres password, for instance).
// sslmode defaults to "disable" only because DatabaseConfig.SSLMode itself
// defaults to "disable" for local-dev convenience (see internal/config);
// remote/production deployments should set database.sslmode explicitly.
func buildPostgresDSN(db config.DatabaseConfig) string {
	sslMode := db.SSLMode
	if sslMode == "" {
		sslMode = "disable"
	}
	parts := []string{
		"host=" + escapeDSNValue(db.Host),
		"port=" + strconv.Itoa(db.Port),
		"user=" + escapeDSNValue(db.User),
		"password=" + escapeDSNValue(db.Password),
		"dbname=" + escapeDSNValue(db.DBName),
		"sslmode=" + escapeDSNValue(sslMode),
	}
	return strings.Join(parts, " ")
}

// escapeDSNValue quotes and escapes a single libpq keyword/value pair's
// value per the format documented for PQconnectdb: values containing
// whitespace or a single quote must be wrapped in single quotes, with
// embedded backslashes and single quotes backslash-escaped.
func escapeDSNValue(v string) string {
	if v == "" {
		return "''"
	}
	if !strings.ContainsAny(v, " '\\\t\n\r\v\f") {
		return v
	}
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `'`, `\'`)
	return "'" + v + "'"
}
