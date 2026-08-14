package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/yolorouter/yolorouter/internal/config"
	"github.com/yolorouter/yolorouter/internal/selfupdate"
	"github.com/yolorouter/yolorouter/internal/version"
)

// latestLookupTimeout bounds the release lookup the windows guidance path
// performs (it only prints the latest tag + URL, never downloads assets).
const latestLookupTimeout = 30 * time.Second

// runUpdate is the `yolorouter update` command: a semi-automatic upgrade
// that resolves the latest release, verifies its checksum, backs up the
// running binary, and atomically replaces it — then leaves the actual
// restart to the operator. The mechanics live in internal/selfupdate (shared
// with the admin console's update endpoint); this command adds the
// interactive confirmation and the printed rollback guidance. It
// deliberately does NOT go through bootstrap.Init: an upgrade only needs
// config.update.*, never a live database connection (same shape as
// db:backup).
func runUpdate(ctx context.Context, args []string) error {
	flagSet, err := parseCommandFlags("update", args, 0, nil)
	if err != nil {
		return err
	}
	configPath := flagSet.Lookup("config").Value.String()
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Inside a container the image is immutable: a replaced binary vanishes
	// on the next container recreate, and the running container would report
	// a version its image tag no longer matches. Point at the image-based
	// upgrade path instead of attempting a replacement — checked before the
	// repo resolution because the answer is "pull the image" regardless of
	// whether a release source is configured.
	if selfupdate.InContainer() {
		fmt.Println("in-place update is disabled inside a container")
		fmt.Println("upgrade by pulling the newer image and recreating the container, e.g.:")
		fmt.Println("  docker compose pull && docker compose up -d")
		return nil
	}

	repo := version.ResolveRepo(cfg.Update.Enabled, cfg.Update.GitHubRepo)
	// When set, route the release lookup and both asset downloads through the
	// configured mirror (deployments where GitHub is slow or blocked).
	proxy := cfg.Update.GitHubProxy

	// Windows can't atomically replace a running .exe (the file is locked),
	// so automatic update is unsupported there — point the user at the
	// release page instead of attempting a replacement that would fail
	// mid-way and leave no binary behind. Like the container branch above,
	// this outranks the disabled-repo error (matching selfupdate.Mode's
	// precedence): a configured repo only upgrades the message with the
	// latest tag + URL, it never changes the answer.
	if runtime.GOOS == "windows" {
		fmt.Printf("automatic update is unsupported on windows\n")
		if repo == "" {
			fmt.Printf("download the latest release manually and replace the binary\n")
			return nil
		}
		client := &http.Client{Timeout: latestLookupTimeout}
		rel, err := selfupdate.FetchLatestRelease(ctx, client, repo, proxy)
		if err != nil {
			return fmt.Errorf("the latest-release lookup also failed: %w", err)
		}
		fmt.Printf("download the latest release (%s) manually from: %s\n", rel.TagName, rel.HTMLURL)
		return nil
	}

	if repo == "" {
		return fmt.Errorf("update is disabled: set update.enabled=true and/or update.github_repo, or rebuild with DEFAULT_GITHUB_REPO")
	}

	res, err := selfupdate.Apply(ctx, selfupdate.Options{
		Repo:  repo,
		Proxy: proxy,
		// The CLI leaves the restart to the operator and prints the
		// capability-reapply note below, so replacing a capability-bearing
		// binary is safe here — unlike the auto-restarting update endpoint.
		AllowCapabilityLoss: true,
		Current:             version.Version,
		Confirm: func(current, target string) bool {
			fmt.Printf("Upgrade yolorouter %s -> %s?\n\n", current, target)
			fmt.Println("WARNING: restarting after upgrade will run database migrations.")
			fmt.Println("For a full rollback, back up the database first:")
			fmt.Println(backupCommand(configPath))
			fmt.Println("The .bak binary backup only allows rollback BEFORE you restart.")
			fmt.Print("\nContinue? [y/N] ")
			var answer string
			_, _ = fmt.Scanln(&answer)
			return strings.ToLower(strings.TrimSpace(answer)) == "y"
		},
	})
	if errors.Is(err, selfupdate.ErrCancelled) {
		fmt.Println("upgrade cancelled")
		return nil
	}
	if err != nil {
		return err
	}
	if res.UpToDate {
		fmt.Printf("already up to date (%s)\n", res.Target)
		return nil
	}

	fmt.Printf("upgraded to %s\n", res.Target)
	fmt.Printf("binary backup: %s\n", res.BackupPath)
	fmt.Printf("  rollback BEFORE restart: stop the service, then: mv %s %s\n", shellQuote(res.BackupPath), shellQuote(strings.TrimSuffix(res.BackupPath, ".bak")))
	if runtime.GOOS == "linux" {
		// Atomic rename creates a fresh inode; Linux file capabilities
		// (security.capability xattr, e.g. cap_net_bind_service via setcap)
		// do not carry over, so a non-root service binding a privileged port
		// would fail to restart. Tell the operator to reapply them.
		fmt.Println("note: if this binary uses Linux file capabilities (e.g. cap_net_bind_service),")
		fmt.Println("      reapply them on the new binary — atomic rename does not preserve xattrs.")
	}
	fmt.Println("restart yolorouter to apply (will run database migrations)")
	return nil
}

// shellQuote wraps s in single quotes so the shell treats it literally.
// Embedded single quotes are escaped by ending the single-quoted string,
// emitting a backslash-escaped single quote, and starting a new single-quoted
// string (the standard shell idiom; written out in prose because gofmt rewrites
// the literal escape sequence inside a comment). Go's %q produces a
// double-quoted string where $, backticks, and dollar-parens still expand or
// execute when an operator pastes a printed command — unsafe for paths the
// user will paste (config path, rollback mv operands).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// backupCommand returns the db:backup hint string for the rollback notice.
// With no --config, the bare command loads the default config (the same one
// update used); appending `--config ` with an empty value would fail the flag
// parse, so the flag is omitted for the default path and shell-quoted
// otherwise.
func backupCommand(configPath string) string {
	if configPath == "" {
		return "  yolorouter db:backup"
	}
	return "  yolorouter db:backup --config " + shellQuote(configPath)
}
