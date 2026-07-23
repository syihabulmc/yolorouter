//go:build !windows

package database

import "syscall"

// withUmask temporarily sets the process umask (Unix-only) and returns a
// restore func — some permission regression tests would otherwise pass or
// fail depending on whatever umask happens to be set in the environment
// running them (e.g. a CI container with umask 077 would mask SQLite's own
// default 0644 create mode down to 0600 by coincidence, hiding a real
// regression).
//
// syscall.Umask mutates process-wide state, not anything per-goroutine or
// per-test — callers of this helper must NOT call t.Parallel(), or a
// concurrently-running test doing its own file creation could observe (or
// race to set) the wrong umask.
func withUmask(mask int) func() {
	old := syscall.Umask(mask)
	return func() { syscall.Umask(old) }
}
