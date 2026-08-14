//go:build !linux

package selfupdate

// executableHasCapabilities: Linux file capabilities do not exist on other
// platforms, so there is never anything the replacement could lose.
func executableHasCapabilities(string) bool { return false }
