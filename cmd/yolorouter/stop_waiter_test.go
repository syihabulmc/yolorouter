package main

import (
	"testing"
	"time"
)

// On unix the waiter never fires on its own (external stop arrives via
// SIGTERM), so its channel must not be readable and cleanup must be safe.
func TestStopWaiterUnixNeverFires(t *testing.T) {
	ch, cleanup, err := newStopWaiter("/tmp/does-not-matter/yolorouter.db")
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
