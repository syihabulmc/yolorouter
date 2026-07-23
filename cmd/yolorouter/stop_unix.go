//go:build !windows

package main

import (
	"errors"
	"syscall"
)

// signalStop asks the running server to shut down gracefully. On unix that is
// a SIGTERM, which serve's signal handler turns into a graceful shutdown.
// sqlitePath is unused here; it exists for the windows counterpart, which
// signals a named event keyed by that path. An ESRCH result means the process
// already exited between the liveness check and now, which is not an error:
// the caller's lock poll will confirm the exit.
func signalStop(pid int, sqlitePath string) error {
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
