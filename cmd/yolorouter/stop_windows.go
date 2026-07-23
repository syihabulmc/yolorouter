//go:build windows

package main

import (
	"errors"

	"golang.org/x/sys/windows"
)

// signalStop opens the server's stop event and sets it, which wakes serve's
// waiter and drives a graceful shutdown. pid is unused on windows: the event
// (keyed by sqlitePath) is how the shutdown is delivered. A "file not found"
// error means the event does not exist — the server is not listening (already
// gone, or in its earliest startup window before the event is created) — which
// is not a failure, so this is a no-op and the caller's lock poll converges.
// Any other OpenEvent error is real (e.g. an access-denied or name collision)
// and is propagated rather than silently swallowed.
func signalStop(pid int, sqlitePath string) error {
	name, err := windows.UTF16PtrFromString(stopEventName(sqlitePath))
	if err != nil {
		return err
	}
	h, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, name)
	if err != nil {
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return nil
		}
		return err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	return windows.SetEvent(h)
}
