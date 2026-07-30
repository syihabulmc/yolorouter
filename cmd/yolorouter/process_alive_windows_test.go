//go:build windows

package main

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

// stillActive is the exit code GetExitCodeProcess reports for a process that
// has not exited (STILL_ACTIVE).
const stillActive = 259

// assertProcessAlive fails the test unless p is still running. os.Process.Signal
// cannot be used here: on windows it rejects every signal except Kill with
// EWINDOWS, so a signal-0 liveness probe would report failure for a perfectly
// healthy process. Querying the exit code is the equivalent check.
func assertProcessAlive(t *testing.T, p *os.Process) {
	t.Helper()
	// PROCESS_QUERY_LIMITED_INFORMATION is enough for GetExitCodeProcess and
	// succeeds against processes the full query right would be denied for.
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(p.Pid))
	if err != nil {
		t.Fatalf("process %d should still be alive, but could not be opened: %v", p.Pid, err)
	}
	defer func() { _ = windows.CloseHandle(h) }()

	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		t.Fatalf("GetExitCodeProcess for pid %d: %v", p.Pid, err)
	}
	if code != stillActive {
		t.Fatalf("process %d should still be alive, but has exited with code %d", p.Pid, code)
	}
}
