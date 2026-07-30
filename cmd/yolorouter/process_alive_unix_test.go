//go:build !windows

package main

import (
	"os"
	"syscall"
	"testing"
)

// assertProcessAlive fails the test unless p is still running. Signal 0 probes
// liveness without delivering anything: the kernel performs the permission and
// existence checks and returns ESRCH if the process is gone.
func assertProcessAlive(t *testing.T, p *os.Process) {
	t.Helper()
	if err := p.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("process %d should still be alive: %v", p.Pid, err)
	}
}
