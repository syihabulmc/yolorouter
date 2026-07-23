//go:build !windows

package main

// newStopWaiter is a no-op on unix: external stop requests arrive as SIGTERM,
// which serve already handles via signal.Notify. Returning a nil channel is
// deliberate — a receive on a nil channel blocks forever, so the select arm
// that reads it never fires. sqlitePath is unused here; the windows build uses
// it to name the stop event.
func newStopWaiter(sqlitePath string) (<-chan struct{}, func(), error) {
	return nil, func() {}, nil
}
