package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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
	// The victim must still be alive, proving stopInstance never signaled the
	// stale pid. How liveness is probed is platform-specific (see
	// process_alive_unix_test.go / process_alive_windows_test.go).
	assertProcessAlive(t, victim.Process)
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
// TestStopInstanceStopsRunningChild. It stands in for a running server, so it
// mirrors what serve actually does to become stoppable on each platform: take
// the serve lock, register the stop waiter, publish its pid, then block.
//
// How the stop arrives is platform-specific. On unix newStopWaiter is a no-op
// returning a nil channel and the default SIGTERM disposition terminates this
// process. On windows there is no SIGTERM delivery, so newStopWaiter creates
// the named event stopInstance sets, and the receive below is what ends the
// wait. The timeout is only a backstop against a hung test.
//
// Ordering matters: the waiter is registered BEFORE the pid file is written.
// The parent treats a readable pid file as "helper is ready" and calls
// stopInstance immediately after, and on windows signalStop is a silent no-op
// when the event does not exist yet — so publishing the pid first would race
// the stop request against event creation and leave the lock held.
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

	stopCh, stopCleanup, err := newStopWaiter(sqlitePath)
	if err != nil {
		os.Exit(5)
	}
	defer stopCleanup()

	if err := writePIDFile(instancePIDPath(sqlitePath)); err != nil {
		os.Exit(4)
	}

	select {
	case <-stopCh:
	case <-time.After(60 * time.Second):
	}
}

// TestRunStopWithoutConfigFailsLoudly is the regression test for stop reporting
// "no running instance" while the server was running: run from a directory with
// no configs/config.yaml, stop used to generate a fresh config there and then
// probe that new, empty deployment's lock — which nobody held. It must now fail
// with a message naming the path instead, and leave nothing behind.
func TestRunStopWithoutConfigFailsLoudly(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	// --config, not the default resolution: with no flag, runStop consults the
	// executable's installation, so on a machine that has one this test would
	// probe — and could signal — the operator's live server. Which file the
	// default path resolves to is covered in internal/config, where the
	// executable lookup is injectable.
	err := runStop(context.Background(), []string{"--config", filepath.Join(dir, "configs", "config.yaml")})
	if err == nil {
		t.Fatal("expected an error when the config does not exist")
	}
	if !strings.Contains(err.Error(), "config file not found") {
		t.Fatalf("error should name the missing config, got: %v", err)
	}
	if !strings.Contains(err.Error(), dir) {
		t.Fatalf("error should name the path it tried, got: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "configs", "config.yaml")); !os.IsNotExist(statErr) {
		t.Fatalf("stop must not generate a config, stat err = %v", statErr)
	}
}
