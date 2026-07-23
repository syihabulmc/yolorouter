//go:build windows

package database

// withUmask is a no-op on windows, which has no umask equivalent. The tests
// that rely on it assert unix file-permission bits and skip themselves on
// windows, so the returned restore func is never meaningfully exercised here.
func withUmask(mask int) func() {
	return func() {}
}
