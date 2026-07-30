//go:build windows

package main

import (
	"path/filepath"
	"testing"
	"time"
)

// On windows the waiter is the actual stop delivery mechanism, not a no-op:
// it creates the named event that signalStop sets. This is the round trip a
// running server depends on, so it is asserted end to end rather than only
// checking that the channel exists.
func TestStopWaiterFiresWhenSignalStopSetsTheEvent(t *testing.T) {
	sqlitePath := filepath.Join(t.TempDir(), "yolorouter.db")

	ch, cleanup, err := newStopWaiter(sqlitePath)
	if err != nil {
		t.Fatalf("newStopWaiter: %v", err)
	}
	defer cleanup()

	// pid is unused on windows — the event keyed by sqlitePath is the channel.
	if err := signalStop(0, sqlitePath); err != nil {
		t.Fatalf("signalStop: %v", err)
	}

	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("stop waiter should fire after signalStop sets the event")
	}
}

// signalStop must not fail when no server is listening: the event simply does
// not exist. stopInstance relies on this being a silent no-op so its lock poll
// is what decides the outcome.
func TestSignalStopIsNoOpWhenEventMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-server-here.db")
	if err := signalStop(0, missing); err != nil {
		t.Fatalf("signalStop should be a no-op when the event does not exist: %v", err)
	}
}

// Event names are matched byte-for-byte by the kernel while windows paths are
// case-insensitive, so serve and stop must fold path spelling variants to one
// identity or stop could never reach the server's event.
func TestStopEventNameIsCaseAndSeparatorInsensitive(t *testing.T) {
	base := `C:\Users\Test\yolorouter\data\yolorouter.db`
	variants := []string{
		base,
		`c:\users\test\yolorouter\data\yolorouter.db`,
		`C:\Users\Test\yolorouter\data\.\yolorouter.db`,
		`C:\Users\Test\yolorouter\extra\..\data\yolorouter.db`,
	}
	want := stopEventName(base)
	for _, v := range variants {
		if got := stopEventName(v); got != want {
			t.Errorf("stopEventName(%q) = %q, want %q — stop would not reach serve", v, got, want)
		}
	}

	if same := stopEventName(`C:\other\deployment\yolorouter.db`); same == want {
		t.Error("distinct deployments must not share a stop event name")
	}
}
