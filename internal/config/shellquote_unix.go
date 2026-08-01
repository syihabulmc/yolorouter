//go:build !windows

package config

import "strings"

// quoteForShell wraps a path so it survives being pasted into the shell the
// operator is most likely holding. Single quotes, because inside them a POSIX
// shell expands nothing at all — a path containing $, a backtick or $(…) is a
// perfectly legal filename, and double quotes would let the shell run it. An
// embedded single quote is closed, escaped and reopened, the only way to get
// one through.
func quoteForShell(path string) string {
	return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
}
