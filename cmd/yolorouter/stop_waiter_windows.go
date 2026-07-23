//go:build windows

package main

import "golang.org/x/sys/windows"

// newStopWaiter creates the manual-reset stop event and returns a channel that
// closes when another process (the `stop` command) sets it. Windows has no
// SIGTERM delivery to a detached process, so this event is how a running
// server learns it should shut down. The returned cleanup stops the watcher
// goroutine and closes the event handle.
func newStopWaiter(sqlitePath string) (<-chan struct{}, func(), error) {
	name, err := windows.UTF16PtrFromString(stopEventName(sqlitePath))
	if err != nil {
		return nil, nil, err
	}
	// manualReset=1, initialState=0. CreateEvent returns a valid handle even
	// when the event already exists (err == ERROR_ALREADY_EXISTS); only a
	// zero handle is a real failure.
	h, err := windows.CreateEvent(nil, 1, 0, name)
	if h == 0 {
		return nil, nil, err
	}

	stopCh := make(chan struct{})
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		for {
			// Poll the event with a short timeout so `done` (fired by cleanup
			// when serve exits without an external stop) can unblock us.
			r, _ := windows.WaitForSingleObject(h, 250)
			if r == windows.WAIT_OBJECT_0 {
				close(stopCh)
				return
			}
			if r == windows.WAIT_FAILED {
				// The wait itself failed (e.g. the handle became invalid).
				// Stop polling rather than spin; event-based stop is no longer
				// possible, but ctx-based shutdown still works.
				return
			}
			select {
			case <-done:
				return
			default:
			}
		}
	}()

	cleanup := func() {
		// Signal the poller to stop, then wait until it has actually returned
		// (it may be mid-WaitForSingleObject on h) before closing the handle,
		// so the handle is never closed while another thread is waiting on it.
		close(done)
		<-exited
		_ = windows.CloseHandle(h)
	}
	return stopCh, cleanup, nil
}
