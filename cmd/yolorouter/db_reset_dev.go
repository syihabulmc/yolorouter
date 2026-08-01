//go:build !release

package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/yolorouter/yolorouter/pkg/database"
)

// confirmDestructive prints prompt and requires the exact word "yes" on in.
// Anything else — a different answer, a closed stdin, a read error — aborts,
// so a command that ends up connected to a terminal-less context cannot take
// silence for agreement.
//
// io.EOF is the one error that still allows a "yes": it is what an answer typed
// without a trailing newline looks like. Any other read error means the answer
// cannot be trusted to be what was typed, even when the bytes read so far spell
// the word.
func confirmDestructive(in io.Reader, prompt string) error {
	fmt.Print(prompt)
	answer, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("aborted: reading confirmation: %w", err)
	}
	if strings.TrimRight(answer, "\r\n") != "yes" {
		return fmt.Errorf("aborted: confirmation not given")
	}
	return nil
}

func runDBReset(ctx context.Context, args []string) error {
	var yes *bool
	_, app, err := bootstrapCommand("db:reset", args, 0, func(fs *flag.FlagSet) {
		yes = fs.Bool("yes", false, "skip interactive confirmation (for scripting)")
	})
	if err != nil {
		return err
	}
	defer func() { _ = app.Close() }()

	fmt.Print(describeTarget(app.Config, app.ConfigPath))
	if !*yes {
		if err := confirmDestructive(os.Stdin, "this will delete ALL data and re-migrate. type \"yes\" to continue: "); err != nil {
			return err
		}
	}

	migrationsFS, dir := migrationsFor(app.Config.Database.Driver)
	lockPath := instanceLockPath(app.Config.Database.SQLitePath)

	if app.Config.Database.Driver == "sqlite" {
		// bootstrapCommand's bootstrap.Init already opened a connection to
		// this exact file (app.DB) — ResetSQLite is about to delete that
		// file out from under it, so close it first. sql.DB.Close is safe to
		// call again later via the deferred app.Close().
		sqlDB, err := app.DB.DB()
		if err != nil {
			return err
		}
		if err := sqlDB.Close(); err != nil {
			return fmt.Errorf("close pre-reset database connection: %w", err)
		}
		// ResetSQLite acquires lockPath itself (see pkg/database/reset_sqlite.go).
		return database.ResetSQLite(app.Config.Database.SQLitePath, lockPath, migrationsFS, dir)
	}

	// ResetPostgres acquires lockPath itself too (see pkg/database/reset_postgres.go) —
	// both Reset* functions are self-contained about the mutual-exclusion
	// precondition against a running `serve` instance.
	sqlDB, err := app.DB.DB()
	if err != nil {
		return err
	}
	return database.ResetPostgres(sqlDB, app.Config.Database.Driver, migrationsFS, dir, lockPath)
}
