//go:build !windows

package main

import (
	"path/filepath"
	"testing"
	"time"
)

// On unix the waiter is a deliberate no-op: an external stop arrives as
// SIGTERM, which serve handles via signal.Notify. The returned channel must
// therefore never become readable, and cleanup must be safe to call anyway.
func TestStopWaiterNeverFiresOnUnix(t *testing.T) {
	ch, cleanup, err := newStopWaiter(filepath.Join(t.TempDir(), "yolorouter.db"))
	if err != nil {
		t.Fatalf("newStopWaiter: %v", err)
	}
	defer cleanup()

	select {
	case <-ch:
		t.Fatal("unix stop waiter channel should never fire")
	case <-time.After(100 * time.Millisecond):
	}
}
