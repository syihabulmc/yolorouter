//go:build windows

package bootstrap

// withUmask is a no-op on windows, which has no umask equivalent. The test
// that relies on it asserts unix file-permission bits and skips itself on
// windows.
func withUmask(mask int) func() {
	return func() {}
}
