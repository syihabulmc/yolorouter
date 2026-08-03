package config

// quoteForShell wraps a path so it survives being pasted into the shell the
// operator is most likely holding. Double quotes, which both cmd.exe and
// PowerShell accept, and which the default install path needs: the machine-wide
// installer puts the deployment under %ProgramFiles%, so the unquoted hint
// splits at the space in "Program Files" and the pasted command fails to parse.
//
// Nothing is escaped inside them because nothing can be: Windows forbids " in a
// path outright, so a quoted path never contains its own delimiter.
func quoteForShell(path string) string {
	return `"` + path + `"`
}
