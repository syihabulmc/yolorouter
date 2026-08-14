//go:build !windows

package selfupdate

import (
	"os"
	"syscall"
	"time"
)

// ScheduleRestart asks the process to shut down gracefully after a short
// delay, so the caller's HTTP response reaches the client first. It sends
// SIGTERM to the process itself — the exact signal the serve loop already
// handles — so the shutdown drains in-flight requests, and a service manager
// configured to restart (the installers set systemd Restart=always / launchd
// KeepAlive) brings the process back up on the just-installed binary. A
// process started by hand in a foreground shell simply exits; the update
// endpoint's confirmation warns the operator about that case.
func ScheduleRestart() {
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
	}()
}
