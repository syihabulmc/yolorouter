package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/yolorouter/yolorouter/pkg/database"
)

// TestStopInstanceNoInstance: no server running -> success, prints nothing
// fatal, and removes a stale pid file if present.
func TestStopInstanceNoInstance(t *testing.T) {
	dir := t.TempDir()
	sqlitePath := filepath.Join(dir, "yolorouter.db")
	// stale pid file left over from a crashed run
	if err := os.WriteFile(instancePIDPath(sqlitePath), []byte("999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := stopInstance(sqlitePath); err != nil {
		t.Fatalf("stopInstance should succeed when nothing is running: %v", err)
	}
	if _, err := os.Stat(instancePIDPath(sqlitePath)); !os.IsNotExist(err) {
		t.Fatalf("stale pid file should have been removed")
	}
}

// TestStopInstanceLockHeldPIDMissing: a server holds the serve lock but the pid
// file is gone -> error explaining the pid cannot be determined. stopInstance
// retries the pid read briefly first, so this returns after that short window.
func TestStopInstanceLockHeldPIDMissing(t *testing.T) {
	dir := t.TempDir()
	sqlitePath := filepath.Join(dir, "yolorouter.db")
	unlock, err := database.AcquireInstanceLock(serveInstanceLockPath(sqlitePath))
	if err != nil {
		t.Fatalf("test setup: acquire serve lock: %v", err)
	}
	defer func() { _ = unlock() }()

	if err := stopInstance(sqlitePath); err == nil {
		t.Fatal("expected error when serve lock is held but pid file is missing")
	}
}

// TestStopInstanceIgnoresInstanceLockHolder guards the server/pid ownership
// invariant: holding the *instance* lock (as db:reset does) must NOT make stop
// believe a server is running. A stale pid file present during a db:reset must
// never be signaled, or stop could SIGTERM an unrelated recycled process.
func TestStopInstanceIgnoresInstanceLockHolder(t *testing.T) {
	dir := t.TempDir()
	sqlitePath := filepath.Join(dir, "yolorouter.db")

	// Simulate db:reset: hold the instance lock but NOT the serve lock.
	unlock, err := database.AcquireInstanceLock(instanceLockPath(sqlitePath))
	if err != nil {
		t.Fatalf("test setup: acquire instance lock: %v", err)
	}
	defer func() { _ = unlock() }()

	// A live, unrelated process whose pid a stale pid file happens to point at.
	// It must still be alive after stopInstance runs.
	victim := exec.Command(os.Args[0], "-test.run=TestHelperSleeps")
	victim.Env = append(os.Environ(), "GO_WANT_HELPER_SLEEP=1")
	if err := victim.Start(); err != nil {
		t.Fatalf("start victim: %v", err)
	}
	t.Cleanup(func() { _ = victim.Process.Kill() })
	if err := os.WriteFile(instancePIDPath(sqlitePath), []byte(strconv.Itoa(victim.Process.Pid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := stopInstance(sqlitePath); err != nil {
		t.Fatalf("stopInstance should report no running server: %v", err)
	}
	// signal 0 probes liveness without delivering a signal: the victim must
	// still be alive, proving stopInstance never signaled the stale pid.
	if err := victim.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("stopInstance wrongly signaled an unrelated process: %v", err)
	}
}

// TestHelperSleeps is not a real test: it is the unrelated victim process spawned
// by TestStopInstanceIgnoresInstanceLockHolder. It just blocks so its pid is a
// live, signalable target.
func TestHelperSleeps(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_SLEEP") != "1" {
		return
	}
	time.Sleep(60 * time.Second)
}

// TestStopInstanceStopsRunningChild: spawn a helper process that holds the
// lock and writes its pid; stopInstance must signal it, and once it exits the
// lock frees and stopInstance returns nil.
func TestStopInstanceStopsRunningChild(t *testing.T) {
	dir := t.TempDir()
	sqlitePath := filepath.Join(dir, "yolorouter.db")

	cmd := exec.Command(os.Args[0], "-test.run=TestHelperHoldsLock")
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_LOCK=1", "HELPER_SQLITE_PATH="+sqlitePath)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	// Wait until the helper has taken the lock and written its pid file. The
	// helper acquires the lock before writing the pid, so a readable pid file
	// already implies the lock is held.
	pidPath := instancePIDPath(sqlitePath)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := readPIDFile(pidPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper never acquired lock + wrote pid file")
		}
		time.Sleep(50 * time.Millisecond)
	}

	if err := stopInstance(sqlitePath); err != nil {
		t.Fatalf("stopInstance should stop the running helper: %v", err)
	}
	_ = cmd.Wait()
}

// TestHelperHoldsLock is not a real test: it is the child process spawned by
// TestStopInstanceStopsRunningChild. It acquires the serve lock, writes its
// pid, and blocks until the default SIGTERM disposition terminates it (which
// releases the flock via process exit).
func TestHelperHoldsLock(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_LOCK") != "1" {
		return
	}
	sqlitePath := os.Getenv("HELPER_SQLITE_PATH")
	unlock, err := database.AcquireInstanceLock(serveInstanceLockPath(sqlitePath))
	if err != nil {
		os.Exit(3)
	}
	defer func() { _ = unlock() }()
	if err := writePIDFile(instancePIDPath(sqlitePath)); err != nil {
		os.Exit(4)
	}
	time.Sleep(60 * time.Second) // outlived by SIGTERM from stopInstance
}
