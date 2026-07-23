package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// instancePIDPath returns the pid file path a running serve writes so that
// stop can discover which process to signal. It sits next to the instance
// lock file, both derived from the resolved SQLite path so they share one
// stable per-deployment location on either driver.
func instancePIDPath(sqlitePath string) string {
	return sqlitePath + ".pid"
}

// serveInstanceLockPath returns the path of the serve-liveness lock. Unlike the
// instance lock (sqlitePath+".lock"), which serve AND maintenance commands such
// as db:reset all take for mutual exclusion, this lock is held ONLY by a running
// serve, for its entire lifetime. stop uses it to tell "a server is running"
// apart from "some maintenance command happens to hold the instance lock": a
// held serve lock means a live serve wrote the pid file next to it, so that pid
// is safe to signal; a free serve lock means no server is running and any pid
// file is stale — even while db:reset holds the instance lock.
func serveInstanceLockPath(sqlitePath string) string {
	return sqlitePath + ".serve.lock"
}

// writePIDFile records the current process id at path. A plain write (not a
// temp+rename) is sufficient: serve writes this exactly once at startup while
// holding the exclusive instance lock, so there is only ever one writer and
// no reader observes a torn tiny-integer write.
func writePIDFile(path string) error {
	return os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600)
}

// readPIDFile reads and validates the pid recorded at path.
func readPIDFile(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, fmt.Errorf("invalid pid in %s: %w", path, err)
	}
	if pid <= 0 {
		return 0, fmt.Errorf("invalid pid %d in %s", pid, path)
	}
	return pid, nil
}
