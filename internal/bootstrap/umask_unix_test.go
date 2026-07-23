//go:build !windows

package bootstrap

import "syscall"

// withUmask temporarily sets the process umask (Unix-only) and returns a
// restore func, so a permission test can pin the umask instead of inheriting
// whatever the environment happens to set. syscall.Umask is process-global
// state, so callers must NOT use t.Parallel().
func withUmask(mask int) func() {
	old := syscall.Umask(mask)
	return func() { syscall.Umask(old) }
}
