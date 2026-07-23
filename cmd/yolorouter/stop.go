package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/yolorouter/yolorouter/internal/config"
	"github.com/yolorouter/yolorouter/pkg/database"
)

// runStop stops the local running server. It resolves the data location from
// config only (no database connection), then hands off to stopInstance.
func runStop(ctx context.Context, args []string) error {
	flagSet, err := parseCommandFlags("stop", args, 0, nil)
	if err != nil {
		return err
	}
	cfg, err := config.Load(flagSet.Lookup("config").Value.String())
	if err != nil {
		return err
	}
	return stopInstance(cfg.Database.SQLitePath)
}

// stopInstanceTimeout bounds how long stop waits for the server to exit after
// signaling it. It is slightly larger than serve's ~15s graceful budget.
const stopInstanceTimeout = 20 * time.Second

// stopInstance signals the running server (if any) to shut down and waits for
// it to exit. Liveness is decided by the serve-liveness lock, not the pid file
// and not the shared instance lock: if stop can take the serve lock, no server
// is running and any leftover pid file is stale (even if db:reset is currently
// holding the instance lock). Only when the serve lock is genuinely held —
// meaning a live serve wrote the pid file — does stop read the pid and signal
// it, so a stale pid whose number was reused by an unrelated process is never
// signaled.
func stopInstance(sqlitePath string) error {
	lockPath := serveInstanceLockPath(sqlitePath)
	pidPath := instancePIDPath(sqlitePath)

	// Acquiring the lock means the server has fully exited: drop any leftover
	// pid file, release the lock, and report. Used both for the initial
	// "is anything running?" probe and for each poll after signaling.
	releaseAndReport := func(unlock func() error, msg string) error {
		_ = os.Remove(pidPath)
		_ = unlock()
		fmt.Println(msg)
		return nil
	}

	if unlock, err := database.AcquireInstanceLock(lockPath); err == nil {
		return releaseAndReport(unlock, "no running instance")
	}

	// The serve lock is held, so a server is running (or just starting up). Read
	// its pid, retrying briefly to cover the small startup window between the
	// server taking the serve lock and writing its pid file.
	pid, err := readPIDFileUntil(pidPath, time.Now().Add(2*time.Second))
	if err != nil {
		return fmt.Errorf("an instance is running but its pid file is missing or unreadable at %s: %w", pidPath, err)
	}

	if err := signalStop(pid, sqlitePath); err != nil {
		return fmt.Errorf("signal stop to pid %d: %w", pid, err)
	}

	deadline := time.Now().Add(stopInstanceTimeout)
	for {
		if unlock, err := database.AcquireInstanceLock(lockPath); err == nil {
			return releaseAndReport(unlock, fmt.Sprintf("stopped (pid %d)", pid))
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("instance (pid %d) did not exit within %s", pid, stopInstanceTimeout)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// readPIDFileUntil reads the pid file, retrying until deadline. This covers the
// brief startup window where a server already holds the serve lock but has not
// yet written its pid file.
func readPIDFileUntil(path string, deadline time.Time) (int, error) {
	for {
		pid, err := readPIDFile(path)
		if err == nil {
			return pid, nil
		}
		if time.Now().After(deadline) {
			return 0, err
		}
		time.Sleep(100 * time.Millisecond)
	}
}
